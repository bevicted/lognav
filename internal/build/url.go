package build

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// defaultRepoURL is the public GitHub web base used when the build carries no module path
// (e.g. a file-list `go build x.go`).
const defaultRepoURL = "https://github.com/bevicted/lognav"

// pseudoVersionTail matches the trailing 12-hex commit of a Go pseudo-version
// (e.g. "...-ed33a7ea2bde"). A release tag or bare hash does not match.
var pseudoVersionTail = regexp.MustCompile(`-([0-9a-f]{12})$`)

// RepoURL returns the project's public GitHub web base, derived from the
// module path embedded in the build ("https://" + module path), falling back to
// defaultRepoURL when no module path is available.
func RepoURL() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return repoURL(info.Main.Path)
	}
	return repoURL("")
}

func repoURL(modulePath string) string {
	if modulePath == "" {
		return defaultRepoURL
	}
	return "https://" + modulePath
}

// VersionRef maps GetVersion() to a git ref usable in a GitHub permalink: a release
// tag verbatim, the commit hash extracted from a pseudo-version, a bare local
// hash verbatim, or "unknown".
func VersionRef() string {
	return refFromVersion(GetVersion())
}

func refFromVersion(version string) string {
	v := strings.TrimSuffix(version, "+dirty")
	if m := pseudoVersionTail.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return v
}
