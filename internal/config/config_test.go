package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const enableMouseFalse = "enableMouse: false"

func TestConfigurableLeafPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		path    string
		wantOK  bool
		wantTyp string
	}{
		{"scalar_leaf", "$.core.enableMouse", true, "bool"},
		{"leading_dot_form", ".core.redrawIntervalMs", true, "uint16"},
		{"keybind_leaf", "$.keys.accept", true, "[]string"},
		{"extra_instances_leaf", "$.icl.instances", true, "[]ICLInstanceConfig"},
		{"environments_map_leaf", "$.icl.environments", true, "map[string]ICLEnvironmentConfig"},
		{"version_leaf", "$.version", true, "int"},
		{"section_not_leaf", "$.core", false, ""},
		{"root_not_leaf", "$", false, ""},
		{"unknown_key", "$.core.nope", false, ""},
		{"yaml_dash_field", "$.icl.defaultInstances", false, ""},
		{"index_path", "$.icl.instances[0].crn", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			leaf, ok := configurableLeafPath(tt.path)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantTyp, leaf.GoType)
			}
		})
	}
}

func TestSetValue(t *testing.T) {
	t.Parallel()
	base := "version: 1\ncore:\n  enableMouse: true\n  # keep me\n  redrawIntervalMs: 33\n"

	tests := []struct {
		name       string
		src        string
		path       string
		value      string
		wantErr    bool
		wantSubstr []string // all must appear in output
		wantAbsent []string // none may appear in output
	}{
		{
			name: "replace_existing_scalar_keeps_comment",
			src:  base, path: "core.redrawIntervalMs", value: "50",
			wantSubstr: []string{"redrawIntervalMs: 50", "# keep me", "enableMouse: true"},
		},
		{
			name: "create_field_in_existing_section",
			src:  base, path: "core.maxLogFiles", value: "7",
			wantSubstr: []string{"maxLogFiles: 7", "enableMouse: true", "# keep me"},
		},
		{
			name: "create_missing_section",
			src:  base, path: "logs.scrolloff", value: "9",
			wantSubstr: []string{"logs:", "scrolloff: 9", "enableMouse: true"},
		},
		{
			name: "create_keybind_section_from_empty_mapping",
			src:  "{}\n", path: "keys.accept", value: "[alt+x]",
			wantSubstr: []string{"keys:", "accept:", "- alt+x"},
		},
		{
			name: "create_style_section_from_empty_mapping",
			src:  "{}\n", path: "style.errorColor", value: "green",
			wantSubstr: []string{"style:", "errorColor: green"},
		},
		{
			name: "bool_value",
			src:  base, path: "core.enableMouse", value: "false",
			wantSubstr: []string{enableMouseFalse},
		},
		{
			name: "color_name",
			src:  base, path: "style.errorColor", value: "blue",
			wantSubstr: []string{"errorColor: blue"},
		},
		{
			name: "color_hex_is_not_a_comment",
			src:  base, path: "style.errorColor", value: "#ff5555",
			wantSubstr: []string{"ff5555"}, wantAbsent: []string{"errorColor: null", "errorColor:\n"},
		},
		{
			name: "notify_style_enum",
			src:  base, path: "core.notifyStyle", value: "osc777",
			wantSubstr: []string{"notifyStyle: osc777"},
		},
		{
			name: "whole_keybind_list",
			src:  base, path: "keys.accept", value: "[enter, ctrl+y]",
			wantSubstr: []string{"accept:", "- enter", "- ctrl+y"},
		},
		{
			name: "whole_extra_color_rules_list",
			src:  base, path: "logs.extraColorRules", value: "[{match: ERROR, fg: red}]",
			wantSubstr: []string{"extraColorRules:", "match: ERROR", "fg: red"},
		},
		{
			name: "whole_extra_instances_list",
			src:  base, path: "icl.instances",
			value:      "[{name: foo, crn: 'crn:v1:bluemix:public:logs:us-south:a/foo:foo::'}]",
			wantSubstr: []string{"instances:", "name: foo"},
		},
		{
			name: "whole_environments_map",
			src:  base, path: "icl.environments",
			value:      "{bluemix: {iamURL: https://iam.example/identity}, test-cloud: {iamURL: https://iam.example/test}}",
			wantSubstr: []string{"environments:", "bluemix:", "test-cloud:", "iamURL: https://iam.example/identity"},
		},
		{
			name:    "extra_instance_missing_crn",
			src:     base,
			path:    "icl.instances",
			value:   "[{name: missing}]",
			wantErr: true,
		},
		{
			name: "string_with_hash",
			src:  base, path: "style.elapsedFetchTimeFormat", value: "#> ",
			wantSubstr: []string{"elapsedFetchTimeFormat:"}, wantAbsent: []string{"elapsedFetchTimeFormat: null"},
		},
		{
			name: "new_file_seeds_version",
			src:  "", path: "core.enableMouse", value: "false",
			wantSubstr: []string{"version:", enableMouseFalse},
		},
		{name: "invalid_color", src: base, path: "style.errorColor", value: "notacolor", wantErr: true},
		{name: "int_overflow_uint8", src: base, path: "core.maxLogFiles", value: "9999", wantErr: true},
		{name: "unknown_path", src: base, path: "core.nope", value: "1", wantErr: true},
		{name: "retired_status_row_background", src: base, path: "style.status" + "lineBg", value: "blue", wantErr: true},
		{name: "retired_status_row_foreground", src: base, path: "style.status" + "lineFg", value: "blue", wantErr: true},
		{name: "retired_status_row_timestamp", src: base, path: "style.status" + "lineTimestampFormat", value: "2006", wantErr: true},
		{name: "yaml_dash_path", src: base, path: "icl.defaultInstances", value: "[]", wantErr: true},
		{name: "scalar_for_list_hint", src: base, path: "keys.accept", value: "enter", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := SetValue([]byte(tt.src), tt.path, tt.value)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			got := string(out)
			for _, s := range tt.wantSubstr {
				assert.Contains(t, got, s)
			}
			for _, s := range tt.wantAbsent {
				assert.NotContains(t, got, s)
			}
			// Output must round-trip into a valid Config.
			var c Config
			require.NoError(t, yaml.UnmarshalWithOptions(out, &c, yaml.DisallowUnknownField()))
		})
	}
}

func TestSetConfig_RejectsUnsupportedVersion(t *testing.T) {
	// Cannot parallelize: shares XDG_CONFIG_HOME env var.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	p, err := GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))

	for _, tt := range []struct {
		name  string
		src   string
		path  string
		value string
	}{
		{
			name:  "target",
			src:   "version: 1\ncore:\n  enableMouse: true\n",
			path:  "version",
			value: "2",
		},
		{
			name:  "preserved_sibling",
			src:   "version: 2\ncore:\n  enableMouse: true\n",
			path:  "core.enableMouse",
			value: "false",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(p, []byte(tt.src), 0o600))

			err := SetConfig(tt.path, tt.value)
			require.ErrorContains(t, err, "invalid version 2")

			got, err := os.ReadFile(p) //nolint:gosec // test temp path
			require.NoError(t, err)
			assert.Equal(t, tt.src, string(got), "invalid final config must not be written")
		})
	}
}

func TestWriteConfigFile_SecuresExistingFile(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "user.yaml")
	require.NoError(t, os.WriteFile(p, []byte("version: 1\n"), 0o644)) //nolint:gosec // verify remediation of an insecure existing mode.
	require.NoError(t, os.Chmod(p, 0o644))                             //nolint:gosec // umask may have already restricted it.

	require.NoError(t, writeConfigFile(p, []byte("version: 1\ncore:\n  enableMouse: false\n")))
	info, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestSetValue_ListHintMessage(t *testing.T) {
	t.Parallel()
	_, err := SetValue([]byte("version: 1\n"), "keys.accept", "enter")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expects a list")
}

func TestUnsetValue(t *testing.T) {
	t.Parallel()
	base := "version: 1\ncore:\n  enableMouse: false\n  redrawIntervalMs: 50\nlogs:\n  scrolloff: 9\n"

	tests := []struct {
		name       string
		src        string
		path       string
		wantErr    bool
		wantSubstr []string
		wantAbsent []string
	}{
		{
			name: "remove_one_field_keeps_siblings",
			src:  base, path: "core.redrawIntervalMs",
			wantSubstr: []string{enableMouseFalse, "scrolloff: 9"},
			wantAbsent: []string{"redrawIntervalMs"},
		},
		{
			name: "remove_last_field_drops_empty_section",
			src:  base, path: "logs.scrolloff",
			wantSubstr: []string{"core:", enableMouseFalse},
			wantAbsent: []string{"logs:", "scrolloff"},
		},
		{
			name: "absent_key_is_noop",
			src:  base, path: "core.maxLogFiles",
			wantSubstr: []string{enableMouseFalse, "redrawIntervalMs: 50"},
		},
		{
			name: "remove_top_level",
			src:  base, path: "version",
			wantAbsent: []string{"version:"},
		},
		{name: "unknown_path_errors", src: base, path: "core.nope", wantErr: true},
		{name: "retired_status_row_background_errors", src: base, path: "style.status" + "lineBg", wantErr: true},
		{name: "retired_status_row_foreground_errors", src: base, path: "style.status" + "lineFg", wantErr: true},
		{name: "retired_status_row_timestamp_errors", src: base, path: "style.status" + "lineTimestampFormat", wantErr: true},
		{name: "section_path_errors", src: base, path: "core", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := UnsetValue([]byte(tt.src), tt.path)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			got := string(out)
			for _, s := range tt.wantSubstr {
				assert.Contains(t, got, s)
			}
			for _, s := range tt.wantAbsent {
				assert.NotContains(t, got, s)
			}
			var c Config
			require.NoError(t, yaml.UnmarshalWithOptions(out, &c, yaml.DisallowUnknownField()))
		})
	}
}

func TestUnsetValue_EmptySrcIsNoop(t *testing.T) {
	t.Parallel()
	out, err := UnsetValue([]byte(""), "core.enableMouse")
	require.NoError(t, err)
	assert.Empty(t, string(out))
}

// Disk round-trip: t.Setenv makes this non-parallel (env is process-global).
//
//nolint:paralleltest // t.Setenv modifies process-global env
func TestSetConfig_UnsetConfig_DiskRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// set on a missing file creates it
	require.NoError(t, SetConfig("core.enableMouse", "false"))

	p, err := GetConfigPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "lognav", "user.yaml"), p)
	b, err := os.ReadFile(p) //nolint:gosec // test temp path
	require.NoError(t, err)
	assert.Contains(t, string(b), "enableMouse: false")
	assert.Contains(t, string(b), "version:")
	info, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// set a second field
	require.NoError(t, SetConfig("logs.scrolloff", "9"))
	b, _ = os.ReadFile(p) //nolint:gosec // test temp path
	assert.Contains(t, string(b), "scrolloff: 9")

	// unset reverts the field
	require.NoError(t, UnsetConfig("core.enableMouse"))
	b, _ = os.ReadFile(p) //nolint:gosec // test temp path
	assert.NotContains(t, string(b), "enableMouse")

	// invalid value does not write
	require.Error(t, SetConfig("style.errorColor", "notacolor"))

	// unset on a missing file is a no-op success
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	require.NoError(t, UnsetConfig("core.enableMouse"))
}

func TestConfig_RenderCacheSize_FallbackTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		yamlOverride string
		wantSize     int
		wantErr      bool
	}{
		{"unset_uses_default", "", 128, false},
		// Config layer stores 0 as-is; the <=0 fallback to 1024 happens in logviewer.New.
		{"explicit_zero_stored_as_zero", "logs:\n  renderCacheSize: 0\n", 0, false},
		{"negative_honored_as_stored", "logs:\n  renderCacheSize: -5\n", -5, false},
		{"positive_honored", "logs:\n  renderCacheSize: 256\n", 256, false},
		{"max_int_honored", "logs:\n  renderCacheSize: 9223372036854775807\n", math.MaxInt, false},
		// go-yaml silently truncates floats to int (no error); 1024.5 → 1024.
		{"yaml_float_truncated_silently", "logs:\n  renderCacheSize: 1024.5\n", 1024, false},
		{"yaml_string_decode_error", "logs:\n  renderCacheSize: \"1k\"\n", 0, true},
		{"yaml_list_wrong_type", "logs:\n  renderCacheSize: [1, 2]\n", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := newConfig()
			if tt.yamlOverride != "" {
				err := yaml.UnmarshalWithOptions([]byte(tt.yamlOverride), cfg, yaml.DisallowUnknownField())
				if tt.wantErr {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantSize, cfg.Logs.RenderCacheSize)
		})
	}
}

func TestNewConfig_LogScalingDefaults(t *testing.T) {
	t.Parallel()
	c := New()
	require.Equal(t, uint32(0), c.Logs.MaxRows)
	require.Equal(t, uint16(1), c.Logs.ResidentInstances)
	require.Equal(t, 1, c.Logs.EffectiveResidentInstances())

	require.Equal(t, 1, (Logs{ResidentInstances: 0}).EffectiveResidentInstances(), "zero floors to 1")
	require.Equal(t, 5, (Logs{ResidentInstances: 5}).EffectiveResidentInstances(), "non-zero pass-through")
}

// Experimental features (currently the archive tab + fetch mode) ship OFF by
// default: a fresh config must have Core.EnableExperimental == false, so they are
// hidden unless the user opts in.
func TestNewConfig_ExperimentalDefaults(t *testing.T) {
	t.Parallel()
	c := New()
	require.False(t, c.Core.EnableExperimental, "experimental features must default to disabled (opt-in)")
}
