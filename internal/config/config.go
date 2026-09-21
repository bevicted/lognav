package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/xdg"
	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

const (
	configFileName   = "user.yaml"
	systemFileName   = "system.yaml"
	userConfigName   = "user config"
	systemConfigName = "system config"
	CurrentVersion   = 1
)

// ErrUnknownConfigurationKey reports that a valid dotted key does not exist in
// the effective configuration view.
var ErrUnknownConfigurationKey = errors.New("unknown configuration key")

// PackageConfigPath is an optional read-only defaults file stamped at build
// time with -ldflags -X. Ordinary builds leave it empty.
var PackageConfigPath = ""

// New returns a Config populated with the lognav default values.
// Tests use this directly; production code calls LoadConfig() which falls
// back to New() when no file is found.
func New() *Config {
	return newConfig()
}

// NormalizeYAMLPath canonicalizes a single yamlpath argument: the root spellings
// "", ".", "$", and "$." all become "$"; a leading "$" is preserved as-is; a
// leading "." is prefixed with "$" (".keys.accept" -> "$.keys.accept"); any
// other bare path is prefixed with "$." ("core.enableMouse" -> "$.core.enableMouse").
func NormalizeYAMLPath(arg string) string {
	switch arg {
	case "", ".", "$", "$.":
		return "$"
	}
	switch arg[0] {
	case '$':
		return arg
	case '.':
		return "$" + arg
	default:
		return "$." + arg
	}
}

func GetConfigPath() (string, error) {
	p, err := xdg.GetConfigPath()
	if err != nil {
		return "", fmt.Errorf("load config: resolve path: %w", err)
	}
	return path.Join(p, configFileName), nil
}

func getSystemConfigPath() (string, error) {
	p, err := xdg.GetConfigPath()
	if err != nil {
		return "", fmt.Errorf("load config: resolve path: %w", err)
	}
	return path.Join(p, systemFileName), nil
}

// LoadConfig reads public defaults, optional stamped Homebrew defaults,
// system.yaml, and user.yaml in that order. Neither file is changed while
// loading. Every error is wrapped with "load config:"; callers should not wrap
// it again.
func LoadConfig() (*Config, error) {
	logger := slog.Default().With(logging.KeyComponent, "config")
	start := time.Now()

	packageBytes, err := readPackageConfig()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	systemPath, err := getSystemConfigPath()
	if err != nil {
		return nil, err // already wrapped
	}
	systemBytes, err := readOptionalConfigFile(systemPath, systemConfigName)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	p, err := GetConfigPath()
	if err != nil {
		return nil, err // already wrapped
	}
	userBytes, err := readOptionalConfigFile(p, userConfigName)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	cfg, err := configFromLayers(
		configLayer{name: "package defaults", bytes: packageBytes},
		configLayer{name: systemConfigName, bytes: systemBytes},
		configLayer{name: userConfigName, bytes: userBytes},
	)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	logger.Info("config loaded", "path", p, "version", cfg.Version, logging.KeyDurationMS, time.Since(start).Milliseconds())
	return cfg, nil
}

func readPackageConfig() ([]byte, error) {
	if PackageConfigPath == "" {
		return nil, nil
	}
	b, err := readOptionalConfigFile(PackageConfigPath, "package defaults")
	if err != nil {
		return nil, err
	}
	return b, nil
}

func readOptionalConfigFile(p, name string) ([]byte, error) {
	b, err := os.ReadFile(p) // #nosec G304 -- build-stamped or xdg-resolved config path
	if err == nil {
		return b, nil
	}
	if os.IsNotExist(err) {
		return nil, nil
	}
	return nil, fmt.Errorf("read %s %s: %w", name, p, err)
}

type configLayer struct {
	name  string
	bytes []byte
}

// configFromLayers validates each supplied layer before merging mappings
// recursively. Scalars and sequences replace lower-layer values.
func configFromLayers(layers ...configLayer) (*Config, error) {
	base, err := yaml.Marshal(New())
	if err != nil {
		return nil, err
	}
	merged, err := configDocument(base)
	if err != nil {
		return nil, err
	}
	for _, layer := range layers {
		if len(bytes.TrimSpace(layer.bytes)) == 0 {
			continue
		}
		document, err := validateConfigLayer(layer.bytes)
		if err != nil {
			if layer.name == userConfigName {
				return nil, err
			}
			return nil, fmt.Errorf("%s: %w", layer.name, err)
		}
		mergeConfigMappings(merged, document)
	}

	b, err := yaml.Marshal(merged)
	if err != nil {
		return nil, err
	}
	cfg := New()
	if err := yaml.UnmarshalWithOptions(b, cfg, yaml.DisallowUnknownField()); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	return cfg, nil
}

func configDocument(b []byte) (map[string]any, error) {
	var value any
	if err := yaml.Unmarshal(b, &value); err != nil {
		return nil, err
	}
	document, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("configuration document must be a map")
	}
	return document, nil
}

func mergeConfigMappings(base, overlay map[string]any) {
	for key, value := range overlay {
		if lower, ok := base[key].(map[string]any); ok {
			if upper, ok := value.(map[string]any); ok {
				mergeConfigMappings(lower, upper)
				continue
			}
		}
		base[key] = value
	}
}

// configurableLeafPath reports whether yamlPath addresses a single settable
// config leaf (after normalization), returning the leaf's FieldMeta when it
// does. Sections, unknown keys, yaml:"-" fields, and index paths are not
// leaves. Derived from the type structure only; no loaded config required.
func configurableLeafPath(yamlPath string) (FieldMeta, bool) {
	norm := NormalizeYAMLPath(yamlPath)
	defaults := newConfig()
	var leaves []FieldMeta
	flattenMetas(extractWith(defaults, defaults), &leaves)
	for _, m := range leaves {
		if m.YAMLPath == norm {
			return m, true
		}
	}
	return FieldMeta{}, false
}

// redactedConfig returns a copy safe for every configuration read surface.
// OpRef fields intentionally remain visible because they are 1Password
// references, not secrets.
func redactedConfig(cfg *Config) Config {
	c := *cfg
	c.ICL.Environments = make(map[string]ICLEnvironmentConfig, len(cfg.ICL.Environments))
	for cname, environment := range cfg.ICL.Environments {
		if environment.APIKey != "" {
			environment.APIKey = "redacted"
		}
		c.ICL.Environments[cname] = environment
	}
	return c
}

// GetConfigValue returns the config subtree at yamlPath as a generic Go value
// suitable for re-marshalling in any format. The IBM Cloud API key is redacted.
func GetConfigValue(cfg *Config, yamlPath string) (any, error) {
	c := redactedConfig(cfg)
	b, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}

	yp, err := yaml.PathString(yamlPath)
	if err != nil {
		return nil, err
	}

	var res any
	if err := yp.Read(bytes.NewReader(b), &res); err != nil {
		return nil, err
	}

	return res, nil
}

// GetConfig returns the config subtree at yamlPath rendered as YAML.
func GetConfig(cfg *Config, yamlPath string) (string, error) {
	res, err := GetConfigValue(cfg, yamlPath)
	if err != nil {
		return "", err
	}

	b, err := yaml.Marshal(res)
	if err != nil {
		return "", err
	}

	return string(b), nil
}

// EffectiveConfigValue returns a simple dotted-key value from the complete,
// display-only effective configuration, with API keys redacted.
func EffectiveConfigValue(cfg *Config, key string) (any, error) {
	view, err := effectiveConfigView(cfg)
	if err != nil {
		return nil, err
	}

	var value any = view
	for part := range strings.SplitSeq(key, ".") {
		mapping, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownConfigurationKey, key)
		}
		value, ok = mapping[part]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownConfigurationKey, key)
		}
	}
	return value, nil
}

// EffectiveConfig returns the full effective configuration rendered as YAML.
// It is display-only because it includes defaults and redacted secrets.
func EffectiveConfig(cfg *Config) (string, error) {
	view, err := effectiveConfigView(cfg)
	if err != nil {
		return "", err
	}
	b, err := yaml.Marshal(view)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// EffectiveConfigJSONValue returns the full effective configuration as a value
// suitable for JSON marshaling.
func EffectiveConfigJSONValue(cfg *Config) (any, error) {
	return effectiveConfigView(cfg)
}

func effectiveConfigView(cfg *Config) (map[string]any, error) {
	c := redactedConfig(cfg)
	b, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}

	view := make(map[string]any)
	if err := yaml.Unmarshal(b, &view); err != nil {
		return nil, err
	}
	return view, nil
}

// SetValue parses value according to the target leaf's type and sets it at
// yamlPath within the YAML document src, preserving unrelated content
// (comments, sibling keys, the on-disk secret). yamlPath must address a
// configurable leaf. The result is validated against public defaults.
func SetValue(src []byte, yamlPath, value string) ([]byte, error) {
	out, leaf, parsed, norm, err := setValueUnchecked(src, yamlPath, value)
	if err != nil {
		return nil, err
	}
	if err := validateConfigBytes(out); err != nil {
		return nil, setValueError(leaf, parsed, norm, err)
	}
	return out, nil
}

// setValueUnchecked applies one AST edit without decoding the prior document.
// This permits SetConfig to repair an invalid target before validating the
// proposed document against its package base.
func setValueUnchecked(src []byte, yamlPath, value string) ([]byte, FieldMeta, any, string, error) {
	leaf, ok := configurableLeafPath(yamlPath)
	if !ok {
		return nil, FieldMeta{}, nil, "", fmt.Errorf("%q is not a configurable field", NormalizeYAMLPath(yamlPath))
	}
	norm := NormalizeYAMLPath(yamlPath)
	parsed, err := parseSetValue(leaf, value)
	if err != nil {
		return nil, FieldMeta{}, nil, "", fmt.Errorf("invalid value for %q: %w", norm, err)
	}
	if len(bytes.TrimSpace(src)) == 0 {
		src = fmt.Appendf(nil, "version: %d\n", CurrentVersion)
	}
	out, err := applyToYAML(src, norm, parsed)
	if err != nil {
		return nil, FieldMeta{}, nil, "", err
	}
	return out, leaf, parsed, norm, nil
}

// applyToYAML edits src in-place (AST level) to set norm to val. If the leaf
// already exists it is replaced; otherwise a merge fragment is appended at the
// nearest existing ancestor mapping.
func applyToYAML(src []byte, norm string, val any) ([]byte, error) {
	file, err := parser.ParseBytes(src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	p, err := yaml.PathString(norm)
	if err != nil {
		return nil, err
	}

	var dummy any
	if p.Read(bytes.NewReader(src), &dummy) == nil {
		return applyReplace(file, p, val)
	}
	return applyMerge(src, file, norm, val)
}

// applyReplace replaces the node at p with val, preserving comments and siblings.
func applyReplace(file *ast.File, p *yaml.Path, val any) ([]byte, error) {
	vb, err := yaml.Marshal(val)
	if err != nil {
		return nil, err
	}
	if err := p.ReplaceWithReader(file, bytes.NewReader(vb)); err != nil {
		return nil, err
	}
	return []byte(file.String()), nil
}

// applyMerge appends val at norm via a merge fragment on the nearest existing ancestor.
func applyMerge(src []byte, file *ast.File, norm string, val any) ([]byte, error) {
	anchor, frag, err := buildMergeFragment(src, norm, val)
	if err != nil {
		return nil, err
	}
	ap, err := yaml.PathString(anchor)
	if err != nil {
		return nil, err
	}
	// MergeFromReader serializes an empty root mapping as an invalid flow map.
	// Replace the empty root with the block-mapping fragment instead.
	if anchor == "$" {
		if root, ok := file.Docs[0].Body.(*ast.MappingNode); ok && len(root.Values) == 0 {
			fragment, err := parser.ParseBytes(frag, parser.ParseComments)
			if err != nil {
				return nil, err
			}
			file.Docs[0].Body = fragment.Docs[0].Body
			return []byte(file.String()), nil
		}
	}
	if err := ap.MergeFromReader(file, bytes.NewReader(frag)); err != nil {
		return nil, err
	}
	return []byte(file.String()), nil
}

// parseSetValue converts the raw CLI value string into a Go value suitable for
// re-marshalling. Numeric/bool/list leaves are parsed as YAML (so "50" -> int,
// "[a, b]" -> sequence); every other leaf (string, color, NotifyStyle, CRN) is
// taken literally, which is required because hex colors and other "#..." values
// would otherwise be read by YAML as comments.
func parseSetValue(leaf FieldMeta, value string) (any, error) {
	if strings.HasPrefix(leaf.GoType, "[]") || strings.HasPrefix(leaf.GoType, "map[") || isScalarYAMLType(leaf.GoType) {
		var v any
		if err := yaml.Unmarshal([]byte(value), &v); err != nil {
			return nil, err
		}
		return v, nil
	}
	return value, nil
}

func isScalarYAMLType(goType string) bool {
	switch goType {
	case "bool", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64":
		return true
	}
	return false
}

// buildMergeFragment returns the anchor path and a YAML fragment that, merged at
// the anchor, creates the missing leaf at norm without disturbing siblings.
// norm is depth 1 ("$.field") or depth 2 ("$.section.field").
func buildMergeFragment(src []byte, norm string, val any) (string, []byte, error) {
	segs := strings.Split(strings.TrimPrefix(norm, "$."), ".")
	switch len(segs) {
	case 1:
		frag, err := yaml.Marshal(map[string]any{segs[0]: val})
		return "$", frag, err
	case 2:
		secPath := "$." + segs[0]
		if sp, perr := yaml.PathString(secPath); perr == nil {
			var d any
			if sp.Read(bytes.NewReader(src), &d) == nil {
				frag, err := yaml.Marshal(map[string]any{segs[1]: val})
				return secPath, frag, err
			}
		}
		frag, err := yaml.Marshal(map[string]any{segs[0]: map[string]any{segs[1]: val}})
		return "$", frag, err
	default:
		return "", nil, fmt.Errorf("unsupported path depth: %q", norm)
	}
}

// UnsetValue removes yamlPath's leaf from the YAML document src and returns the
// new document, reverting the field to its built-in default. yamlPath must
// address a configurable leaf. An absent path (or empty src) is a no-op and
// returns src unchanged. Removing the last field of a section also drops the
// now-empty section. The result is validated against public defaults.
func UnsetValue(src []byte, yamlPath string) ([]byte, error) {
	out, norm, err := unsetValueUnchecked(src, yamlPath)
	if err != nil {
		return nil, err
	}
	if err := validateConfigBytes(out); err != nil {
		return nil, fmt.Errorf("unset %q produced invalid config: %w", norm, err)
	}
	return out, nil
}

func unsetValueUnchecked(src []byte, yamlPath string) ([]byte, string, error) {
	if _, ok := configurableLeafPath(yamlPath); !ok {
		return nil, "", fmt.Errorf("%q is not a configurable field", NormalizeYAMLPath(yamlPath))
	}
	norm := NormalizeYAMLPath(yamlPath)
	if len(bytes.TrimSpace(src)) == 0 {
		return src, norm, nil
	}
	file, err := parser.ParseBytes(src, parser.ParseComments)
	if err != nil {
		return nil, "", fmt.Errorf("parse config: %w", err)
	}
	root, ok := file.Docs[0].Body.(*ast.MappingNode)
	if !ok {
		return src, norm, nil
	}

	segs := strings.Split(strings.TrimPrefix(norm, "$."), ".")
	switch len(segs) {
	case 1:
		root.Values = removeKey(root.Values, segs[0])
	case 2:
		for _, mv := range root.Values {
			if mv.Key.String() != segs[0] {
				continue
			}
			sec, ok := mv.Value.(*ast.MappingNode)
			if !ok {
				break
			}
			sec.Values = removeKey(sec.Values, segs[1])
			if len(sec.Values) == 0 {
				root.Values = removeKey(root.Values, segs[0])
			}
			break
		}
	}
	return []byte(file.String()), norm, nil
}

// removeKey returns vals without the MappingValueNode whose key equals key.
func removeKey(vals []*ast.MappingValueNode, key string) []*ast.MappingValueNode {
	var kept []*ast.MappingValueNode
	for _, mv := range vals {
		if mv.Key.String() != key {
			kept = append(kept, mv)
		}
	}
	return kept
}

// validateConfigLayer confirms that one sparse layer has a current-or-missing
// version, the documented mapping shape, no unknown fields, and valid custom
// leaves. Cross-field validation deliberately waits until layers are merged.
func validateConfigLayer(b []byte) (map[string]any, error) {
	document, err := configDocument(b)
	if err != nil {
		return nil, err
	}
	if raw, present := document["version"]; present {
		var v versionOnly
		if err := yaml.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		if v.Value != CurrentVersion {
			return nil, fmt.Errorf("invalid version %v", raw)
		}
	}
	if err := validateConfigShape(document); err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.UnmarshalWithOptions(b, &c, yaml.DisallowUnknownField()); err != nil {
		return nil, err
	}
	return document, nil
}

// validateConfigShape rejects null collection values while allowing absent keys
// to inherit from lower layers.
func validateConfigShape(document map[string]any) error {
	for _, section := range []string{"keys", "style", "icl", "logs", "core"} {
		if value, present := document[section]; present {
			if _, ok := value.(map[string]any); !ok {
				return fmt.Errorf("%s must be a map", section)
			}
		}
	}
	iclValue, present := document["icl"]
	if !present {
		return nil
	}
	return validateICLShape(iclValue)
}

func validateICLShape(value any) error {
	icl, ok := value.(map[string]any)
	if !ok {
		return errors.New("icl must be a map")
	}
	instances, present := icl["instances"]
	if present {
		entries, ok := instances.([]any)
		if !ok || entries == nil {
			return errors.New("icl.instances must be a list; use [] for no instances")
		}
	}
	environments, present := icl["environments"]
	if !present {
		return nil
	}
	records, ok := environments.(map[string]any)
	if !ok || records == nil {
		return errors.New("icl.environments must be a map")
	}
	for cname, record := range records {
		if _, ok := record.(map[string]any); !ok {
			return fmt.Errorf("icl.environments.%q must be a record", cname)
		}
	}
	return nil
}

func validateConfigBytes(b []byte) error {
	_, err := configFromLayers(configLayer{name: "config", bytes: b})
	return err
}

// setValueError wraps a validation error, substituting a friendly hint when a
// scalar was supplied for a list-typed field.
func setValueError(leaf FieldMeta, parsed any, norm string, err error) error {
	if strings.HasPrefix(leaf.GoType, "[]") {
		if _, isSeq := parsed.([]any); !isSeq {
			return fmt.Errorf("%q expects a list, e.g. config set %s '[a, b]'",
				norm, strings.TrimPrefix(norm, "$."))
		}
	}
	return fmt.Errorf("invalid value for %q: %w", norm, err)
}

// SetConfig applies an AST edit to the sparse user file and validates the
// proposed document against public defaults and the optional Homebrew and
// system layers. A missing file is created with the current version header.
func SetConfig(yamlPath, value string) error {
	p, err := GetConfigPath()
	if err != nil {
		return err
	}
	src, err := os.ReadFile(p) // #nosec G304 -- xdg-resolved config path
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config: %w", err)
	}
	if os.IsNotExist(err) {
		src = nil
	}
	out, leaf, parsed, norm, err := setValueUnchecked(src, yamlPath, value)
	if err != nil {
		return err
	}
	packageBytes, err := readPackageConfig()
	if err != nil {
		return err
	}
	systemPath, err := getSystemConfigPath()
	if err != nil {
		return err
	}
	systemBytes, err := readOptionalConfigFile(systemPath, systemConfigName)
	if err != nil {
		return err
	}
	if _, err := configFromLayers(
		configLayer{name: "package defaults", bytes: packageBytes},
		configLayer{name: systemConfigName, bytes: systemBytes},
		configLayer{name: userConfigName, bytes: out},
	); err != nil {
		return setValueError(leaf, parsed, norm, err)
	}
	return writeConfigFile(p, out)
}

// UnsetConfig removes one user override and validates the proposed document
// against its Homebrew and system base. A missing user file is a no-op success.
func UnsetConfig(yamlPath string) error {
	p, err := GetConfigPath()
	if err != nil {
		return err
	}
	src, err := os.ReadFile(p) // #nosec G304 -- xdg-resolved config path
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read config: %w", err)
	}
	out, norm, err := unsetValueUnchecked(src, yamlPath)
	if err != nil {
		return err
	}
	packageBytes, err := readPackageConfig()
	if err != nil {
		return err
	}
	systemPath, err := getSystemConfigPath()
	if err != nil {
		return err
	}
	systemBytes, err := readOptionalConfigFile(systemPath, systemConfigName)
	if err != nil {
		return err
	}
	if _, err := configFromLayers(
		configLayer{name: "package defaults", bytes: packageBytes},
		configLayer{name: systemConfigName, bytes: systemBytes},
		configLayer{name: userConfigName, bytes: out},
	); err != nil {
		return fmt.Errorf("unset %q produced invalid config: %w", norm, err)
	}
	return writeConfigFile(p, out)
}

// writeConfigFile creates the config directory if needed and writes b with
// 0o600 permissions. An existing file is secured before its contents change:
// os.WriteFile's mode applies only to newly created files.
func writeConfigFile(p string, b []byte) error {
	if err := os.MkdirAll(path.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.Chmod(p, 0o600); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("secure config before write: %w", err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil { //nolint:gosec // p from GetConfigPath
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		return fmt.Errorf("secure config after write: %w", err)
	}
	return nil
}
