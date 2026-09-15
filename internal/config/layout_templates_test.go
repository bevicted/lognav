package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewConfig_QueryTimeTemplateDefaults(t *testing.T) {
	t.Parallel()
	cfg := New()

	assert.Equal(t, `source logs between @'{{ date -1 }}' and @'now'`, cfg.ICL.DefaultQuery[:len(`source logs between @'{{ date -1 }}' and @'now'`)])
	assert.NotContains(t, cfg.ICL.DefaultQuery, "subsystemname")
	assert.Contains(t, cfg.ICL.DefaultQuery, "| orderby $m.timestamp asc")
	assert.Contains(t, cfg.Core.DefaultSnippets, Snippet{Snippet: "between @'{{ date -1 }}' and @'{{ date }}'", Desc: "timeframe: between two timestamps"})
	assert.Contains(t, cfg.Core.DefaultSnippets, Snippet{Snippet: "around @'{{ date }}' interval 30m", Desc: "timeframe: around timestamp with interval"})
	assert.Contains(t, cfg.Core.DefaultSnippets, Snippet{Snippet: "@'{{ timestamp }}'", Desc: "timestamp format (ISO 8601)"})
}
