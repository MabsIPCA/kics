package helm

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Checkmarx/kics/v2/pkg/model"
	masterUtils "github.com/Checkmarx/kics/v2/pkg/utils"
	"github.com/pkg/errors"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/release"
)

// Resolver is an instance of the helm resolver
type Resolver struct {
}

// splitManifest keeps the information of the manifest split by source
type splitManifest struct {
	path       string
	content    []byte
	original   []byte
	splitID    string
	splitIDMap map[int]interface{}
}

const (
	kicsHelmID      = "# KICS_HELM_ID_"
	maxCombinations = 128
)

// Resolve will render the passed helm chart and return its content ready for parsing
func (r *Resolver) Resolve(filePath string) (model.ResolvedFiles, error) {
	// handle panic during resolve process
	defer func() {
		if r := recover(); r != nil {
			errMessage := "Recovered from panic during resolve of file " + filePath
			masterUtils.HandlePanic(r, errMessage)
		}
	}()
	splitRenderedManifests, excluded, err := renderHelm(filePath, true)
	if err != nil { // return error to be logged
		return model.ResolvedFiles{}, errors.New("failed to render helm chart")
	}
	var rFiles = model.ResolvedFiles{
		Excluded: excluded,
	}
	for k, splitRenderedManifestsPerValue := range splitRenderedManifests {
		for _, split := range *splitRenderedManifestsPerValue {
			subFolder := filepath.Base(filePath)

			splitPath := filepath.Join(
				strings.Split(split.path, getPathSeparator(split.path))[1:]...,
			)

			originalPath := filepath.Join(filepath.Dir(filePath), subFolder, splitPath)

			// append resolved helm file to the list
			rFiles.File = append(rFiles.File, model.ResolvedHelm{
				FileName:     originalPath,
				Content:      split.content,
				OriginalData: split.original,
				SplitID:      split.splitID,
				IDInfo:       split.splitIDMap,
				ContextSelector: func() string {
					// only add context selector when multiple values files are used and is not the default values.yaml
					if len(splitRenderedManifests) == 1 && k == "values.yaml" {
						return ""
					} else {
						return k
					}
				}(),
			})
		}
	}
	return rFiles, nil
}

// SupportedTypes returns the supported fileKinds for this resolver
func (r *Resolver) SupportedTypes() []model.FileKind {
	return []model.FileKind{model.KindHELM}
}

// renderHelm will use helm library to render helm charts
func renderHelm(path string, experimentalRendering bool) (map[string]*[]splitManifest, []string, error) {
	splitManifests := map[string]*[]splitManifest{}
	excludedMap := map[string][]string{}
	client := newClient()
	manifest, excluded, err := runInstall([]string{path}, client, &values.Options{})
	for _, v := range excluded {
		excludedMap[v] = []string{}
	}
	excludedMap["values.yaml"] = excluded
	if err != nil {
		return nil, []string{}, err
	}
	split, err := splitManifestYAML(manifest)
	splitManifests["values.yaml"] = split
	if err != nil {
		return nil, []string{}, err
	}
	if experimentalRendering {
		otherValuesFiles := getOtherValuesFiles(path)
		if len(otherValuesFiles) > 0 {
			// Generate all possible combinations of values files
			combinations := generateCombinations(otherValuesFiles)
			for _, combo := range combinations {
				if len(combo) == 0 {
					continue // skip empty combination, already handled by default values.yaml
				}
				expManifest, expExcluded, err := runInstall([]string{path}, client, &values.Options{
					ValueFiles: combo,
				})
				for _, v := range expExcluded {
					excludedMap[v] = []string{}
				}
				if err != nil {
					return nil, []string{}, err
				}
				expSplit, err := splitManifestYAML(expManifest)
				if err != nil {
					return nil, []string{}, err
				}
				// create a key from all filenames in the combination
				comboKey := generateComboKey(combo)
				splitManifests[comboKey] = expSplit
			}
		}
	}

	var excludedMerged []string
	for k := range excludedMap {
		// merge excluded values files, filtering duplicates
		excludedMerged = append(excludedMerged, k)
	}
	return splitManifests, excludedMerged, nil
}

func getOtherValuesFiles(path string) []string {
	otherValuesFiles := []string{}

	files, err := filepath.Glob(filepath.Join(path, "*.yaml"))
	if err != nil {
		return otherValuesFiles
	}

	for _, file := range files {
		if filepath.Base(file) != "values.yaml" && filepath.Base(file) != "Chart.yaml" {
			otherValuesFiles = append(otherValuesFiles, file)
		}
	}

	return otherValuesFiles
}

// generateCombinations generates all possible combinations (power set) of the input slice
func generateCombinations(files []string) [][]string {
	var result [][]string
	n := len(files)

	// Total number of combinations is 2^n (excluding empty set)
	totalCombinations := 1 << n // equivalent to 2^n

	// Limit the number of combinations to avoid excessive processing
	if totalCombinations > maxCombinations {
		totalCombinations = maxCombinations
	}

	// Iterate through all possible combinations (skip 0 to exclude empty set)
	for combinationMask := 1; combinationMask < totalCombinations; combinationMask++ {
		var combo []string

		// Check each file's position in the current combination
		for fileIndex := 0; fileIndex < n; fileIndex++ {
			fileBit := 1 << fileIndex
			isFileIncluded := combinationMask&fileBit != 0

			if isFileIncluded {
				combo = append(combo, files[fileIndex])
			}
		}
		result = append(result, combo)
	}
	return result
}

// generateComboKey creates a unique key from a combination of values files
func generateComboKey(combo []string) string {
	var fileNames []string
	for _, file := range combo {
		_, fileName := filepath.Split(file)
		fileNames = append(fileNames, fileName)
	}
	return strings.Join(fileNames, "+")
}

// splitManifestYAML will split the rendered file and return its content by template as well as the template path
func splitManifestYAML(template *release.Release) (*[]splitManifest, error) {
	sources := make([]*chart.File, 0)
	sources = updateName(sources, template.Chart, template.Chart.Name())
	var manifests []splitManifest
	splitTemplate := strings.Split(template.Manifest, "---") // splitTemplate manifest by '---'
	origData := toMap(sources)
	for _, manifest := range splitTemplate {
		var lineID string
		for _, line := range strings.Split(manifest, "\n") {
			if strings.Contains(line, kicsHelmID) {
				lineID = line // get auxiliary line id
				break
			}
		}
		path := strings.Split(strings.TrimPrefix(manifest, "\n# Source: "), "\n") // get source of splitTemplate yaml
		// ignore auxiliary files used to render chart
		if path[0] == "" || isEmptyFileRender(path) {
			continue
		}
		if origData[filepath.FromSlash(path[0])] == nil {
			continue
		}
		idMap, err := getIDMap(origData[filepath.FromSlash(path[0])])
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, splitManifest{
			path:       path[0],
			content:    []byte(strings.ReplaceAll(manifest, "\r", "")),
			original:   origData[filepath.FromSlash(path[0])], // get original data from template
			splitID:    lineID,
			splitIDMap: idMap,
		})
	}
	return &manifests, nil
}

// toMap will convert to map original data having the path as it's key
func toMap(files []*chart.File) map[string][]byte {
	mapFiles := make(map[string][]byte)
	for _, file := range files {
		mapFiles[file.Name] = []byte(strings.ReplaceAll(string(file.Data), "\r", ""))
	}
	return mapFiles
}

// updateName will update the templates name as well as its dependencies
func updateName(template []*chart.File, charts *chart.Chart, name string) []*chart.File {
	if name != charts.Name() {
		name = filepath.Join(name, charts.Name())
	}
	for _, temp := range charts.Templates {
		temp.Name = filepath.Join(name, temp.Name)
	}
	template = append(template, charts.Templates...)
	for _, dep := range charts.Dependencies() {
		template = updateName(template, dep, filepath.Join(name, "charts"))
	}
	return template
}

// getIdMap will construct a map with ids with the corresponding lines as keys
// for use in detector
func getIDMap(originalData []byte) (map[int]interface{}, error) {
	ids := make(map[int]interface{})
	mapLines := make(map[int]int)
	idHelm := -1
	for line, stringLine := range strings.Split(string(originalData), "\n") {
		if strings.Contains(stringLine, kicsHelmID) {
			id, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(stringLine, kicsHelmID), ":"))
			if err != nil {
				return nil, err
			}
			if idHelm == -1 {
				idHelm = id
				mapLines[line] = line
			} else {
				ids[idHelm] = mapLines
				mapLines = make(map[int]int)
				idHelm = id
				mapLines[line] = line
			}
		} else if idHelm != -1 {
			mapLines[line] = line
		}
	}
	ids[idHelm] = mapLines

	return ids, nil
}

func getPathSeparator(path string) string {
	if matched, err := regexp.MatchString(`[a-zA-Z0-9_\/-]+(\[a-zA-Z0-9_\/-]+)*`, path); matched && err == nil {
		return "/"
	} else if matched, err := regexp.MatchString(`[a-z0-9_.$-]+(\\[a-z0-9_.$-]+)*`, path); matched && err == nil {
		return "\\"
	}
	return ""
}

func isEmptyFileRender(fileLines []string) bool {
	for _, line := range fileLines[1:] {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine != "" && !strings.HasPrefix(trimmedLine, "#") {
			return false
		}
	}
	return true
}
