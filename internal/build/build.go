package build

import (
	"runtime/debug"
)

const (
	Name        string = "lognav"
	Description string = "Log search for power users"
)

// unknownVersion is reported when neither a module version nor a VCS revision
// is embedded in the build (e.g. built with -buildvcs=false or outside a git
// tree).
const unknownVersion = "unknown"

// shortHashLen is the number of leading hex characters kept from the VCS
// revision, matching the width Go uses in pseudo-versions.
const shortHashLen = 12

var version = resolveVersion()

// GetVersion returns the build version. For a local build from the git tree it
// is the short commit hash, with a "+dirty" suffix when the working tree had
// uncommitted changes. For binaries produced by `go install module@version`
// (which carry no VCS settings) it is the module version: a release tag or a Go
// pseudo-version. It falls back to "unknown" when no build information is
// available.
func GetVersion() string {
	return version
}

// resolveVersion derives the version string from the embedded build info. A
// VCS-stamped local build is preferred as a bare short commit hash (otherwise
// Go reports a noisy synthesized pseudo-version in Main.Version); an installed
// build has no VCS settings, so its module version is used; failing both, the
// result is "unknown".
func resolveVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownVersion
	}

	if v := vcsVersion(info.Settings); v != unknownVersion {
		return v
	}

	// Installed via `go install module@version`: the module version is a clean
	// release tag or a pseudo-version that already embeds the commit.
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}

	return unknownVersion
}

// vcsVersion builds a "<shorthash>[+dirty]" string from the vcs.* build
// settings, or returns "unknown" when no revision was stamped.
func vcsVersion(settings []debug.BuildSetting) string {
	var revision, modified string
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}

	if revision == "" {
		return unknownVersion
	}

	short := revision
	if len(short) > shortHashLen {
		short = short[:shortHashLen]
	}
	if modified == "true" {
		short += "+dirty"
	}
	return short
}
