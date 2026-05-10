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

	"time"

	"github.com/agentctl/agentctl/internal/api"
	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/skills"
	"github.com/agentctl/agentctl/internal/sm"
	"github.com/agentctl/agentctl/internal/usage"
)

type SessionLogStreamer interface {
	Stream(ctx context.Context, sessionID string, follow bool, send func(line []byte) error) error
}

type ContainerLogStreamer interface {
	Stream(ctx context.Context, sessionID string, follow bool, send func(line []byte) error) error
}

// UsageAggregator is the subset of usage.Aggregator the socket server needs
// for the GetCost op (R10). Declared as an interface so tests can stub it.
type UsageAggregator interface {
	PerSession(ctx context.Context, sessionID string) (usage.PerSessionTotals, error)
	Range(ctx context.Context, start, end time.Time, sessionFilter string) (usage.RangeTotals, error)
}

type Server struct {
	socketPath    string
	apiSrv        *api.Server
	manager       sm.Manager
	mcps          mcp.Registry
	skills        skills.Manager
	logStream     SessionLogStreamer
	containerLogs ContainerLogStreamer
	usage         UsageAggregator
	logger        *slog.Logger
	listener      net.Listener
	wg            sync.WaitGroup
	closing       chan struct{}
	writeMu       sync.Mutex
}

type Options struct {
	SocketPath    string
	API           *api.Server
	Manager       sm.Manager
	MCPs          mcp.Registry
	Skills        skills.Manager
	LogStream     SessionLogStreamer
	ContainerLogs ContainerLogStreamer
	Usage         UsageAggregator
	Logger        *slog.Logger
}

func New(opts Options) *Server {
	return &Server{
		socketPath:    opts.SocketPath,
		apiSrv:        opts.API,
		manager:       opts.Manager,
		mcps:          opts.MCPs,
		skills:        opts.Skills,
		logStream:     opts.LogStream,
		containerLogs: opts.ContainerLogs,
		usage:         opts.Usage,
		logger:        opts.Logger,
		closing:       make(chan struct{}),
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

type connWriter struct {
	conn net.Conn
	mu   sync.Mutex
}

func (w *connWriter) write(payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return WriteFrame(w.conn, payload)
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer func() { _ = conn.Close() }()
	cw := &connWriter{conn: conn}
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
			s.writeError(cw, "", proto.ErrBadRequest, "invalid frame: "+err.Error())
			continue
		}
		s.dispatch(cw, frame)
	}
}

func (s *Server) dispatch(cw *connWriter, frame proto.Frame) {
	if frame.Kind != proto.KindRequest {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, fmt.Sprintf("unexpected kind %q", frame.Kind))
		return
	}
	switch frame.Op {
	case proto.OpHealth:
		resp := s.apiSrv.Health(context.Background())
		s.writeResponse(cw, frame.ID, resp)
	case proto.OpCreateSession:
		s.handleCreateSession(cw, frame)
	case proto.OpListSessions:
		s.handleListSessions(cw, frame)
	case proto.OpGetSession:
		s.handleGetSession(cw, frame)
	case proto.OpSendMessage:
		s.handleSendMessage(cw, frame)
	case proto.OpInterrupt:
		s.handleInterrupt(cw, frame)
	case proto.OpTerminateSession:
		s.handleTerminate(cw, frame)
	case proto.OpRestartSession:
		s.handleRestart(cw, frame)
	case proto.OpAttachStream:
		go s.handleAttachStream(cw, frame)
	case proto.OpGetLogs:
		go s.handleGetLogs(cw, frame)
	case proto.OpGetContainerLogs:
		go s.handleGetContainerLogs(cw, frame)
	case proto.OpListMCPs:
		s.handleListMCPs(cw, frame)
	case proto.OpAddMCP:
		s.handleAddMCP(cw, frame)
	case proto.OpUpdateMCP:
		s.handleUpdateMCP(cw, frame)
	case proto.OpRemoveMCP:
		s.handleRemoveMCP(cw, frame)
	case proto.OpSetDefaultMCP:
		s.handleSetDefaultMCP(cw, frame)
	case proto.OpListInstalledSkills:
		s.handleListInstalledSkills(cw, frame)
	case proto.OpAddSkill:
		s.handleAddSkill(cw, frame)
	case proto.OpRemoveSkill:
		s.handleRemoveSkill(cw, frame)
	case proto.OpImportSkill:
		s.handleImportSkill(cw, frame)
	case proto.OpExportSkill:
		s.handleExportSkill(cw, frame)
	case proto.OpValidateSkill:
		s.handleValidateSkill(cw, frame)
	case proto.OpGetCost:
		s.handleGetCost(cw, frame)
	default:
		s.writeError(cw, frame.ID, proto.ErrBadRequest, "unknown op: "+frame.Op)
	}
}

func (s *Server) requireManager(cw *connWriter, id string) bool {
	if s.manager == nil {
		s.writeError(cw, id, proto.ErrUnavailable, "session manager not available")
		return false
	}
	return true
}

func (s *Server) handleCreateSession(cw *connWriter, frame proto.Frame) {
	if !s.requireManager(cw, frame.ID) {
		return
	}
	var req proto.CreateSessionRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	res, err := s.manager.Create(context.Background(), sm.CreateRequest{
		Name:          req.Name,
		MCPs:          req.MCPs,
		ExcludeMCPs:   req.ExcludeMCPs,
		Repos:         req.Repos,
		Model:         req.Model,
		MemLimitBytes: req.MemLimitBytes,
		CPULimitCores: req.CPULimitCores,
	})
	if err != nil {
		s.writeError(cw, frame.ID, proto.ErrInternal, err.Error())
		return
	}
	resp := proto.CreateSessionResponse{
		SessionID: res.SessionID,
		Status:    res.Status,
		Attach:    proto.AttachPointer{StreamOp: proto.OpAttachStream},
		Session:   res.Summary,
	}
	s.writeResponse(cw, frame.ID, resp)
}

func (s *Server) handleListSessions(cw *connWriter, frame proto.Frame) {
	if !s.requireManager(cw, frame.ID) {
		return
	}
	list, err := s.manager.List(context.Background())
	if err != nil {
		s.writeError(cw, frame.ID, proto.ErrInternal, err.Error())
		return
	}
	s.populateRunningCosts(list)
	s.writeResponse(cw, frame.ID, proto.ListSessionsResponse{Sessions: list})
}

func (s *Server) populateRunningCosts(list []proto.SessionSummary) {
	if s.usage == nil || len(list) == 0 {
		return
	}
	type runningTotaller interface {
		RunningTotals(ctx context.Context, ids []string) (map[string]float64, error)
	}
	rt, ok := s.usage.(runningTotaller)
	if !ok {
		return
	}
	ids := make([]string, 0, len(list))
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	totals, err := rt.RunningTotals(context.Background(), ids)
	if err != nil {
		return
	}
	for i, sum := range list {
		if v, ok := totals[sum.ID]; ok {
			c := v
			list[i].CostUSD = &c
		}
	}
}

func (s *Server) handleGetSession(cw *connWriter, frame proto.Frame) {
	if !s.requireManager(cw, frame.ID) {
		return
	}
	var req proto.GetSessionRequest
	_ = json.Unmarshal(frame.Data, &req)
	d, err := s.manager.Get(context.Background(), req.SessionID)
	if err != nil {
		s.writeError(cw, frame.ID, proto.ErrNotFound, err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.GetSessionResponse{Session: d})
}

func (s *Server) handleSendMessage(cw *connWriter, frame proto.Frame) {
	if !s.requireManager(cw, frame.ID) {
		return
	}
	var req proto.SendMessageRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	res, err := s.manager.Send(context.Background(), sm.SendRequest{
		SessionID: req.SessionID, Content: req.Content, ClientID: req.ClientID, IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		s.writeError(cw, frame.ID, mapSMError(err), err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.SendMessageResponse{
		MessageID: res.MessageID, Queued: res.Queued, QueueDepth: res.QueueDepth, Idempotent: res.Idempotent,
	})
}

func (s *Server) handleInterrupt(cw *connWriter, frame proto.Frame) {
	if !s.requireManager(cw, frame.ID) {
		return
	}
	var req proto.InterruptRequest
	_ = json.Unmarshal(frame.Data, &req)
	res, err := s.manager.Interrupt(context.Background(), req.SessionID, req.ClearQueue)
	if err != nil {
		if errors.Is(err, sm.ErrNoInFlight) {
			s.writeError(cw, frame.ID, proto.ErrPreconditionFailed, err.Error())
			return
		}
		s.writeError(cw, frame.ID, mapSMError(err), err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.InterruptResponse{
		Interrupted: res.Interrupted, ClearedQueueDepth: res.ClearedQueueDepth,
	})
}

func (s *Server) handleRestart(cw *connWriter, frame proto.Frame) {
	if !s.requireManager(cw, frame.ID) {
		return
	}
	var req proto.RestartSessionRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	if req.SessionID == "" {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, "session_id required")
		return
	}
	res, err := s.manager.Restart(context.Background(), req.SessionID)
	if err != nil {
		s.writeError(cw, frame.ID, mapSMError(err), err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.RestartSessionResponse{
		SessionID: res.SessionID, Status: res.Status, ImageID: res.ImageID,
	})
}

func (s *Server) handleTerminate(cw *connWriter, frame proto.Frame) {
	if !s.requireManager(cw, frame.ID) {
		return
	}
	var req proto.TerminateSessionRequest
	_ = json.Unmarshal(frame.Data, &req)
	if err := s.manager.Terminate(context.Background(), req.SessionID); err != nil {
		s.writeError(cw, frame.ID, mapSMError(err), err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.TerminateSessionResponse{SessionID: req.SessionID, Status: "terminated"})
}

func (s *Server) handleAttachStream(cw *connWriter, frame proto.Frame) {
	if !s.requireManager(cw, frame.ID) {
		return
	}
	var req proto.AttachStreamRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	stream, err := s.manager.Attach(context.Background(), req.SessionID)
	if err != nil {
		s.writeError(cw, frame.ID, mapSMError(err), err.Error())
		return
	}
	defer stream.Close()
	for {
		ev, ok, reason := stream.Recv()
		if !ok {
			s.writeStreamEnd(cw, frame.ID, reason)
			return
		}
		body, _ := json.Marshal(ev)
		out, _ := json.Marshal(proto.Frame{V: proto.ProtocolVersion, ID: frame.ID, Kind: proto.KindStreamChunk, Data: body})
		if err := cw.write(out); err != nil {
			return
		}
	}
}

func (s *Server) handleGetLogs(cw *connWriter, frame proto.Frame) {
	if s.logStream == nil {
		s.writeError(cw, frame.ID, proto.ErrUnavailable, "log streaming not configured")
		return
	}
	var req proto.GetLogsRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	send := func(line []byte) error {
		body, _ := json.Marshal(proto.LogLineData{Raw: string(line)})
		out, _ := json.Marshal(proto.Frame{V: proto.ProtocolVersion, ID: frame.ID, Kind: proto.KindStreamChunk, Data: body})
		return cw.write(out)
	}
	if err := s.logStream.Stream(context.Background(), req.SessionID, req.Follow, send); err != nil {
		s.writeError(cw, frame.ID, proto.ErrInternal, err.Error())
		return
	}
	s.writeStreamEnd(cw, frame.ID, "eof")
}

func (s *Server) handleGetContainerLogs(cw *connWriter, frame proto.Frame) {
	if s.containerLogs == nil {
		s.writeError(cw, frame.ID, proto.ErrUnavailable, "container log streaming not configured")
		return
	}
	var req proto.GetContainerLogsRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	if req.SessionID == "" {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, "session_id required")
		return
	}
	send := func(line []byte) error {
		body, _ := json.Marshal(proto.LogLineData{Raw: string(line)})
		out, _ := json.Marshal(proto.Frame{V: proto.ProtocolVersion, ID: frame.ID, Kind: proto.KindStreamChunk, Data: body})
		return cw.write(out)
	}
	if err := s.containerLogs.Stream(context.Background(), req.SessionID, req.Follow, send); err != nil {
		s.writeError(cw, frame.ID, proto.ErrInternal, err.Error())
		return
	}
	s.writeStreamEnd(cw, frame.ID, "eof")
}

func (s *Server) writeResponse(cw *connWriter, id string, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		s.writeError(cw, id, proto.ErrInternal, err.Error())
		return
	}
	frame := proto.Frame{V: proto.ProtocolVersion, ID: id, Kind: proto.KindResponse, Data: body}
	out, err := json.Marshal(frame)
	if err != nil {
		s.writeError(cw, id, proto.ErrInternal, err.Error())
		return
	}
	if err := cw.write(out); err != nil {
		s.logger.Debug("sock.write_failed", slog.String("error", err.Error()))
	}
}

func (s *Server) writeError(cw *connWriter, id, code, msg string) {
	body, _ := json.Marshal(proto.ErrorData{Code: code, Message: msg})
	frame := proto.Frame{V: proto.ProtocolVersion, ID: id, Kind: proto.KindError, Data: body}
	out, _ := json.Marshal(frame)
	_ = cw.write(out)
}

func (s *Server) writeStreamEnd(cw *connWriter, id, reason string) {
	body, _ := json.Marshal(map[string]string{"reason": reason})
	frame := proto.Frame{V: proto.ProtocolVersion, ID: id, Kind: proto.KindStreamEnd, Data: body}
	out, _ := json.Marshal(frame)
	_ = cw.write(out)
}

func mapSMError(err error) string {
	switch {
	case errors.Is(err, sm.ErrSessionNotFound):
		return proto.ErrNotFound
	case errors.Is(err, sm.ErrNoInFlight):
		return proto.ErrPreconditionFailed
	case errors.Is(err, sm.ErrSnapshotFailed):
		return proto.ErrSnapshotFailed
	default:
		return proto.ErrInternal
	}
}
