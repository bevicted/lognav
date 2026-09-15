package snapshot

import (
	"testing"
)

func FuzzParseContainer(f *testing.F) {
	seed := makeContainerBytes(f, []struct {
		name string
		data []byte
	}{
		{"a", []byte("aaaa")},
		{"b", []byte("bbbbbbbb")},
	})
	f.Add(seed)
	f.Add([]byte{})
	f.Add([]byte{0xb0, 'L', 'O', 'G', 'N', 'A', 'V', 0xe9, 0, 0, 0, 1})

	f.Fuzz(func(t *testing.T, data []byte) {
		// A strict parse must terminate without panic on any input.
		if c, err := openBytes(t, data); err == nil {
			for _, name := range c.GetFrameNames() {
				_, _ = c.ReadFrame(name)
			}
			_ = c.Close()
		}
	})
}
