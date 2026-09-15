package archivehandler

import (
	"path/filepath"
	"sync"

	"github.com/bevicted/lognav/internal/archive"
)

// archiveCache holds the archives decoded by ONE archive.Scan() pass, keyed by
// the on-disk basename WITHOUT the extension.
//
// It exists because a refresh consults every archive file twice: once for the
// row's color and once for its preview. Decoding per hook meant re-deriving the
// path (an MkdirAll inside archive.Dir) and re-reading + re-unmarshalling the
// same JSON for every row. The listing's NewRowStyler hook now fills the cache
// with a single directory walk and both hooks read it.
//
// All of its callers are OFF-loop (the filehandler's ListFiles worker fills it;
// one preview worker per file reads it, concurrently with the next listing's
// fill), hence the mutex.
type archiveCache struct {
	mu     sync.Mutex
	byName map[string]*archive.Archive // nil until the first fill
}

// fill decodes the whole registry in one directory walk and replaces the cached
// map — one call per listing. A scan error leaves an empty (non-nil) map: every
// lookup then takes get's single-file fallback, which is what the pre-cache code
// did for every row anyway.
func (c *archiveCache) fill() {
	entries, err := archive.Scan()
	byName := make(map[string]*archive.Archive, len(entries))
	if err == nil {
		for _, e := range entries {
			byName[e.Name] = e.Archive
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byName = byName
}

// get returns the decoded archive named base (basename WITHOUT the extension)
// from the current listing's scan. A name that scan did not see — a file created
// between the file browser's own directory walk and ours, or one unreadable at
// scan time — falls back to a single-file read, so such a row behaves exactly as
// it did before the cache.
func (c *archiveCache) get(base string) (*archive.Archive, error) {
	c.mu.Lock()
	a, ok := c.byName[base]
	c.mu.Unlock()
	if ok {
		return a, nil
	}
	return loadOne(base)
}

// loadOne is get's cache-miss fallback: resolve the path for one basename and
// read that file alone.
func loadOne(base string) (*archive.Archive, error) {
	p, err := archive.PathFor(base)
	if err != nil {
		return nil, err
	}
	return archive.Load(p)
}

// baseOf maps an archive file path to its cache key: the basename with the
// archive extension stripped.
func baseOf(path string) string { return trimExt(filepath.Base(path)) }
