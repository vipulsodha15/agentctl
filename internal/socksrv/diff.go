package socksrv

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
)

func (s *Server) handleDiff(cw *connWriter, frame proto.Frame, patch bool) {
	if !s.requireManager(cw, frame.ID) {
		return
	}
	var req proto.DiffRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	if req.SessionID == "" {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, "session_id required")
		return
	}
	smReq := sm.DiffRequest{Repo: req.Repo, Format: req.Format}
	var (
		stream sm.DiffStream
		err    error
	)
	if patch {
		stream, err = s.manager.ExportPatch(context.Background(), req.SessionID, smReq)
	} else {
		stream, err = s.manager.Diff(context.Background(), req.SessionID, smReq)
	}
	if err != nil {
		s.writeError(cw, frame.ID, mapDiffError(err), err.Error())
		return
	}
	defer func() { _ = stream.Close() }()
	for {
		ch, rerr := stream.Recv()
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				s.writeStreamEnd(cw, frame.ID, "eof")
				return
			}
			s.writeError(cw, frame.ID, proto.ErrInternal, rerr.Error())
			return
		}
		body, _ := json.Marshal(proto.DiffChunkData{
			Repo:     ch.Repo,
			Data:     ch.Data,
			End:      ch.End,
			ExitCode: ch.ExitCode,
			BaseSHA:  ch.BaseSHA,
			Branch:   ch.Branch,
			Note:     ch.Note,
			Error:    ch.ErrorMsg,
		})
		out, _ := json.Marshal(proto.Frame{V: proto.ProtocolVersion, ID: frame.ID, Kind: proto.KindStreamChunk, Data: body})
		if werr := cw.write(out); werr != nil {
			return
		}
	}
}

func (s *Server) handleExportPush(cw *connWriter, frame proto.Frame) {
	if !s.requireManager(cw, frame.ID) {
		return
	}
	var req proto.ExportPushRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	if req.SessionID == "" || req.Branch == "" {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, "session_id and branch required")
		return
	}
	res, err := s.manager.ExportPush(context.Background(), req.SessionID, sm.PushRequest{
		Repo: req.Repo, Branch: req.Branch, Message: req.Message,
	})
	if err != nil {
		s.writeError(cw, frame.ID, mapDiffError(err), err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.ExportPushResponse{
		Success: res.Success, Repo: res.Repo, Branch: res.Branch,
		Output: res.Output, Error: res.Error,
	})
}

func (s *Server) handleListSessionRepos(cw *connWriter, frame proto.Frame) {
	if !s.requireManager(cw, frame.ID) {
		return
	}
	var req proto.ListSessionReposRequest
	_ = json.Unmarshal(frame.Data, &req)
	if req.SessionID == "" {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, "session_id required")
		return
	}
	repos, err := s.manager.SessionRepos(context.Background(), req.SessionID)
	if err != nil {
		s.writeError(cw, frame.ID, mapDiffError(err), err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.ListSessionReposResponse{Repos: repos})
}

func mapDiffError(err error) string {
	switch {
	case errors.Is(err, sm.ErrSessionNotFound):
		return proto.ErrNotFound
	case errors.Is(err, sm.ErrDiffUnavailable):
		return proto.ErrUnavailable
	default:
		return proto.ErrInternal
	}
}
