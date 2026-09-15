package sessionbus

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeBasename(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"foo.lognav", "foo.lognav", true},
		{"../escape.lognav", "", false},
		{"a/b.lognav", "", false},
		{"", "", false},
		{".", "", false},
		{"..", "", false},
		{"with\x00null", "", false},
	}
	for _, c := range cases {
		got, ok := SanitizeBasename(c.in)
		assert.Equal(t, c.ok, ok, "in=%q", c.in)
		assert.Equal(t, c.want, got, "in=%q", c.in)
	}
}
