package config

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"strings"
)

const (
	statusValid   = "valid"
	statusMissing = "missing"
	statusError   = "error"
)

// StatusFile describes one optional configuration file layer. Keys and Path
// are nil when the corresponding fact could not be determined.
type StatusFile struct {
	Type   string   `json:"type"`
	State  string   `json:"state"`
	Keys   *int     `json:"keys"`
	Path   *string  `json:"path"`
	Errors []string `json:"errors"`

	document map[string]any
}

// StatusEffective describes validation of the merged configuration.
type StatusEffective struct {
	State  string   `json:"state"`
	Errors []string `json:"errors"`
}

// StatusReport is the complete read-only configuration diagnostic.
type StatusReport struct {
	Files     []StatusFile    `json:"files"`
	Effective StatusEffective `json:"effective"`
}

// HasErrors reports whether a file layer or the effective configuration failed
// validation or inspection.
func (r StatusReport) HasErrors() bool {
	if r.Effective.State == statusError {
		return true
	}
	for _, file := range r.Files {
		if file.State == statusError {
			return true
		}
	}
	return false
}

// InspectStatus reads each file layer once and reports both sparse-layer and
// effective validation without changing files or loading runtime dependencies.
func InspectStatus() StatusReport {
	homebrewPath := optionalPath(PackageConfigPath)
	systemPath, userPath, pathErr := statusConfigPaths()

	files := []StatusFile{
		inspectStatusFile("homebrew", homebrewPath),
	}
	if pathErr != nil {
		files = append(files,
			statusPathError("system"),
			statusPathError("user"),
		)
	} else {
		files = append(files,
			inspectStatusFile("system", optionalPath(systemPath)),
			inspectStatusFile("user", optionalPath(userPath)),
		)
	}

	report := StatusReport{Files: files, Effective: StatusEffective{State: statusValid, Errors: []string{}}}
	for _, file := range files {
		if file.State == statusError {
			report.Effective = StatusEffective{State: statusError, Errors: []string{"one or more file layers could not be inspected"}}
			return report
		}
	}

	layers := make([]map[string]any, 0, len(files))
	for _, file := range files {
		if file.State == statusValid {
			layers = append(layers, file.document)
		}
	}
	if _, err := configFromDocuments(layers...); err != nil {
		report.Effective = StatusEffective{State: statusError, Errors: []string{"invalid effective configuration"}}
	}
	return report
}

func optionalPath(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func statusConfigPaths() (systemPath, userPath string, err error) {
	configPath, err := xdgConfigPath()
	if err != nil {
		return "", "", fmt.Errorf("resolve configuration path: %w", err)
	}
	return path.Join(configPath, systemFileName), path.Join(configPath, configFileName), nil
}

func statusPathError(layerType string) StatusFile {
	return StatusFile{
		Type:   layerType,
		State:  statusError,
		Errors: []string{"could not resolve configuration path"},
	}
}

func inspectStatusFile(layerType string, filePath *string) StatusFile {
	if filePath == nil {
		zero := 0
		return StatusFile{Type: layerType, State: statusMissing, Keys: &zero, Errors: []string{}}
	}

	file := StatusFile{Type: layerType, Path: filePath, Errors: []string{}}
	contents, err := os.ReadFile(*filePath) // #nosec G304 -- build-stamped or XDG-resolved config path
	if os.IsNotExist(err) {
		zero := 0
		file.State = statusMissing
		file.Keys = &zero
		return file
	}
	if err != nil {
		file.State = statusError
		file.Errors = []string{"could not read configuration file"}
		return file
	}

	if len(bytes.TrimSpace(contents)) == 0 {
		zero := 0
		file.State = statusValid
		file.Keys = &zero
		file.document = map[string]any{}
		return file
	}

	document, err := configDocument(contents)
	if err != nil {
		file.State = statusError
		file.Errors = []string{"configuration document must be a mapping"}
		return file
	}
	keys := configurableLeafCount(document)
	file.Keys = &keys
	if _, err := validateConfigLayer(contents); err != nil {
		file.State = statusError
		file.Errors = []string{"invalid configuration"}
		return file
	}
	file.State = statusValid
	file.document = document
	return file
}

func configurableLeafCount(document map[string]any) int {
	defaults := newConfig()
	var leaves []FieldMeta
	flattenMetas(extractWith(defaults, defaults), &leaves)

	count := 0
	for _, leaf := range leaves {
		if leaf.YAMLPath == "$.version" {
			continue
		}
		if documentHasPath(document, strings.Split(strings.TrimPrefix(leaf.YAMLPath, "$."), ".")) {
			count++
		}
	}
	return count
}

func documentHasPath(document map[string]any, segments []string) bool {
	var value any = document
	for _, segment := range segments {
		mapping, ok := value.(map[string]any)
		if !ok {
			return false
		}
		value, ok = mapping[segment]
		if !ok {
			return false
		}
	}
	return true
}
