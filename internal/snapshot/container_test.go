package snapshot

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testFrameHello is a frame name used across container and io tests.
const testFrameHello = "hello"

// newContainerFile creates a file at path and returns a writable container for
// it — the production save shape (os.Create + NewWriter). The caller closes the
// container; the backing file is closed on test cleanup.
func newContainerFile(t *testing.T, path string) *Container {
	t.Helper()
	f, err := os.Create(path) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return NewWriter(f)
}

// openBytes parses an in-memory container image by writing it to a temp file
// and opening it through the read door, OpenContainerReadOnly.
func openBytes(t *testing.T, data []byte) (*Container, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bytes.lognav")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return OpenContainerReadOnly(path)
}

func TestContainerCreateAndRead(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lognav")

	c := newContainerFile(t, path)

	require.NoError(t, c.AppendFrame(testFrameHello, []byte("world")))
	require.NoError(t, c.AppendFrame("foo", []byte("bar")))
	require.NoError(t, c.Close())

	c2, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()

	assert.Equal(t, CurrentFileVersion, c2.GetVersion())
	assert.Equal(t, []string{testFrameHello, "foo"}, c2.GetFrameNames())

	data, err := c2.ReadFrame(testFrameHello)
	require.NoError(t, err)
	assert.Equal(t, []byte("world"), data)

	data, err = c2.ReadFrame("foo")
	require.NoError(t, err)
	assert.Equal(t, []byte("bar"), data)
}

func TestVerifyFileMagic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	valid := filepath.Join(dir, "valid")
	require.NoError(t, os.WriteFile(valid, append(magic[:], 0, 0, 0, 1), 0o600))

	bad := filepath.Join(dir, "bad")
	require.NoError(t, os.WriteFile(bad, []byte("not a snapshot!!"), 0o600))

	short := filepath.Join(dir, "short")
	require.NoError(t, os.WriteFile(short, []byte{0xb0, 'L', 'O'}, 0o600))

	missing := filepath.Join(dir, "nope")

	require.NoError(t, VerifyFileMagic(valid))

	require.ErrorIs(t, VerifyFileMagic(bad), ErrInvalidFileMagic)
	require.ErrorIs(t, VerifyFileMagic(short), ErrInvalidFileMagic)

	err := VerifyFileMagic(missing)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrInvalidFileMagic) // a missing file is an IO error, not bad magic
	assert.True(t, os.IsNotExist(err))
}

func TestContainerDuplicateFrame(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lognav")

	c := newContainerFile(t, path)
	defer func() { _ = c.Close() }()

	require.NoError(t, c.AppendFrame("x", []byte("y")))
	err := c.AppendFrame("x", []byte("z"))
	assert.ErrorContains(t, err, "already exists")
}

func TestContainerMissingFrame(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lognav")

	c := newContainerFile(t, path)
	defer func() { _ = c.Close() }()

	_, err := c.ReadFrame("nope")
	assert.ErrorIs(t, err, ErrFrameDoesNotExist)
}

func TestContainerInvalidMagic(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lognav")
	require.NoError(t, os.WriteFile(path, []byte("not a lognav file"), 0o600))

	_, err := OpenContainerReadOnly(path)
	assert.ErrorIs(t, err, ErrInvalidFileMagic)
}

func TestContainerUnsupportedVersion(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lognav")

	// Write valid magic + version 99
	f, err := os.Create(path) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	_, err = f.Write(magic[:])
	require.NoError(t, err)
	_, err = f.Write([]byte{0, 0, 0, 99})
	require.NoError(t, err)
	require.NoError(t, f.Close())

	_, err = OpenContainerReadOnly(path)
	assert.ErrorIs(t, err, ErrUnsupportedVersion)
}

func testRoundTripBytes(t *testing.T, frames map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	c := NewWriter(&buf)
	for name, data := range frames {
		require.NoError(t, c.AppendFrame(name, data))
	}
	require.NoError(t, c.Close())

	c2, err := openBytes(t, buf.Bytes())
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()

	assert.Equal(t, CurrentFileVersion, c2.GetVersion())
	names := make([]string, 0, len(frames))
	for name := range frames {
		names = append(names, name)
	}
	assert.ElementsMatch(t, names, c2.GetFrameNames())

	for name, expected := range frames {
		data, err := c2.ReadFrame(name)
		require.NoError(t, err)
		assert.Equal(t, expected, data)
	}
}

func TestNewWriterRoundTrip(t *testing.T) {
	t.Parallel()
	testRoundTripBytes(t, map[string][]byte{
		"a": []byte("data-a"),
		"b": []byte("data-b"),
	})
}

func TestContainerEmptyFrameData(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lognav")

	c := newContainerFile(t, path)
	require.NoError(t, c.AppendFrame("empty", []byte{}))
	require.NoError(t, c.Close())

	c2, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()

	data, err := c2.ReadFrame("empty")
	require.NoError(t, err)
	assert.Empty(t, data)
}

// failingWriter fails Write after the first N successful bytes have been written.
type failingWriter struct {
	written int
	failAt  int
	err     error
}

func (fw *failingWriter) Write(p []byte) (int, error) {
	remaining := fw.failAt - fw.written
	if remaining <= 0 {
		return 0, fw.err
	}
	if len(p) <= remaining {
		fw.written += len(p)
		return len(p), nil
	}
	fw.written = fw.failAt
	return remaining, fw.err
}

func TestAppendFrame_SingleWriteVisibleViaBuffer(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	c := NewWriter(&buf)
	require.NoError(t, c.AppendFrame(testFrameHello, []byte("world")))

	// After AppendFrame returns, the buffer must already contain the frame
	// (no Close required). Parse to verify.
	c2, err := openBytes(t, buf.Bytes())
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()
	assert.Equal(t, []string{testFrameHello}, c2.GetFrameNames())
	data, err := c2.ReadFrame(testFrameHello)
	require.NoError(t, err)
	assert.Equal(t, []byte("world"), data)
}

func TestAppendFrame_FlushErrorPoisonsContainer(t *testing.T) {
	t.Parallel()
	fw := &failingWriter{failAt: 0, err: errors.New("disk full")}
	c := NewWriter(fw)

	err := c.AppendFrame("x", []byte("y"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "disk full")

	// The failed flush dropped the writer, so subsequent appends are refused.
	err = c.AppendFrame("z", []byte("w"))
	require.ErrorContains(t, err, "not writable")

	// In-memory frame map must NOT contain the failed frame.
	assert.Empty(t, c.GetFrameNames())
}

func TestAppendFrame_BookkeepingOnlyAfterFlush(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	c := NewWriter(&buf)
	require.NoError(t, c.AppendFrame("a", []byte("aaa")))
	require.NoError(t, c.AppendFrame("b", []byte("bbbb")))
	assert.Equal(t, []string{"a", "b"}, c.GetFrameNames())

	// Offsets must be sequential; second frame must not overlap first.
	c2, err := openBytes(t, buf.Bytes())
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()
	dataA, _ := c2.ReadFrame("a")
	dataB, _ := c2.ReadFrame("b")
	assert.Equal(t, []byte("aaa"), dataA)
	assert.Equal(t, []byte("bbbb"), dataB)
}

// TestAppendFrame_RejectsOverMaxFrameSize covers the write-side enforcement of
// maxFrameBytes via checkFrameSize directly: the read path refuses any frame
// declaring more than maxFrameBytes, so the write path must refuse it too.
// Calling AppendFrame with such content would require a >1 GiB allocation, so
// the size check is exercised at its own seam.
func TestContainerFrameNameUint16(t *testing.T) {
	t.Parallel()

	name := strings.Repeat("x", math.MaxUint8+1)
	var buf bytes.Buffer
	c := NewWriter(&buf)
	require.NoError(t, c.AppendFrame(name, []byte("data")))
	require.NoError(t, c.Close())

	// The first frame header follows magic + version. Its uint16 name length
	// must retain the full 256-byte name rather than the old uint8 truncation.
	raw := buf.Bytes()
	assert.Equal(t, uint16(len(name)), binary.BigEndian.Uint16(raw[len(magic)+4:]))
	c2, err := openBytes(t, raw)
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()
	assert.Equal(t, []string{name}, c2.GetFrameNames())

	tooLong := strings.Repeat("x", math.MaxUint16+1)
	err = NewWriter(io.Discard).AppendFrame(tooLong, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max 65535")
}

func TestAppendFrame_RejectsOverMaxFrameSize(t *testing.T) {
	t.Parallel()

	require.NoError(t, checkFrameSize("logs", 0))
	require.NoError(t, checkFrameSize("logs", int(maxFrameBytes)))

	err := checkFrameSize("logs", int(maxFrameBytes)+1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"logs"`)
	assert.Contains(t, err.Error(), strconv.FormatUint(uint64(maxFrameBytes)+1, 10))
	assert.Contains(t, err.Error(), fmt.Sprintf("max %d", maxFrameBytes))

	// Small frames still go through AppendFrame unimpeded.
	var buf bytes.Buffer
	c := NewWriter(&buf)
	require.NoError(t, c.AppendFrame("logs", []byte("ok")))
}

// containerTestHelper is a minimal interface satisfied by both *testing.T and
// *testing.F, allowing makeContainerBytes to be called from both unit tests
// and fuzz targets.
type containerTestHelper interface {
	Helper()
}

// makeContainerBytes returns the on-disk representation of a container with
// the given frames. Useful for constructing corrupted/truncated inputs.
func makeContainerBytes(h containerTestHelper, frames []struct {
	name string
	data []byte
},
) []byte {
	h.Helper()
	var buf bytes.Buffer
	c := NewWriter(&buf)
	for _, f := range frames {
		if err := c.AppendFrame(f.name, f.data); err != nil {
			panic(err)
		}
	}
	return buf.Bytes()
}

func TestParser_RejectsFrameLengthPastEOF(t *testing.T) {
	t.Parallel()
	full := makeContainerBytes(t, []struct {
		name string
		data []byte
	}{
		{"a", []byte("aaaa")},
		{"b", make([]byte, 100)},
	})
	truncated := full[:len(full)-50]

	_, err := openBytes(t, truncated)
	require.Error(t, err)
}

func TestOpenContainerReadOnly(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lognav")
	c := newContainerFile(t, path)
	require.NoError(t, c.AppendFrame("x", []byte("y")))
	require.NoError(t, c.Close())

	c2, err := OpenContainerReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()

	err = c2.AppendFrame("z", []byte("w"))
	require.ErrorContains(t, err, "not writable")

	data, err := c2.ReadFrame("x")
	require.NoError(t, err)
	assert.Equal(t, []byte("y"), data)
}

func TestOpenContainerReadOnly_Missing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nope.lognav")
	_, err := OpenContainerReadOnly(path)
	require.Error(t, err)
	require.ErrorIs(t, err, fs.ErrNotExist)
	// A read must never mint the file it failed to find.
	assert.NoFileExists(t, path)
}

func TestParser_LenientTolerance(t *testing.T) {
	t.Parallel()

	// Build a "good prefix" container with two complete frames.
	goodPrefix := makeContainerBytes(t, []struct {
		name string
		data []byte
	}{
		{"a", []byte("aaaa")},
		{"b", []byte("bbbbbbbb")},
	})

	cases := []struct {
		name              string
		construct         func() []byte
		wantFramesStrict  []string // nil = expect error in strict mode
		wantFramesLenient []string
	}{
		{
			name: "zero_body",
			construct: func() []byte {
				var b bytes.Buffer
				c := NewWriter(&b)
				_ = c.Close()
				return b.Bytes()
			},
			wantFramesStrict:  []string{},
			wantFramesLenient: []string{},
		},
		{
			name:              "eof_at_frame_boundary",
			construct:         func() []byte { return goodPrefix },
			wantFramesStrict:  []string{"a", "b"},
			wantFramesLenient: []string{"a", "b"},
		},
		{
			name: "eof_in_nameBytes",
			construct: func() []byte {
				b := append([]byte(nil), goodPrefix...)
				b = binary.BigEndian.AppendUint16(b, 5)
				return b
			},
			wantFramesStrict:  nil,
			wantFramesLenient: []string{"a", "b"},
		},
		{
			name: "eof_in_frameLength",
			construct: func() []byte {
				b := append([]byte(nil), goodPrefix...)
				b = binary.BigEndian.AppendUint16(b, 1)
				b = append(b, 'c')
				b = append(b, 0, 0)
				return b
			},
			wantFramesStrict:  nil,
			wantFramesLenient: []string{"a", "b"},
		},
		{
			name: "frameLength_past_eof",
			construct: func() []byte {
				b := append([]byte(nil), goodPrefix...)
				b = binary.BigEndian.AppendUint16(b, 1)
				b = append(b, 'c')
				b = append(b, 0, 0, 1, 0)
				b = append(b, 0, 0, 0)
				return b
			},
			wantFramesStrict:  nil,
			wantFramesLenient: []string{"a", "b"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := tc.construct()

			// Strict mode.
			c, err := openBytes(t, data)
			if tc.wantFramesStrict == nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantFramesStrict, c.GetFrameNames())
				_ = c.Close()
			}

			// Lenient mode via file.
			path := filepath.Join(t.TempDir(), tc.name+".lognav")
			require.NoError(t, os.WriteFile(path, data, 0o600))
			c2, err := OpenContainerReadOnlyWithOpts(path, OpenOpts{Lenient: true})
			require.NoError(t, err)
			defer func() { _ = c2.Close() }()
			assert.Equal(t, tc.wantFramesLenient, c2.GetFrameNames())
		})
	}
}

func TestOpenContainerReadOnlyLenient_PartialTrailingFrame(t *testing.T) {
	t.Parallel()
	full := makeContainerBytes(t, []struct {
		name string
		data []byte
	}{
		{"a", []byte("aaaa")},
		{"b", make([]byte, 100)},
	})
	truncated := full[:len(full)-50]

	path := filepath.Join(t.TempDir(), "partial.lognav")
	require.NoError(t, os.WriteFile(path, truncated, 0o600))

	c, err := OpenContainerReadOnlyWithOpts(path, OpenOpts{Lenient: true})
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	assert.Equal(t, []string{"a"}, c.GetFrameNames())
}

func TestReadFramePointers_RejectsFrameOverMax_Strict(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.snap")

	// Create a valid empty container first.
	c := newContainerFile(t, path)
	require.NoError(t, c.Close())

	// Append a malformed frame: nameLength=1, name='x', frameLength=maxFrameBytes+1.
	// Truncate the temp file to a sparse large size so the EOF check is not what fires.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	header := binary.BigEndian.AppendUint16(nil, 1)
	header = append(header, 'x', 0, 0, 0, 0)
	binary.BigEndian.PutUint32(header[3:], maxFrameBytes+1)
	_, err = f.Write(header)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(int64(maxFrameBytes)+100))
	require.NoError(t, f.Close())

	_, err = OpenContainerReadOnly(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "> max")
}

func TestReadFramePointers_LenientBreaksOnOverMax(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.snap")

	c := newContainerFile(t, path)
	require.NoError(t, c.Close())

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	header := binary.BigEndian.AppendUint16(nil, 1)
	header = append(header, 'x', 0, 0, 0, 0)
	binary.BigEndian.PutUint32(header[3:], maxFrameBytes+1)
	_, err = f.Write(header)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(int64(maxFrameBytes)+100))
	require.NoError(t, f.Close())

	c2, err := OpenContainerReadOnlyWithOpts(path, OpenOpts{Lenient: true})
	require.NoError(t, err)
	require.NoError(t, c2.Close())
}
