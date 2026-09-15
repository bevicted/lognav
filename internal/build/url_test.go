package build

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRepoURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"module path becomes https base", "github.com/bevicted/lognav", "https://github.com/bevicted/lognav"},
		{"empty path falls back to default", "", defaultRepoURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, repoURL(tt.input))
		})
	}
}

func TestVersionRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"release tag verbatim", "v0.1.1", "v0.1.1"},
		{"pseudo-version yields commit hash", "v0.1.2-0.20260611162901-ed33a7ea2bde", "ed33a7ea2bde"},
		{"bare hash verbatim", "ed33a7ea2bde", "ed33a7ea2bde"},
		{"dirty suffix stripped", "ed33a7ea2bde+dirty", "ed33a7ea2bde"},
		{"unknown verbatim", "unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, refFromVersion(tt.version))
		})
	}
}
