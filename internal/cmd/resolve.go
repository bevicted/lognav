package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/spf13/cobra"
)

// latestSentinel is the reserved argument that resolves to the most-recent
// managed snapshot. It is treated as a name, never as a path.
const latestSentinel = "latest"

// isPathArg reports whether arg should be treated as a filesystem PATH rather
// than a managed snapshot NAME. Deterministic and CWD-independent: a path
// separator or a trailing ".lognav" marks a path. Managed names never carry the
// ".lognav" suffix (Scan strips it), so the two namespaces do not overlap. The
// reserved sentinel "latest" is a name, never a path.
func isPathArg(arg string) bool {
	if arg == latestSentinel {
		return false
	}
	return strings.ContainsRune(arg, filepath.Separator) || strings.HasSuffix(arg, snapshot.FileExt)
}

// resolveNameOrPath maps a CLI argument to an Entry. A PATH arg builds an Entry
// from the file directly (no requirement it live in the managed dir); a NAME
// arg (or "latest") goes through snapshot.Resolve.
func resolveNameOrPath(arg string) (snapshot.Entry, error) {
	if isPathArg(arg) {
		return entryFromPath(arg)
	}
	return snapshot.ResolveNameOrID(arg)
}

// resolveLaunchTarget maps a root positional to a snapshot path to open. A PATH
// opens in place (returned unchanged) unless adopt is set, in which case it is
// moved into the managed dir first. A NAME / "latest" resolves to its managed
// path; --adopt is invalid for those.
func resolveLaunchTarget(arg string, adopt bool) (string, error) {
	if isPathArg(arg) {
		e, err := entryFromPath(arg)
		if err != nil {
			return "", WithExit(ExitNoInput, err)
		}
		if adopt {
			return adoptForLaunch(e.Path)
		}
		return e.Path, nil
	}
	if adopt {
		return "", WithExit(ExitUsage, errors.New("--adopt requires a snapshot path, not a name"))
	}
	e, err := snapshot.Resolve(arg)
	if err != nil {
		return "", WithExit(ExitNoInput, err)
	}
	return e.Path, nil
}

// adoptForLaunch moves an external snapshot into the managed dir and returns the
// managed path. It validates the magic header and fails closed on a destination
// collision (no overwrite on the launch path; use `snapshot adopt --force`).
func adoptForLaunch(src string) (string, error) {
	dir, err := snapshot.Dir()
	if err != nil {
		return "", WithExit(ExitGeneral, err)
	}
	if verr := snapshot.VerifyFileMagic(src); verr != nil {
		return "", WithExit(ExitNoInput, fmt.Errorf("%s: %w", src, verr))
	}
	destPath := filepath.Join(dir, adoptDestName(filepath.Base(src)))
	if _, statErr := os.Stat(destPath); statErr == nil {
		return "", WithExit(ExitGeneral, fmt.Errorf("%s already exists; use `lognav snapshot adopt --force` to overwrite", filepath.Base(destPath)))
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", WithExit(ExitGeneral, statErr)
	}
	if terr := transferSnapshot(src, destPath, false); terr != nil { // move
		return "", WithExit(ExitGeneral, terr)
	}
	return destPath, nil
}

// completeNameOrPath completes one managed snapshot name plus "latest" while
// leaving filesystem completion enabled for accepted external paths.
func completeNameOrPath(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := []string{latestSentinel}
	if entries, err := snapshot.Scan(); err == nil {
		for _, e := range entries {
			out = append(out, e.Name)
		}
	}
	return out, cobra.ShellCompDirectiveDefault
}

// entryFromPath validates and builds an Entry for a raw filesystem path. It
// rejects directories and in-progress ".wip" containers (mirroring adopt), and
// requires the file to exist.
func entryFromPath(path string) (snapshot.Entry, error) {
	if strings.HasSuffix(filepath.Base(path), snapshot.WipSuffix) {
		return snapshot.Entry{}, fmt.Errorf("%s: refusing an in-progress .wip file", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return snapshot.Entry{}, fmt.Errorf("%s: no such snapshot file (treated as a path because of the .lognav suffix or a separator; for a managed snapshot use its name without the extension)", path)
		}
		return snapshot.Entry{}, err
	}
	if info.IsDir() {
		return snapshot.Entry{}, fmt.Errorf("%s: is a directory, not a snapshot", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return snapshot.Entry{}, err
	}
	return snapshot.Entry{
		Name:    strings.TrimSuffix(filepath.Base(abs), snapshot.FileExt),
		Path:    abs,
		Kind:    snapshot.KindManual,
		ModTime: info.ModTime(),
		Size:    info.Size(),
	}, nil
}
