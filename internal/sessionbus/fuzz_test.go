package sessionbus

import (
	"path/filepath"
	"strings"
	"testing"
)

func FuzzSanitizeBasename(f *testing.F) {
	f.Add("foo.lognav")
	f.Add("../../etc/passwd")
	f.Add("a\x00b")
	f.Fuzz(func(t *testing.T, in string) {
		got, ok := SanitizeBasename(in) // must never panic
		if ok {
			if got != filepath.Base(got) || strings.ContainsAny(got, `/\`+"\x00") || got == "" || got == "." || got == ".." {
				t.Fatalf("accepted unsafe basename %q from %q", got, in)
			}
		}
	})
}
