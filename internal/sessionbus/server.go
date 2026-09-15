package sessionbus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// EventSender delivers an event onto the runtime's owned event channel.
type EventSender func(uv.Event)

// Server receives snapshot notifications from peer sessions over this process's
// Unix socket. The optional Socket is a test seam; production uses this process's
// PID socket.
type Server struct {
	send       EventSender
	httpServer *http.Server
	listener   net.Listener
	socket     Socket
	logger     *slog.Logger

	closeMu sync.Mutex
	closed  bool
}

// NewServer creates a peer notification server.
func NewServer(send EventSender, sockets ...Socket) *Server {
	s := &Server{
		send:   send,
		logger: slog.Default().With(logging.KeyComponent, "sessionbus-server"),
	}
	if len(sockets) > 0 {
		s.socket = sockets[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", s.handle)
	s.httpServer = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return s
}

// Start listens on this process's Unix socket and serves peer notifications
// until Close.
func (s *Server) Start(ctx context.Context) {
	if s.socket.Path == "" {
		socket, err := NewSocketFromPID()
		if err != nil {
			s.logger.Error("failed to create socket", logging.KeyError, err)
			return
		}
		s.socket = socket
	}

	if err := os.MkdirAll(filepath.Dir(s.socket.Path), 0o700); err != nil {
		s.logger.Error("failed to create socket directory", logging.KeyError, err)
		return
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", s.socket.Path)
	if err != nil {
		s.logger.Error("server failed to listen", logging.KeyError, err)
		return
	}
	s.listener = ln
	s.logger.Info("server listening", "path", s.socket.Path)
	go func() {
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("server error", logging.KeyError, err)
		}
	}()
}

// Close shuts down the server and removes its socket. Its first caller owns
// cleanup and receives any joined cleanup errors; all later callers return nil.
func (s *Server) Close(ctx context.Context) error {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil
	}
	s.closed = true
	s.closeMu.Unlock()

	var errs []error
	if s.listener != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("server shutdown failed", logging.KeyError, err)
			errs = append(errs, err)
		}
	}
	if s.socket.Path != "" {
		if err := os.Remove(s.socket.Path); err != nil && !os.IsNotExist(err) {
			s.logger.Error("socket removal failed", "path", s.socket.Path, logging.KeyError, err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) writeResponse(w http.ResponseWriter, status int, resp Response) {
	w.Header().Set("Content-Type", "application/json")
	body, err := jsonutil.API.Marshal(resp)
	if err != nil {
		s.logger.Error("response marshal failed", logging.KeyError, err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error"}` + "\n"))
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var req Request
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeResponse(w, http.StatusBadRequest, Response{Error: "read request body failed"})
		return
	}
	if err := jsonutil.API.Unmarshal(body, &req); err != nil {
		s.writeResponse(w, http.StatusBadRequest, Response{Error: "malformed request body"})
		return
	}
	s.writeResponse(w, http.StatusOK, s.dispatch(req))
}

func (s *Server) dispatch(req Request) Response {
	switch req.Method {
	case MethodSnapshotRenamed:
		var p SnapshotRenamedParams
		if err := jsonutil.API.Unmarshal(req.Params, &p); err != nil {
			return Response{Error: fmt.Sprintf("bad params: %v", err)}
		}
		from, okFrom := SanitizeBasename(p.From)
		to, okTo := SanitizeBasename(p.To)
		if !okFrom || !okTo || from == to {
			s.logger.Warn("rejected snapshot_renamed", "from", p.From, "to", p.To)
			return Response{Error: "invalid rename notify"}
		}
		s.send(msgs.SnapshotRenamedMsg{From: from, To: to})
		return Response{Result: "ok"}
	case MethodSnapshotsDirty:
		s.send(msgs.SnapshotsDirtyMsg{})
		return Response{Result: "ok"}
	default:
		return Response{Error: "unknown method: " + req.Method}
	}
}
