package config

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/bevicted/lognav/internal/jsonutil"
)

// FieldMeta describes a single config field or section.
type FieldMeta struct {
	YAMLPath    string      `json:"path"`
	YAMLKey     string      `json:"key"`
	Description string      `json:"description"`
	GoType      string      `json:"type"`
	Default     any         `json:"default,omitempty"`
	Current     any         `json:"current,omitempty"`
	Computed    bool        `json:"computed,omitempty"`
	ReadOnly    bool        `json:"readOnly,omitempty"`
	Children    []FieldMeta `json:"children,omitempty"`
}

var leafTypes = map[reflect.Type]bool{
	reflect.TypeFor[KeyBind](): true,
	reflect.TypeFor[Color]():   true,
	reflect.TypeFor[*CRN]():    true,
}

func isLeafType(t reflect.Type) bool {
	if leafTypes[t] {
		return true
	}
	if t.Kind() == reflect.Pointer && leafTypes[t.Elem()] {
		return true
	}
	// Slice of struct (e.g. []ICLInstanceConfig)
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Struct {
		return true
	}
	return false
}

//nolint:gocyclo // reflection-driven field walker; branches are kind switches, splitting them adds indirection.
func extractFieldMeta(t reflect.Type, defaultVal, currentVal reflect.Value, prefix string) []FieldMeta {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
		if defaultVal.IsValid() && !defaultVal.IsNil() {
			defaultVal = defaultVal.Elem()
		}
		if currentVal.IsValid() && !currentVal.IsNil() {
			currentVal = currentVal.Elem()
		}
	}

	var metas []FieldMeta
	for i := range t.NumField() {
		field := t.Field(i)
		yamlTag := field.Tag.Get("yaml")
		desc := field.Tag.Get("desc")

		yamlKey := yamlTag
		if idx := strings.Index(yamlKey, ","); idx != -1 {
			yamlKey = yamlKey[:idx]
		}
		yamlKey = strings.TrimSpace(yamlKey)

		if yamlKey == "-" {
			continue
		}

		path := prefix + "." + yamlKey

		var defVal, curVal reflect.Value
		if defaultVal.IsValid() {
			defVal = defaultVal.Field(i)
		}
		if currentVal.IsValid() {
			curVal = currentVal.Field(i)
		}

		fm := FieldMeta{
			YAMLPath:    path,
			YAMLKey:     yamlKey,
			Description: desc,
			GoType:      friendlyType(field.Type),
		}

		ft := field.Type
		if !isLeafType(ft) && (ft.Kind() == reflect.Struct || (ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.Struct)) {
			fm.Children = extractFieldMeta(ft, defVal, curVal, path)
		} else {
			if defVal.IsValid() {
				fm.Default = defVal.Interface()
			}
			if curVal.IsValid() {
				fm.Current = curVal.Interface()
			}
		}

		metas = append(metas, fm)
	}
	return metas
}

func friendlyType(t reflect.Type) string {
	if t == reflect.TypeFor[KeyBind]() {
		return "[]string"
	}
	if t == reflect.TypeFor[Color]() {
		return "color"
	}
	if t.Kind() == reflect.Pointer {
		return "*" + friendlyType(t.Elem())
	}
	if t.Kind() == reflect.Slice {
		return "[]" + friendlyType(t.Elem())
	}
	if t.Kind() == reflect.Map {
		return "map[" + friendlyType(t.Key()) + "]" + friendlyType(t.Elem())
	}
	return t.Name()
}

// extractWith walks the Config type once, taking Default values from defaults
// and Current values from cfg. Callers that already hold a defaults config pass
// it in rather than making extractAll build a second one.
func extractWith(defaults, cfg *Config) []FieldMeta {
	t := reflect.TypeFor[Config]()
	return extractFieldMeta(t, reflect.ValueOf(*defaults), reflect.ValueOf(*cfg), "$")
}

func extractAll(cfg *Config) []FieldMeta {
	return extractWith(newConfig(), cfg)
}

func extractRedacted(cfg *Config) []FieldMeta {
	redacted := redactedConfig(cfg)
	return extractAll(&redacted)
}

// GetFieldMetadata returns metadata for all config fields under the given YAML path prefix.
// Use "$" or "" for the root.
func GetFieldMetadata(cfg *Config, yamlPrefix string) []FieldMeta {
	all := extractRedacted(cfg)

	if yamlPrefix == "" || yamlPrefix == "$" {
		return all
	}

	node := findNode(all, yamlPrefix)
	if node == nil {
		return nil
	}
	if len(node.Children) > 0 {
		return node.Children
	}
	return []FieldMeta{*node}
}

// DescribeConfig returns a human-readable description of config fields under the given YAML path prefix.
func DescribeConfig(cfg *Config, yamlPrefix string) string {
	all := extractRedacted(cfg)

	var sb strings.Builder
	if yamlPrefix == "" || yamlPrefix == "$" {
		for _, m := range all {
			writeMeta(&sb, m, "")
		}
		return sb.String()
	}

	node := findNode(all, yamlPrefix)
	if node == nil {
		return ""
	}
	writeMeta(&sb, *node, "")
	return sb.String()
}

func findNode(metas []FieldMeta, path string) *FieldMeta {
	for i, m := range metas {
		if m.YAMLPath == path {
			return &metas[i]
		}
		if strings.HasPrefix(path, m.YAMLPath+".") && len(m.Children) > 0 {
			return findNode(m.Children, path)
		}
	}
	return nil
}

func writeMeta(sb *strings.Builder, m FieldMeta, indent string) {
	if len(m.Children) > 0 {
		writeSection(sb, m, indent)
	} else {
		writeField(sb, m, indent)
	}
}

func writeSection(sb *strings.Builder, m FieldMeta, indent string) {
	fmt.Fprintf(sb, "%s: %s\n", fieldPath(m), m.Description)
	for _, child := range m.Children {
		writeMeta(sb, child, indent+"  ")
	}
}

func writeField(sb *strings.Builder, m FieldMeta, indent string) {
	attributes := m.GoType
	if m.Computed && m.ReadOnly {
		attributes += ", computed, read-only"
	}
	fmt.Fprintf(sb, "%s (%s) = %v\n", fieldPath(m), attributes, formatValue(m.Current))
	fmt.Fprintf(sb, "%s  %s\n", indent, m.Description)
	fmt.Fprintf(sb, "%s  default: %v\n", indent, formatValue(m.Default))
}

// fieldPath returns the field's YAML path without the leading "$." root marker,
// e.g. "$.core.enableMouse" -> "core.enableMouse".
func fieldPath(m FieldMeta) string {
	return strings.TrimPrefix(m.YAMLPath, "$.")
}

func formatValue(v any) string {
	if v == nil {
		return "<nil>"
	}
	switch val := v.(type) {
	case string:
		return fmt.Sprintf("%q", val)
	case KeyBind:
		return formatValue([]string(val))
	case []string:
		if len(val) == 0 {
			return "[]"
		}
		parts := make([]string, len(val))
		for i, s := range val {
			parts[i] = fmt.Sprintf("%q", s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case Color:
		return val.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// FieldMetadataJSON returns a JSON representation of config field metadata.
// Section structs are flattened; only leaf fields are included. Values use
// their YAML configuration representation, including yaml-tagged object keys.
func FieldMetadataJSON(cfg *Config, yamlPrefix string) (string, error) {
	metas := GetFieldMetadata(cfg, yamlPrefix)
	var flat []FieldMeta
	flattenMetas(metas, &flat)

	defaults := newConfig()
	for i := range flat {
		defaultValue, err := GetConfigValue(defaults, flat[i].YAMLPath)
		if err != nil {
			return "", err
		}
		currentValue, err := GetConfigValue(cfg, flat[i].YAMLPath)
		if err != nil {
			return "", err
		}
		flat[i].Default = defaultValue
		flat[i].Current = currentValue
	}

	b, err := jsonutil.API.MarshalIndent(flat, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func flattenMetas(metas []FieldMeta, out *[]FieldMeta) {
	for _, m := range metas {
		if len(m.Children) > 0 {
			flattenMetas(m.Children, out)
		} else {
			*out = append(*out, m)
		}
	}
}
