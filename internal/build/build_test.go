package build

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVCSVersion(t *testing.T) {
	t.Parallel()

	const fullSHA = "ed33a7ea2bde91e3a7e653ba35afcd1a19e00949"

	tests := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{
			name:     "no settings yields unknown",
			settings: nil,
			want:     unknownVersion,
		},
		{
			name:     "no revision yields unknown",
			settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}},
			want:     unknownVersion,
		},
		{
			name: "clean revision truncated to short hash",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: fullSHA},
				{Key: "vcs.modified", Value: "false"},
			},
			want: "ed33a7ea2bde",
		},
		{
			name: "dirty revision gets dirty suffix",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: fullSHA},
				{Key: "vcs.modified", Value: "true"},
			},
			want: "ed33a7ea2bde+dirty",
		},
		{
			name:     "short revision kept verbatim",
			settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}},
			want:     "abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, vcsVersion(tt.settings))
		})
	}
}
