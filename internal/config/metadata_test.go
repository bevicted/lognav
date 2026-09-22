package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllFieldsHaveDescTag(t *testing.T) {
	checkStructDescTags(t, reflect.TypeFor[Config](), "Config")
}

func checkStructDescTags(t *testing.T, typ reflect.Type, path string) {
	t.Helper()
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for i := range typ.NumField() {
		field := typ.Field(i)
		fieldPath := path + "." + field.Name

		yamlTag := field.Tag.Get("yaml")
		if strings.TrimSpace(yamlTag) == "-" {
			continue
		}

		desc := field.Tag.Get("desc")
		if desc == "" {
			t.Errorf("field %s has no desc tag", fieldPath)
		}

		ft := field.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && !isLeafType(field.Type) {
			checkStructDescTags(t, ft, fieldPath)
		}
	}
}

func TestYAMLPathsAreCorrect(t *testing.T) {
	metas := GetFieldMetadata(New(), "$")
	sectionKeys := map[string]bool{}
	for _, m := range metas {
		sectionKeys[m.YAMLKey] = true
	}
	for _, expected := range []string{"keys", "style", "icl", "logs", "core"} {
		if !sectionKeys[expected] {
			t.Errorf("expected section %q not found in root metadata", expected)
		}
	}
}

func TestYAMLDashFieldsExcluded(t *testing.T) {
	metas := GetFieldMetadata(New(), "$")
	keysMeta := findNode(metas, "$.keys")
	if keysMeta == nil {
		t.Fatal("keys section not found")
	}
	for _, child := range keysMeta.Children {
		if child.YAMLKey == "forceQuit" {
			t.Error("ForceQuit (yaml:\"-\") should be excluded from metadata")
		}
	}

	iclMeta := findNode(metas, "$.icl")
	if iclMeta == nil {
		t.Fatal("icl section not found")
	}
	instances := findNode(metas, "$.icl.instances")
	require.NotNil(t, instances)
	assert.False(t, instances.Computed)
	assert.False(t, instances.ReadOnly)
}

func TestFirstFetchKeyMetadata(t *testing.T) {
	t.Parallel()

	node := findNode(GetFieldMetadata(New(), "$.keys"), "$.keys.firstFetch")
	require.NotNil(t, node)
	assert.Equal(t, "race enabled instances until the first logs arrive", node.Description)
	assert.Equal(t, KeyBind{"shift+f"}, node.Default)
}

func TestWritableInstancesMetadata(t *testing.T) {
	t.Parallel()
	metas := GetFieldMetadata(New(), "$.icl.instances")
	require.Len(t, metas, 1)
	assert.False(t, metas[0].Computed)
	assert.False(t, metas[0].ReadOnly)
	assert.Equal(t, "[]ICLInstanceConfig", metas[0].GoType)
	assert.NotNil(t, metas[0].Default)
}

func TestGetFieldMetadataCore(t *testing.T) {
	metas := GetFieldMetadata(New(), "$.core")
	if len(metas) == 0 {
		t.Fatal("expected core fields, got none")
	}

	fieldKeys := map[string]bool{}
	for _, m := range metas {
		fieldKeys[m.YAMLKey] = true
	}

	expected := []string{"showKeyHints", "extraSnippets", "includeDefaultSnippets", "includeSnippetsInEditor", "maxAutoSnapshots", "maxLogFiles", "enableMouse", "enableHover", "openBrowser", "doubleClickMs", "scrollAxisLockMs", "wheelScrollLines", "redrawIntervalMs", "saveSnapshotOnFetchDone", "notifyOnFetchDone", "notifyStyle", "watchCooldownSeconds", "watchMaxFetches", "watchMaxDurationSeconds", "enableExperimental"}
	for _, key := range expected {
		if !fieldKeys[key] {
			t.Errorf("expected core field %q not found", key)
		}
	}
	if len(metas) != len(expected) {
		t.Errorf("expected %d core fields, got %d", len(expected), len(metas))
	}
}

func TestNewConfig_EnableHoverDefaultsOn(t *testing.T) {
	t.Parallel()
	if !newConfig().Core.EnableHover {
		t.Error("EnableHover should default to true (hover is a first-class feature)")
	}
}

func TestDefaultsMatchNewConfig(t *testing.T) {
	defaults := newConfig()
	metas := GetFieldMetadata(New(), "$.core")
	for _, m := range metas {
		switch m.YAMLKey {
		case "showKeyHints":
			if m.Default != defaults.Core.ShowKeyHints {
				t.Errorf("showKeyHints default mismatch: got %v, want %v", m.Default, defaults.Core.ShowKeyHints)
			}
		case "enableMouse":
			if m.Default != defaults.Core.EnableMouse {
				t.Errorf("enableMouse default mismatch: got %v, want %v", m.Default, defaults.Core.EnableMouse)
			}
		case "maxAutoSnapshots":
			if m.Default != defaults.Core.MaxAutoSnapshots {
				t.Errorf("maxAutoSnapshots default mismatch: got %v, want %v", m.Default, defaults.Core.MaxAutoSnapshots)
			}
		case "includeSnippetsInEditor":
			if m.Default != defaults.Core.IncludeSnippetsInEditor {
				t.Errorf("includeSnippetsInEditor default mismatch: got %v, want %v", m.Default, defaults.Core.IncludeSnippetsInEditor)
			}
		}
	}
}

func TestFieldMetadataJSON(t *testing.T) {
	result, err := FieldMetadataJSON(New(), "$.core")
	if err != nil {
		t.Fatal(err)
	}

	var metas []FieldMeta
	if err := json.Unmarshal([]byte(result), &metas); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	if len(metas) == 0 {
		t.Error("expected non-empty JSON output")
	}
}

func TestFieldMetadataJSONAllSections(t *testing.T) {
	result, err := FieldMetadataJSON(New(), "$")
	if err != nil {
		t.Fatal(err)
	}

	var metas []FieldMeta
	if err := json.Unmarshal([]byte(result), &metas); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	if len(metas) == 0 {
		t.Error("expected non-empty JSON output for all sections")
	}
}

func TestDescribeConfigSingleField(t *testing.T) {
	t.Parallel()

	output := DescribeConfig(New(), "$.core.enableMouse")
	if output == "" {
		t.Fatal("expected non-empty output for single field")
	}
	if !strings.Contains(output, "enableMouse") {
		t.Error("output should contain field name")
	}
	if !strings.Contains(output, "enable mouse support") {
		t.Error("output should contain description")
	}
}

func TestRetiredStatusRowFieldsAreAbsentFromReadMetadata(t *testing.T) {
	t.Parallel()

	style, err := GetConfig(New(), "$.style")
	require.NoError(t, err)
	for _, key := range []string{"status" + "lineBg", "status" + "lineFg", "status" + "lineTimestampFormat"} {
		path := "$.style." + key
		assert.Nilf(t, findNode(GetFieldMetadata(New(), "$.style"), path), "%s must not have metadata", path)
		assert.Emptyf(t, DescribeConfig(New(), path), "%s must not be described", path)
		assert.NotContainsf(t, style, key, "%s must not be returned", path)
	}
}

func TestShowKeyHintsMetadataDescribesContextualRow(t *testing.T) {
	t.Parallel()

	node := findNode(GetFieldMetadata(New(), "$.core"), "$.core.showKeyHints")
	require.NotNil(t, node)
	assert.Equal(t, "show the clickable key-hint row below an optional contextual row; false returns the key-hint row to tab content", node.Description)
}

func TestNotifyStyleMetadataDoesNotClaimGhosttyOSC99Support(t *testing.T) {
	t.Parallel()

	node := findNode(GetFieldMetadata(New(), "$.core"), "$.core.notifyStyle")
	require.NotNil(t, node)
	styles := strings.Split(node.Description, " | ")
	require.Len(t, styles, 4)
	assert.Contains(t, styles[3], "kitty")
	assert.Contains(t, styles[3], "foot")
	assert.NotContains(t, styles[3], "Ghostty")
}

func TestRedactsAPIKeys(t *testing.T) {
	t.Parallel()
	cfg := New()
	// #nosec G101 -- synthetic credentials exercise configuration redaction.
	cfg.ICL.Environments = map[string]ICLEnvironmentConfig{
		"bluemix": {IAMURL: "https://iam.example/identity", APIKey: "prod-secret", APIKeyOpRef: "op://vault/prod"},
		"test":    {IAMURL: "https://iam.example/test", APIKey: "test-secret", APIKeyOpRef: "op://vault/test"},
	}

	metas := GetFieldMetadata(cfg, "$.icl")
	environments := findNode(metas, "$.icl.environments")
	require.NotNil(t, environments)
	records, ok := environments.Current.(map[string]ICLEnvironmentConfig)
	require.True(t, ok)
	bluemix, ok := records["bluemix"]
	require.True(t, ok)
	assert.Equal(t, "redacted", bluemix.APIKey)
	assert.Equal(t, "op://vault/prod", bluemix.APIKeyOpRef)

	yml, err := GetConfig(cfg, "$.icl")
	require.NoError(t, err)
	assert.NotContains(t, yml, "prod-secret")
	assert.NotContains(t, yml, "test-secret")
	assert.Contains(t, yml, "op://vault/prod")
	assert.Equal(t, "prod-secret", cfg.ICL.Environments["bluemix"].APIKey)
}

func TestDescribeConfigSection(t *testing.T) {
	output := DescribeConfig(New(), "$.core")
	if output == "" {
		t.Fatal("expected non-empty output for section")
	}
	if !strings.Contains(output, "general application behavior") {
		t.Error("output should contain section description")
	}
}
