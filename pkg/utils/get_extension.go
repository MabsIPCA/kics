package utils

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"golang.org/x/tools/godoc/util"
)

// GetExtension gets the extension of a file path
func GetExtension(path string) (string, error) {
	targets := []string{"Dockerfile", "tfvars"}

	// If the path already has an explicit extension, return it without hitting
	// the filesystem. This handles virtual (in-memory) paths such as resolved
	// Helm template manifests that do not exist on disk.
	if ext := filepath.Ext(path); ext != "" {
		return ext, nil
	}

	// No extension — need filesystem access to detect content-based types
	// (Dockerfile, tfvars, possibleDockerfile).
	fileInfo, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("file %s not found", path)
	}

	if fileInfo.IsDir() {
		return "", fmt.Errorf("the path %s is a directory", path)
	}

	base := filepath.Base(path)
	if Contains(base, targets) {
		return base, nil
	}

	isText, err := isTextFile(path)
	if err != nil {
		return "", err
	}

	if isText {
		if readPossibleDockerFile(path) {
			return "possibleDockerfile", nil
		}
	}

	return "", nil
}

func readPossibleDockerFile(path string) bool {
	path = filepath.Clean(path)
	if strings.HasSuffix(path, "gitignore") {
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.Error().Err(errClose).Msg("Error closing file")
		}
	}()
	// Create a scanner to read the file line by line
	scanner := bufio.NewScanner(file)
	// Read lines from the file
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "FROM") {
			return true
		} else if strings.HasPrefix(scanner.Text(), "#") {
			continue
		} else {
			return false
		}
	}
	return false
}

func isTextFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		log.Error().Msgf("failed to get file info: %s", err)
		return false, err
	}

	if info.IsDir() {
		return false, nil
	}

	content, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		log.Error().Msgf("failed to analyze file: %s", err)
		return false, err
	}

	content = bytes.ReplaceAll(content, []byte("\r"), []byte(""))

	isText := util.IsText(content)

	return isText, nil
}
