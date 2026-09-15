package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnframeLoginPasscode(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain", input: "one-time-code", want: "one-time-code"},
		{name: "complete bracketed paste", input: "\x1b[200~one-time-code\x1b[201~", want: "one-time-code"},
		{name: "incomplete prefix is preserved", input: "\x1b[200~one-time-code", want: "\x1b[200~one-time-code"},
		{name: "incomplete suffix is preserved", input: "one-time-code\x1b[201~", want: "one-time-code\x1b[201~"},
		{name: "whitespace is preserved", input: " one-time-code ", want: " one-time-code "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, []byte(tt.want), unframeLoginPasscode([]byte(tt.input)))
		})
	}
}
