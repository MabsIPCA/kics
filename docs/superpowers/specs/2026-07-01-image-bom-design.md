# Design: `--image-bom` Docker Image Bill of Materials

**Date:** 2026-07-01
**Status:** Approved
**Branch:** `feat/image-bom`

## Goal

Export a Bill of Materials (BoM) of the Docker/OCI image references found while
scanning Infrastructure-as-Code. The primary consumption model is **feeding
vulnerability scanners** (Grype / Trivy, which this repo already configures via
`.grype.yaml` / `trivy.image.yaml`): a downstream step iterates the SBOM's
container components and scans each image (`grype <ref>` / `trivy image <ref>`,
or `trivy sbom`).

This BoM is an **inventory of image references** (e.g. `nginx:1.21`, `redis:7`),
not an SBOM of the packages *inside* those images.

## Scope

Image references are extracted from every source KICS already parses:

- **Dockerfile** — `FROM` instructions (including multi-stage builds)
- **docker-compose** — `image:` field in services
- **Kubernetes** — container `image:` fields (Deployments, Pods, StatefulSets,
  initContainers, etc.)
- **Helm** — images in rendered chart templates

## Non-goals

- Scanning image contents / resolving CVEs (delegated to Grype/Trivy downstream)
- Pulling images or resolving `:latest` to a digest over the network
- Folding images into the existing cloud-resource `--bom` flow or the existing
  file-based `cyclonedx` report

## User-facing surface

A dedicated boolean flag:

```bash
kics scan -p . --image-bom
# writes kics-image-bom.json  (CycloneDX 1.5 JSON)
```

The file is written to `--output-path` (default: current directory) with the
fixed name `kics-image-bom.json`. The flag is independent of `--report-formats`
and of the cloud-resource `--bom` flag.

## Architecture

New flag `--image-bom` → `scan.Parameters.ImageBOM` → `scan.Client`. In
`postScan`, after the summary is built, when `ImageBOM` is set, a new
`pkg/imagebom` package extracts image references from the already-parsed
`scanResults.Files` (`model.FileMetadatas` — each carries the normalized
`Document`), dedupes them, and writes the SBOM. Extraction is fully independent
of the Rego query engine.

Extraction point: `pkg/scan/post_scan.go`, inside `postScan`, using
`scanResults.Files`.

### Package `pkg/imagebom`

| File            | Responsibility |
|-----------------|----------------|
| `imagebom.go`   | `Extract(files model.FileMetadatas) []Image`; dispatch per `file.Kind`; dedupe into a map; merge provenance. |
| `dockerfile.go` | Walk `document[i].command[stage][_]` for `Cmd == "from"`; track `AS` stage names and skip stage-alias `FROM`s; skip `scratch`; skip `${VAR}` template refs. |
| `yaml.go`       | Recursive walk of the parsed `Document`; record string values under an exact `image` key (covers k8s containers/initContainers, compose `services.*.image`, Helm-rendered templates). |
| `image.go`      | `Image` struct; parse refs via `github.com/distribution/reference` into registry/repository/tag/digest; build PURL. |
| `cyclonedx.go`  | Build a CycloneDX 1.5 **JSON** document (dedicated json-tagged structs). |

The existing `pkg/report/model/cyclonedx.go` is XML-only and models scanned
*files*, so it is not reused; a small JSON model lives in `imagebom`.

### Data model

```go
type Image struct {
    Ref        string   // reference as written, e.g. "nginx:1.21"
    Registry   string   // "docker.io" default, or explicit (incl. host:port)
    Repository string   // "library/nginx"
    Tag        string   // "1.21" (may be empty if digest-only)
    Digest     string   // "sha256:..." if present
    Sources    []Source // provenance
}

type Source struct {
    File string
    Line int
}
```

Dedup key = normalized reference (registry/repository:tag@digest). Multiple
occurrences merge into a single `Image` with all `Sources` appended.

### CycloneDX output

Each `Image` becomes a component:

- `type: "container"`
- `name`: repository (or full ref)
- `version`: tag or digest
- `purl`: `pkg:docker/<repository>@<tag|digest>`, with `?repository_url=<registry>`
  qualifier when the registry is not `docker.io`
- `bom-ref`: the normalized reference
- `properties`: `[{name:"kics:source:file",value:...},{name:"kics:source:line",value:...}]`
  for each source — keeps the document standards-compliant while preserving
  where-found provenance

Top-level: `bomFormat: "CycloneDX"`, `specVersion: "1.5"`,
`serialNumber: urn:uuid:...`, `version: 1`, `metadata` with timestamp + KICS
tool entry.

## Data flow

```
postScan
  └─ if ImageBOM:
       images := imagebom.Extract(scanResults.Files)
       bom    := imagebom.BuildCycloneDx(images)
       report.ExportJSONReport(outputPath, "kics-image-bom", bom)
```

## Error handling

- Best-effort per file: a malformed/unexpected document logs a warning and is
  skipped; extraction never aborts the scan.
- No images found → write a valid **empty** SBOM (`components: []`) + info log,
  so downstream tooling does not break on a missing file.
- Write errors are logged and returned, following the existing `resolveOutputs`
  pattern in `post_scan.go`.

## Testing (TDD)

Unit tests:

- **Reference parsing** — docker.io defaults, digest-only refs,
  `localhost:5000/img:tag` (port-in-registry), explicit registry, PURL output.
- **Dockerfile extraction** — single `FROM`; multi-stage with `AS` alias
  skipping; `scratch` skip; `--platform` flag present; `${VAR}` skip.
- **YAML extraction** — k8s Deployment (`containers` + `initContainers`);
  docker-compose `services.*.image`; nested Helm-style output.
- **Dedup** — same image across multiple files merges `Sources`.
- **CycloneDX JSON** — golden/structural test: valid JSON, `type: container`
  components, provenance properties present, empty-input yields valid empty SBOM.

E2E:

- One test exercising `--image-bom` end-to-end against a small fixture tree and
  asserting `kics-image-bom.json` is produced with the expected components.

## Edge cases (v1)

- A bare `image:` key in a non-container YAML context may yield a false positive
  — acceptable for v1, refinable later.
- ARG/`${VAR}`-templated Dockerfile `FROM`s are skipped (not scannable refs).

## Dependencies

- Promote `github.com/distribution/reference v0.6.0` (already transitive) to a
  direct dependency for robust reference parsing.
