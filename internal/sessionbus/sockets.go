package sessionbus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/xdg"
)

const socketDir = "sessionbus"

// Request is the JSON body sent over the Unix session bus to a TUI session.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the JSON body returned by a TUI session.
type Response struct {
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Socket identifies a peer session's Unix socket.
type Socket struct {
	Path string
	PID  int
}

func (s Socket) String() string {
	return fmt.Sprintf("pid %d", s.PID)
}

// statForOwner is a package-internal seam returning the owning uid of path.
// It uses Lstat so a final socket-entry symlink is never followed. Test code
// substitutes it to exercise foreign-uid paths without requiring root.
var statForOwner = func(path string) (uint32, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, syscall.EINVAL
	}
	return st.Uid, nil
}

// IsSessionAlive returns true when the socket file is owned by the current
// user AND the recorded PID is reachable. The final entry is checked with
// Lstat before its owner is read, so a symlink is never considered a socket.
// Ownership is checked before kill(0) to avoid signaling a foreign PID.
func (s Socket) IsSessionAlive() bool {
	info, err := os.Lstat(s.Path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	uid, err := statForOwner(s.Path)
	if err != nil || uid != uint32(os.Getuid()) {
		return false
	}
	return syscall.Kill(s.PID, 0) == nil
}

func getSocketFromPath(path string) (Socket, error) {
	name := filepath.Base(path)
	if !strings.HasSuffix(name, ".sock") {
		return Socket{}, fmt.Errorf("invalid socket name %q", name)
	}
	pidText := strings.TrimSuffix(name, ".sock")
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 || strconv.Itoa(pid) != pidText {
		return Socket{}, fmt.Errorf("invalid socket pid in %q", name)
	}
	return Socket{Path: filepath.Join(filepath.Dir(path), name), PID: pid}, nil
}

// GetSocketFromPID returns the socket path associated with pid.
func GetSocketFromPID(pid int) (Socket, error) {
	dir, err := SocketDir()
	if err != nil {
		return Socket{}, err
	}
	return getSocketFromPath(filepath.Join(dir, fmt.Sprintf("%d.sock", pid)))
}

// NewSocketFromPID creates a Socket for the current process PID, removing any
// stale socket file at the expected path.
func NewSocketFromPID() (Socket, error) {
	dir, err := SocketDir()
	if err != nil {
		return Socket{}, err
	}
	pid := os.Getpid()
	s := Socket{Path: filepath.Join(dir, fmt.Sprintf("%d.sock", pid)), PID: pid}
	if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
		return Socket{}, fmt.Errorf("failed to remove stale socket: %w", err)
	}
	return s, nil
}

// ListSockets returns all live peer sockets, removing stale entries.
func ListSockets() ([]Socket, error) {
	dir, err := SocketDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sockets []Socket
	for _, entry := range entries {
		entryPath := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(entryPath)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Remove the entry itself. Never resolve it: it may point outside the
			// sessionbus directory.
			_ = os.Remove(entryPath)
			continue
		}
		socket, err := getSocketFromPath(entryPath)
		if err != nil {
			continue
		}
		if !socket.IsSessionAlive() {
			// entryPath is the directory entry discovered above. socket.Path may
			// have a canonical parent, but cleanup must never target elsewhere.
			_ = os.Remove(entryPath)
			continue
		}
		sockets = append(sockets, socket)
	}
	return sockets, nil
}

// SocketDir returns the directory where session sockets are stored, creating it
// if necessary.
func SocketDir() (string, error) {
	statePath, err := xdg.GetStatePath()
	if err != nil {
		return "", err
	}
	p := filepath.Join(statePath, socketDir)
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	// Resolve only the socket directory, never an individual socket entry.
	return filepath.EvalSymlinks(p)
}

// SocketPath returns the socket path for the current process PID.
func SocketPath() (string, error) {
	dir, err := SocketDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%d.sock", os.Getpid())), nil
}

// CallSession sends a command to a specific session socket and returns the result.
// An optional timeout overrides the default 2-second deadline for both dial and read.
func CallSession(ctx context.Context, sockPath, method string, params any, timeout ...time.Duration) (string, error) {
	deadline := 2 * time.Second
	if len(timeout) > 0 {
		deadline = timeout[0]
	}
	var paramsJSON json.RawMessage
	if params != nil {
		paramsJSON, _ = jsonutil.API.Marshal(params)
	}
	reqBody, _ := jsonutil.API.Marshal(Request{Method: method, Params: paramsJSON})
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/", strings.NewReader(string(reqBody)))
	if err != nil {
		return "", fmt.Errorf("failed to build session request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := unixClient(sockPath).Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("failed to connect to session at %s: %w", sockPath, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read session response body: %w", err)
	}
	var sessResp Response
	if err := jsonutil.API.Unmarshal(body, &sessResp); err != nil {
		return "", fmt.Errorf("failed to decode session response: %w", err)
	}
	if sessResp.Error != "" {
		return "", fmt.Errorf("%s", sessResp.Error)
	}
	return sessResp.Result, nil
}

// unixClient returns an http.Client that dials the given Unix socket. Keep-
// alives are disabled so each one-shot session call uses a fresh connection.
func unixClient(sockPath string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
		DisableKeepAlives: true,
	}}
}
