package helm

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	zlog "github.com/rs/zerolog/log"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/release"
)

// credit: https://github.com/helm/helm

var (
	settings = cli.New()
)

func runInstall(args []string, client *action.Install,
	valueOpts *values.Options, fixRender bool) (*release.Release, []string, error) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	if client.Version == "" && client.Devel {
		client.Version = ">0.0.0-0"
	}

	name, charts, err := client.NameAndChart(args)
	if err != nil {
		return nil, []string{}, err
	}
	client.ReleaseName = name

	cp, err := client.LocateChart(charts, settings)
	if err != nil {
		return nil, []string{}, err
	}

	p := getter.All(settings)

	// Check chart dependencies to make sure all are present in /charts
	chartRequested, err := loader.Load(cp)
	if err != nil {
		return nil, []string{}, err
	}

	excluded := getExcluded(chartRequested, cp)

	chartRequested = setID(chartRequested)

	if instErr := checkIfInstallable(chartRequested); instErr != nil {
		return nil, []string{}, instErr
	}

	client.Namespace = "kics-namespace"
	helmRelease, err := renderWithFixer(client, chartRequested, valueOpts, p, fixRender)
	if err != nil {
		return nil, []string{}, err
	}
	return helmRelease, excluded, nil
}

// renderWithFixer renders the chart with client.Run. When fixRender is false it
// behaves exactly as before: a single render, returning any error untouched.
//
// When fixRender is true it engages the helm fixer loop: on a render error it
// classifies the failure (see parseError) and injects a placeholder value
// (--set) or a kubeVersion override, then re-renders, looping up to
// maxFixIterations. The loop stops early when an error repeats (no progress),
// when the error is unfixable by value injection, or when it is a documented
// non-fixable class (malformed YAML, a nil-pointer on a Helm built-in, or
// missing dependencies — which KICS resolves separately). On any stop the
// ORIGINAL render error is returned, preserving today's fail-closed behavior.
func renderWithFixer(client *action.Install, chartRequested *chart.Chart,
	valueOpts *values.Options, providers getter.Providers, fixRender bool) (*release.Release, error) {
	patch := map[string]string{}
	seenErrs := map[string]bool{}

	for attempt := 1; attempt <= maxFixIterations; attempt++ {
		vals, err := mergeValuesWithPatch(valueOpts, patch, providers)
		if err != nil {
			// The values themselves (a -f file or a prior --set) do not parse;
			// no further --set injection can repair that.
			return nil, err
		}

		helmRelease, runErr := client.Run(chartRequested, vals)
		if runErr == nil {
			if attempt > 1 {
				zlog.Debug().Str("chart", chartRequested.Name()).Int("attempts", attempt).
					Msg("helm fixer: render resolved after value injection")
			}
			return helmRelease, nil
		}

		// Without the fixer, preserve the original single-render behavior.
		if !fixRender {
			return nil, runErr
		}

		errStr := runErr.Error()

		// Repeated identical error => the last injection made no progress. Stop.
		if seenErrs[errStr] {
			return nil, runErr
		}
		seenErrs[errStr] = true

		// Documented non-fixable classes: no value injection can help, so log and
		// stop rather than spin. (Kept explicit per the fixer design.)
		switch {
		case isUnparseableYAML(errStr):
			zlog.Debug().Str("chart", chartRequested.Name()).
				Msg("helm fixer: skipping non-fixable malformed YAML")
			return nil, runErr
		case isUnsupportedBuiltin(errStr):
			zlog.Debug().Str("chart", chartRequested.Name()).
				Msg("helm fixer: skipping non-fixable nil pointer on helm built-in")
			return nil, runErr
		case isMissingDependency(errStr):
			zlog.Debug().Str("chart", chartRequested.Name()).
				Msg("helm fixer: skipping missing-dependency error (resolved separately by KICS)")
			return nil, runErr
		}

		kind, path, value, ok := parseError(errStr)
		if !ok {
			return nil, runErr
		}

		if kind == kindKubeVersion {
			// kubeVersion is a render-wide override, not a --set value.
			if kv, kvErr := chartutil.ParseKubeVersion(value); kvErr == nil {
				client.KubeVersion = kv
			}
			continue
		}
		patch[path] = value
	}

	return nil, errors.Errorf("helm fixer: exhausted %d fix iterations", maxFixIterations)
}

// mergeValuesWithPatch merges the chart's configured value options together with
// the fixer's accumulated --set patch (path=value) overrides.
func mergeValuesWithPatch(valueOpts *values.Options, patch map[string]string,
	providers getter.Providers) (map[string]interface{}, error) {
	if len(patch) == 0 {
		return valueOpts.MergeValues(providers)
	}
	extra := make([]string, 0, len(patch))
	for k, v := range patch {
		extra = append(extra, fmt.Sprintf("%s=%s", k, v))
	}
	merged := &values.Options{
		ValueFiles:    valueOpts.ValueFiles,
		Values:        append(append([]string{}, valueOpts.Values...), extra...),
		StringValues:  valueOpts.StringValues,
		FileValues:    valueOpts.FileValues,
		JSONValues:    valueOpts.JSONValues,
		LiteralValues: valueOpts.LiteralValues,
	}
	return merged.MergeValues(providers)
}

// checkIfInstallable validates if a chart can be installed
//
// Application chart type is only installable
func checkIfInstallable(ch *chart.Chart) error {
	switch ch.Metadata.Type {
	case "", "application":
		return nil
	}
	return errors.Errorf("%s charts are not installable", ch.Metadata.Type)
}

// newClient will create a new instance on helm client used to render the chart
func newClient() *action.Install {
	cfg := new(action.Configuration)
	client := action.NewInstall(cfg)
	client.DryRun = true
	client.ReleaseName = "kics-helm"
	client.Replace = true // Skip the name check
	client.ClientOnly = true
	client.APIVersions = chartutil.VersionSet([]string{})
	client.IncludeCRDs = false
	return client
}

// setID will add auxiliary lines for each template as well as its dependencies
func setID(chartReq *chart.Chart) *chart.Chart {
	for _, temp := range chartReq.Templates {
		temp = addID(temp)
		if temp != nil {
			continue
		}
	}
	for _, dep := range chartReq.Dependencies() {
		dep = setID(dep)
		if dep != nil {
			continue
		}
	}
	return chartReq
}

// addID will add auxiliary lines used to detect line
// one for each "apiVersion:" where the id will be the line
func addID(file *chart.File) *chart.File {
	split := strings.Split(string(file.Data), "\n")
	for i := 0; i < len(split); i++ {
		if strings.Contains(split[i], "apiVersion:") {
			split = append(split, "")
			copy(split[i+1:], split[i:])
			split[i] = fmt.Sprintf("# KICS_HELM_ID_%d:", i)
			i++
		}
	}
	file.Data = []byte(strings.Join(split, "\n"))
	return file
}

// getExcluded will return all files rendered to be excluded from scan
func getExcluded(chartData *chart.Chart, chartPath string) []string {
	excluded := make([]string, 0)
	for _, file := range chartData.Raw {
		excluded = append(excluded, filepath.Join(chartPath, file.Name))
	}

	return excluded
}
