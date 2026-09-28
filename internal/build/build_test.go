package build

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEffectiveVersion_PrefersPackageStamp(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "v1.2.3", effectiveVersion("v1.2.3"))
}

func TestVersionFromBuildInfo(t *testing.T) {
	t.Parallel()

	const (
		fullSHA       = "7295978844ee493c2f866b5e7fa67e42b4082c49"
		pseudoVersion = "v1.0.0-rc.3.0.20260928115037-7295978844ee"
	)

	tests := []struct {
		name     string
		version  string
		settings []debug.BuildSetting
		want     string
	}{
		{
			name:    "clean tagged build uses release version",
			version: "v1.0.0-rc.3",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: fullSHA},
				{Key: "vcs.modified", Value: "false"},
			},
			want: "v1.0.0-rc.3",
		},
		{
			name:    "dirty tagged build uses revision",
			version: "v1.0.0-rc.3",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: fullSHA},
				{Key: "vcs.modified", Value: "true"},
			},
			want: "7295978844ee+dirty",
		},
		{
			name:    "clean untagged build uses revision",
			version: pseudoVersion,
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: fullSHA},
				{Key: "vcs.modified", Value: "false"},
			},
			want: "7295978844ee",
		},
		{
			name:    "installed pseudo-version uses module version",
			version: pseudoVersion,
			want:    pseudoVersion,
		},
		{
			name: "development build without VCS is unknown",
			want: unknownVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info := &debug.BuildInfo{
				Main:     debug.Module{Version: tt.version},
				Settings: tt.settings,
			}
			assert.Equal(t, tt.want, versionFromBuildInfo(info))
		})
	}
}

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
