package socksrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/agentctl/agentctl/internal/api"
	"github.com/agentctl/agentctl/internal/proto"
)

type Server struct {
	socketPath string
	apiSrv     *api.Server
	logger     *slog.Logger
	listener   net.Listener
	wg         sync.WaitGroup
	closing    chan struct{}
}

type Options struct {
	SocketPath string
	API        *api.Server
	Logger     *slog.Logger
}

func New(opts Options) *Server {
	return &Server{
		socketPath: opts.SocketPath,
		apiSrv:     opts.API,
		logger:     opts.Logger,
		closing:    make(chan struct{}),
	}
}

func (s *Server) Start() error {
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("sock.parent_perm_chmod_failed", slog.String("path", dir), slog.String("error", err.Error()))
	}
	if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove existing socket %s: %w", s.socketPath, err)
	}
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", s.socketPath, err)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}
	s.listener = ln
	s.wg.Add(1)
	go s.acceptLoop()
	return nil
}

func (s *Server) Close() error {
	close(s.closing)
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.wg.Wait()
	_ = os.Remove(s.socketPath)
	return nil
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.closing:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.logger.Warn("sock.accept_failed", slog.String("error", err.Error()))
			return
		}
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer func() { _ = conn.Close() }()
	for {
		payload, err := ReadFrame(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.logger.Debug("sock.read_failed", slog.String("error", err.Error()))
			}
			return
		}
		var frame proto.Frame
		if err := json.Unmarshal(payload, &frame); err != nil {
			s.writeError(conn, "", proto.ErrBadRequest, "invalid frame: "+err.Error())
			continue
		}
		s.dispatch(conn, frame)
	}
}

func (s *Server) dispatch(conn net.Conn, frame proto.Frame) {
	if frame.Kind != proto.KindRequest {
		s.writeError(conn, frame.ID, proto.ErrBadRequest, fmt.Sprintf("unexpected kind %q", frame.Kind))
		return
	}
	switch frame.Op {
	case proto.OpHealth:
		resp := s.apiSrv.Health(context.Background())
		s.writeResponse(conn, frame.ID, resp)
	default:
		s.writeError(conn, frame.ID, proto.ErrBadRequest, "unknown op: "+frame.Op)
	}
}

func (s *Server) writeResponse(conn net.Conn, id string, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		s.writeError(conn, id, proto.ErrInternal, err.Error())
		return
	}
	frame := proto.Frame{V: proto.ProtocolVersion, ID: id, Kind: proto.KindResponse, Data: body}
	out, err := json.Marshal(frame)
	if err != nil {
		s.writeError(conn, id, proto.ErrInternal, err.Error())
		return
	}
	if err := WriteFrame(conn, out); err != nil {
		s.logger.Debug("sock.write_failed", slog.String("error", err.Error()))
	}
}

func (s *Server) writeError(conn net.Conn, id, code, msg string) {
	body, _ := json.Marshal(proto.ErrorData{Code: code, Message: msg})
	frame := proto.Frame{V: proto.ProtocolVersion, ID: id, Kind: proto.KindError, Data: body}
	out, _ := json.Marshal(frame)
	_ = WriteFrame(conn, out)
}
