package helm

import (
	"os"
	"path/filepath"
	"testing"

	"helm.sh/helm/v3/pkg/cli/values"
)

// writeChart lays down a minimal chart whose template uses `required` so a plain
// render fails until the value is injected.
func writeChart(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Chart.yaml"),
		"apiVersion: v2\nname: fixme\nversion: 0.1.0\n")
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "values.yaml"), "image: {}\n")
	// `required` fails the render until image.repository is set; nil-map access on
	// image.tag would also fail — both are fixer-recoverable.
	mustWrite(t, filepath.Join(dir, "templates", "cm.yaml"),
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\ndata:\n"+
			"  repo: {{ required \"image.repository is required\" .Values.image.repository | quote }}\n")
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFixerRecoversRequiredValue(t *testing.T) {
	dir := writeChart(t)

	// Without the fixer the render must fail (current fail-closed behavior).
	_, _, err := runInstall([]string{dir}, newClient(), &values.Options{}, false)
	if err == nil {
		t.Fatal("expected plain render to fail on required value, got nil")
	}

	// With the fixer the chart should render after the placeholder is injected.
	rel, _, err := runInstall([]string{dir}, newClient(), &values.Options{}, true)
	if err != nil {
		t.Fatalf("expected fixer to resolve render, got error: %v", err)
	}
	if rel == nil || rel.Manifest == "" {
		t.Fatal("expected a non-empty manifest from the fixed render")
	}
	t.Logf("fixed manifest:\n%s", rel.Manifest)
}
