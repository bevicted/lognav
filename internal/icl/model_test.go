package icl

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatICLTime_RendersMillisUTC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input time.Time
		want  string
	}{
		{
			name:  "utc input keeps millis and Z",
			input: time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC),
			want:  "2026-06-04T12:00:00.000Z",
		},
		{
			name:  "offset input converted to utc",
			input: time.Date(2026, 6, 4, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60)),
			want:  "2026-06-04T10:00:00.000Z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, formatICLTime(tt.input))
		})
	}
}

func TestDeref_NilReturnsEmpty(t *testing.T) {
	t.Parallel()
	assert.Empty(t, deref(nil))
	s := "x"
	assert.Equal(t, "x", deref(&s))
}
