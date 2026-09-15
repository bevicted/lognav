package snapshot

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
)

const CurrentFileVersion uint32 = 1

// maxFrameBytes bounds the size of any single container frame, enforced on
// both write (AppendFrame) and read (readFrameHeader) so that anything we
// wrote, we can read back. 1 GiB is >100× the realistic state-frame +
// per-instance log-frame; legitimate snapshots stay far below. On read, a
// larger value indicates either corruption (strict mode → error) or a torn
// write — a partial OS flush could split the 4-byte frameLength field,
// producing a garbage value greater than max (lenient mode → break, behave
// like the existing trailing-EOF recovery path).
const maxFrameBytes uint32 = 1 << 30

// checkFrameSize rejects frame content that AppendFrame could write but the
// read path would refuse (frameLength > maxFrameBytes).
func checkFrameSize(name string, n int) error {
	if n > int(maxFrameBytes) {
		return fmt.Errorf("frame %q content size %d > max %d", name, n, maxFrameBytes)
	}
	return nil
}

var supportedVersions = map[uint32]bool{
	1: true,
}

var (
	ErrInvalidFileMagic   = errors.New("invalid file magic")
	ErrUnsupportedVersion = errors.New("unsupported file version")
	ErrFrameDoesNotExist  = errors.New("frame does not exist")
)

var magic = [8]byte{176, 'L', 'O', 'G', 'N', 'A', 'V', 233}

// OpenOpts configures snapshot.Container open behavior.
type OpenOpts struct {
	// Lenient causes the parser to tolerate trailing partial frames
	// (clean or unexpected EOF mid-frame, declared length past EOF) by
	// returning the frames parsed so far rather than an error. Use only
	// for known-in-progress files. Strict mode (default) preserves the
	// current error contract for finalized files.
	Lenient bool
}

// Container is a binary file format for .lognav snapshot files.
// It stores a magic header, version, and sequential named frames.
// New files are writable; existing files are opened read-only.
type Container struct {
	reader       io.ReaderAt
	file         *os.File
	writer       *bufio.Writer
	offset       int64
	version      uint32
	frames       map[string]frame
	orderedNames []string
}

type frame struct {
	offset int64
	size   uint32
}

func newFileHeader() []byte {
	h := make([]byte, 0, len(magic)+4)
	h = append(h, magic[:]...)
	h = binary.BigEndian.AppendUint32(h, CurrentFileVersion)
	return h
}

// NewWriter creates a write-only container that writes directly to w.
// Use this for the save path where no file-based reading is needed.
// The caller must call Close when done to flush buffered data.
func NewWriter(w io.Writer) *Container {
	bw := bufio.NewWriter(w)
	header := newFileHeader()
	// Write is buffered, so this won't fail unless the buffer itself is broken.
	_, _ = bw.Write(header)

	return &Container{
		writer:       bw,
		offset:       int64(len(header)),
		frames:       map[string]frame{},
		orderedNames: []string{},
		version:      CurrentFileVersion,
	}
}

// parseContainerWithOpts reads the header, version, and frame index from a
// ReadSeeker. Returns the version, frame map, ordered frame names, and final
// offset (end of last frame).
func parseContainerWithOpts(rs io.ReadSeeker, opts OpenOpts) (uint32, map[string]frame, []string, int64, error) {
	if err := checkHeader(rs); err != nil {
		return 0, nil, nil, 0, err
	}

	version, err := readUint32(rs)
	if err != nil {
		return 0, nil, nil, 0, unexpectEOF(err)
	}

	if !supportedVersions[version] {
		return 0, nil, nil, 0, fmt.Errorf("%w: %d", ErrUnsupportedVersion, version)
	}

	size, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, nil, nil, 0, err
	}
	startOffset := int64(len(magic)) + 4
	if _, err := rs.Seek(startOffset, io.SeekStart); err != nil {
		return 0, nil, nil, 0, err
	}

	frames, orderedNames, err := readFramePointers(rs, startOffset, size, opts)
	if err != nil {
		return 0, nil, nil, 0, err
	}

	offset, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, nil, nil, 0, err
	}

	return version, frames, orderedNames, offset, nil
}

// OpenContainerReadOnly opens a container file read-only with strict parsing.
// Returns wrapped fs.ErrNotExist if the file does not exist.
func OpenContainerReadOnly(path string) (*Container, error) {
	return OpenContainerReadOnlyWithOpts(path, OpenOpts{})
}

// OpenContainerReadOnlyWithOpts opens a container file read-only with the
// given parser options. Use OpenOpts{Lenient: true} for known-in-progress
// (e.g., .wip) files where trailing partial frames are expected.
func OpenContainerReadOnlyWithOpts(path string, opts OpenOpts) (*Container, error) {
	path = filepath.Clean(path)
	file, err := os.OpenFile(path, os.O_RDONLY, 0) // #nosec G304 -- caller-supplied snapshot path; filepath.Clean applied
	if err != nil {
		return nil, err
	}

	version, frames, orderedNames, offset, err := parseContainerWithOpts(file, opts)
	if err != nil {
		_ = file.Close()
		return nil, err
	}

	return &Container{
		reader:       file,
		file:         file,
		writer:       nil,
		offset:       offset,
		version:      version,
		frames:       frames,
		orderedNames: orderedNames,
	}, nil
}

// readFrameHeader reads one frame's name-length, name, and declared frameLength
// from r. Returns (name, frameLength, nil) on success, ("", 0, io.EOF) at a
// clean frame boundary, or a wrapped error on any other failure.
func readFrameHeader(r io.Reader) (string, uint32, error) {
	nameLength, err := readUint16(r)
	if err != nil {
		return "", 0, err // io.EOF at boundary is the caller's clean-break signal
	}

	nameBytes := make([]byte, nameLength)
	if _, err := io.ReadFull(r, nameBytes); err != nil {
		return "", 0, unexpectEOF(err)
	}

	frameLength, err := readUint32(r)
	if err != nil {
		return "", 0, unexpectEOF(err)
	}

	if frameLength > maxFrameBytes {
		return string(nameBytes), 0, fmt.Errorf("frame %q declares size %d > max %d", string(nameBytes), frameLength, maxFrameBytes)
	}

	return string(nameBytes), frameLength, nil
}

// readFramePointers scans all frames starting from startingOffset and returns
// both the frame map and the names in insertion (file) order.
// fileSize is used to validate that each frame's declared length does not extend past EOF.
func readFramePointers(r io.ReadSeeker, startingOffset, fileSize int64, opts OpenOpts) (map[string]frame, []string, error) {
	framePointers := map[string]frame{}
	orderedNames := []string{}
	currentOffset := startingOffset

	for {
		name, frameLength, err := readFrameHeader(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if opts.Lenient {
				break
			}
			return nil, nil, err
		}

		nameLength := uint16(len(name))
		dataOffset := currentOffset + 2 + int64(nameLength) + 4
		if dataOffset+int64(frameLength) > fileSize {
			if opts.Lenient {
				break
			}
			return nil, nil, io.ErrUnexpectedEOF
		}

		framePointers[name] = frame{
			offset: dataOffset,
			size:   frameLength,
		}
		orderedNames = append(orderedNames, name)

		currentOffset = dataOffset + int64(frameLength)

		if _, err = r.Seek(int64(frameLength), io.SeekCurrent); err != nil {
			if opts.Lenient {
				break
			}
			return nil, nil, err
		}
	}

	return framePointers, orderedNames, nil
}

func unexpectEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func readUint32(reader io.Reader) (uint32, error) {
	buf := [4]byte{}
	if _, err := io.ReadFull(reader, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buf[:]), nil
}

func readUint16(reader io.Reader) (uint16, error) {
	buf := [2]byte{}
	if _, err := io.ReadFull(reader, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(buf[:]), nil
}

func checkHeader(reader io.Reader) error {
	magicBuf := make([]byte, len(magic))
	if _, err := io.ReadFull(reader, magicBuf); err != nil {
		return ErrInvalidFileMagic
	}
	if !bytes.Equal(magicBuf, magic[:]) {
		return ErrInvalidFileMagic
	}
	return nil
}

// VerifyFileMagic reports whether the file at path begins with the lognav
// container magic. It reads only the 8 magic bytes — it does not open the full
// container or build a frame index. Returns nil on a match,
// ErrInvalidFileMagic on a mismatch or short file, or the underlying IO error
// (e.g. a missing file) otherwise.
func VerifyFileMagic(path string) error {
	f, err := os.Open(path) // #nosec G304 -- caller-provided snapshot path
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return checkHeader(f)
}

// Close flushes internal buffers and closes the file handle (if any).
func (c *Container) Close() error {
	if c.writer != nil {
		if err := c.writer.Flush(); err != nil {
			return err
		}
	}
	if c.file != nil {
		_ = c.file.Sync()
		return c.file.Close()
	}
	return nil
}

// ReadFrame reads the specified frame by name.
func (c *Container) ReadFrame(name string) ([]byte, error) {
	fp, ok := c.frames[name]
	if !ok {
		return nil, ErrFrameDoesNotExist
	}
	data := make([]byte, fp.size)
	if _, err := c.reader.ReadAt(data, fp.offset); err != nil {
		return nil, err
	}
	return data, nil
}

// AppendFrame writes a named frame to the end of the file as a single buffered
// write followed by an explicit Flush. Bookkeeping (frames map, offset,
// orderedNames) is updated only after Flush succeeds. On write or flush failure
// the writer is dropped, so the container becomes non-writable and subsequent
// AppendFrame calls fail.
func (c *Container) AppendFrame(name string, content []byte) error {
	// writer is nil when the container was opened read-only, or after a failed
	// write/flush dropped it (below) -- either way it is no longer writable.
	if c.writer == nil {
		return errors.New("container is not writable")
	}
	if _, ok := c.frames[name]; ok {
		return fmt.Errorf("frame %q already exists", name)
	}
	if len(name) > math.MaxUint16 {
		return fmt.Errorf("frame name too long: %d bytes (max %d)", len(name), math.MaxUint16)
	}
	if err := checkFrameSize(name, len(content)); err != nil {
		return err
	}

	headerSize := 2 + len(name) + 4
	buf := make([]byte, 0, headerSize+len(content))
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(name)))
	buf = append(buf, []byte(name)...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(content)))
	buf = append(buf, content...)

	if _, err := c.writer.Write(buf); err != nil {
		c.writer = nil
		return fmt.Errorf("write frame %q: %w", name, err)
	}
	if err := c.writer.Flush(); err != nil {
		c.writer = nil
		return fmt.Errorf("flush frame %q: %w", name, err)
	}

	c.frames[name] = frame{
		offset: c.offset + int64(headerSize),
		size:   uint32(len(content)),
	}
	c.orderedNames = append(c.orderedNames, name)
	c.offset += int64(len(buf))

	return nil
}

// GetFrameNames returns the names of all frames in insertion order.
func (c *Container) GetFrameNames() []string {
	return slices.Clone(c.orderedNames)
}

// GetVersion returns the container's file version.
func (c *Container) GetVersion() uint32 {
	return c.version
}
