package querytemplate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
)

func TestResolve(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2025, time.January, 2, 3, 4, 5, 987_000_000, time.FixedZone("UTC+05:30", 5*60*60+30*60))
	cfg := config.New()
	cfg.ICL.DefaultQuery = `{{ date }}|{{ date -1 }}|{{ date 2 }}|{{ timestamp }}|{{ timestamp "-90m" }}|{{ timestamp }}`
	cfg.Core.ExtraSnippets = []config.Snippet{{Snippet: `literal {{"{{"}}date {{"}}"}} {{ date }}`, Desc: "unchanged"}}
	cfg.Core.DefaultSnippets = []config.Snippet{{Snippet: `{{ timestamp "+1h" }}`, Desc: "built-in"}}
	cfg.Core.IncludeDefaultSnippets = true

	content, err := Resolve(cfg, fixed)
	require.NoError(t, err)
	assert.Equal(t, "2025-01-02|2025-01-01|2025-01-04|2025-01-02T03:04:05+05:30|2025-01-02T01:34:05+05:30|2025-01-02T03:04:05+05:30", content.DefaultQuery)
	assert.Equal(t, []config.Snippet{
		{Snippet: "literal {{date }} 2025-01-02", Desc: "unchanged"},
		{Snippet: "2025-01-02T04:04:05+05:30", Desc: "built-in"},
	}, content.Snippets)
	assert.Equal(t, `{{ date }}|{{ date -1 }}|{{ date 2 }}|{{ timestamp }}|{{ timestamp "-90m" }}|{{ timestamp }}`, cfg.ICL.DefaultQuery, "configured source must remain unchanged")
	assert.Equal(t, `literal {{"{{"}}date {{"}}"}} {{ date }}`, cfg.Core.ExtraSnippets[0].Snippet, "configured snippets must remain unchanged")
}

func TestResolve_DateUsesCalendarDaysAcrossDST(t *testing.T) {
	t.Parallel()
	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	cfg := config.New()
	cfg.ICL.DefaultQuery = "{{ date -1 }}"

	content, err := Resolve(cfg, time.Date(2024, time.March, 11, 0, 30, 0, 0, location))
	require.NoError(t, err)
	assert.Equal(t, "2024-03-10", content.DefaultQuery, "AddDate must not subtract an elapsed 24 hours across DST")
}

func TestResolve_UTCUsesZ(t *testing.T) {
	t.Parallel()
	cfg := config.New()
	cfg.ICL.DefaultQuery = "{{ timestamp }}"

	content, err := Resolve(cfg, time.Date(2025, time.January, 2, 3, 4, 5, 999_000_000, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, "2025-01-02T03:04:05Z", content.DefaultQuery)
}

func TestResolve_ErrorsIdentifySource(t *testing.T) {
	t.Parallel()
	at := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, tt := range []struct {
		name      string
		configure func(*config.Config)
		wantErr   string
	}{
		{name: "default syntax", configure: func(cfg *config.Config) { cfg.ICL.DefaultQuery = "{{ date" }, wantErr: "icl.defaultQuery: parse template"},
		{name: "default excess date arguments", configure: func(cfg *config.Config) { cfg.ICL.DefaultQuery = "{{ date -1 1 }}" }, wantErr: "icl.defaultQuery: execute template: template: icl.defaultQuery"},
		{name: "default invalid date type", configure: func(cfg *config.Config) { cfg.ICL.DefaultQuery = `{{ date "-1" }}` }, wantErr: "date offset must be a signed integer"},
		{name: "default invalid timestamp duration", configure: func(cfg *config.Config) { cfg.ICL.DefaultQuery = `{{ timestamp "tomorrow" }}` }, wantErr: `parse timestamp duration "tomorrow"`},
		{name: "default excess timestamp arguments", configure: func(cfg *config.Config) { cfg.ICL.DefaultQuery = `{{ timestamp "1h" "2h" }}` }, wantErr: "timestamp accepts zero or one Go duration string"},
		{name: "extra snippet", configure: func(cfg *config.Config) { cfg.Core.ExtraSnippets = []config.Snippet{{Snippet: "{{ date 1 2 }}"}} }, wantErr: "core.extraSnippets[0].snippet: execute template"},
		{name: "default snippet", configure: func(cfg *config.Config) { cfg.Core.DefaultSnippets = []config.Snippet{{Snippet: "{{ timestamp 1 }}"}} }, wantErr: "core.defaultSnippets[0].snippet: execute template"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.New()
			cfg.ICL.DefaultQuery = "literal"
			cfg.Core.DefaultSnippets = nil
			tt.configure(cfg)

			_, err := Resolve(cfg, at)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestResolve_ExcludesDisabledBuiltInSnippets(t *testing.T) {
	t.Parallel()
	cfg := config.New()
	cfg.ICL.DefaultQuery = "literal"
	cfg.Core.ExtraSnippets = []config.Snippet{{Snippet: "extra", Desc: "extra"}}
	cfg.Core.DefaultSnippets = []config.Snippet{{Snippet: "{{ date", Desc: "disabled"}}
	cfg.Core.IncludeDefaultSnippets = false

	content, err := Resolve(cfg, time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, []config.Snippet{{Snippet: "extra", Desc: "extra"}}, content.Snippets)
	assert.NotContains(t, content.Snippets[0].Snippet, "{{")
}
