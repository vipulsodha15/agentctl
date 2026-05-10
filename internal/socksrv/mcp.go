package socksrv

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/skills"
)

func entryToProto(e mcp.Entry) proto.MCPEntry {
	return proto.MCPEntry{
		Name:           e.Name,
		URL:            e.URL,
		Transport:      e.Transport,
		Kind:           e.Kind,
		AuthConfigJSON: e.AuthConfigJSON,
		DefaultEnabled: e.DefaultEnabled,
		Description:    e.Description,
		CreatedAt:      e.CreatedAt,
		UpdatedAt:      e.UpdatedAt,
	}
}

func mapMCPError(err error) string {
	switch {
	case errors.Is(err, mcp.ErrNotFound):
		return proto.ErrNotFound
	case errors.Is(err, mcp.ErrConflict):
		return proto.ErrConflict
	default:
		return proto.ErrInternal
	}
}

func (s *Server) requireMCP(cw *connWriter, id string) bool {
	if s.mcps == nil {
		s.writeError(cw, id, proto.ErrUnavailable, "mcp registry not configured")
		return false
	}
	return true
}

func (s *Server) handleListMCPs(cw *connWriter, frame proto.Frame) {
	if !s.requireMCP(cw, frame.ID) {
		return
	}
	list, err := s.mcps.List(context.Background())
	if err != nil {
		s.writeError(cw, frame.ID, proto.ErrInternal, err.Error())
		return
	}
	out := make([]proto.MCPEntry, 0, len(list))
	for _, e := range list {
		out = append(out, entryToProto(e))
	}
	s.writeResponse(cw, frame.ID, proto.ListMCPsResponse{MCPs: out})
}

func (s *Server) handleAddMCP(cw *connWriter, frame proto.Frame) {
	if !s.requireMCP(cw, frame.ID) {
		return
	}
	var req proto.AddMCPRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	entry := mcp.Entry{
		Name:           req.Name,
		URL:            req.URL,
		Transport:      req.Transport,
		Kind:           req.Kind,
		AuthConfigJSON: req.AuthConfigJSON,
		DefaultEnabled: req.DefaultEnabled,
		Description:    req.Description,
	}
	if err := s.mcps.Add(context.Background(), entry); err != nil {
		s.writeError(cw, frame.ID, mapMCPError(err), err.Error())
		return
	}
	got, _ := s.mcps.Get(context.Background(), req.Name)
	s.writeResponse(cw, frame.ID, proto.AddMCPResponse{MCP: entryToProto(got)})
}

func (s *Server) handleUpdateMCP(cw *connWriter, frame proto.Frame) {
	if !s.requireMCP(cw, frame.ID) {
		return
	}
	var req proto.UpdateMCPRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	upd := mcp.EntryUpdate{
		URL: req.URL, Transport: req.Transport, Kind: req.Kind,
		AuthConfigJSON: req.AuthConfigJSON, DefaultEnabled: req.DefaultEnabled,
		Description: req.Description,
	}
	if err := s.mcps.Update(context.Background(), req.Name, upd); err != nil {
		s.writeError(cw, frame.ID, mapMCPError(err), err.Error())
		return
	}
	got, _ := s.mcps.Get(context.Background(), req.Name)
	s.writeResponse(cw, frame.ID, proto.UpdateMCPResponse{MCP: entryToProto(got)})
}

func (s *Server) handleRemoveMCP(cw *connWriter, frame proto.Frame) {
	if !s.requireMCP(cw, frame.ID) {
		return
	}
	var req proto.RemoveMCPRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	if err := s.mcps.Remove(context.Background(), req.Name, req.Force); err != nil {
		s.writeError(cw, frame.ID, mapMCPError(err), err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.RemoveMCPResponse{Removed: true})
}

func (s *Server) handleSetDefaultMCP(cw *connWriter, frame proto.Frame) {
	if !s.requireMCP(cw, frame.ID) {
		return
	}
	var req proto.SetDefaultMCPRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	if err := s.mcps.SetDefault(context.Background(), req.Name, req.DefaultEnabled); err != nil {
		s.writeError(cw, frame.ID, mapMCPError(err), err.Error())
		return
	}
	got, _ := s.mcps.Get(context.Background(), req.Name)
	s.writeResponse(cw, frame.ID, proto.SetDefaultMCPResponse{MCP: entryToProto(got)})
}

func (s *Server) requireSkills(cw *connWriter, id string) bool {
	if s.skills == nil {
		s.writeError(cw, id, proto.ErrUnavailable, "skill manager not configured")
		return false
	}
	return true
}

func skillToProto(s skills.InstalledSkill) proto.SkillEntry {
	return proto.SkillEntry{
		Name:        s.Name,
		Description: s.Description,
		Source:      s.Source,
		Path:        s.Path,
		Overrides:   s.Overrides,
	}
}

func (s *Server) handleListInstalledSkills(cw *connWriter, frame proto.Frame) {
	if !s.requireSkills(cw, frame.ID) {
		return
	}
	list, err := s.skills.ListInstalled()
	if err != nil {
		s.writeError(cw, frame.ID, proto.ErrInternal, err.Error())
		return
	}
	out := make([]proto.SkillEntry, 0, len(list))
	for _, sk := range list {
		out = append(out, skillToProto(sk))
	}
	s.writeResponse(cw, frame.ID, proto.ListInstalledSkillsResponse{Skills: out})
}

func (s *Server) handleAddSkill(cw *connWriter, frame proto.Frame) {
	if !s.requireSkills(cw, frame.ID) {
		return
	}
	var req proto.AddSkillRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	res, err := s.skills.Add(skills.AddSource{Path: req.Path}, skills.ImportOptions{Force: req.Force})
	if err != nil {
		s.writeError(cw, frame.ID, mapSkillError(err), err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.AddSkillResponse{Name: res.Name, Path: res.Path})
}

func (s *Server) handleRemoveSkill(cw *connWriter, frame proto.Frame) {
	if !s.requireSkills(cw, frame.ID) {
		return
	}
	var req proto.RemoveSkillRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	if err := s.skills.Remove(req.Name); err != nil {
		s.writeError(cw, frame.ID, mapSkillError(err), err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.RemoveSkillResponse{Removed: true})
}

func (s *Server) handleImportSkill(cw *connWriter, frame proto.Frame) {
	if !s.requireSkills(cw, frame.ID) {
		return
	}
	var req proto.ImportSkillRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	res, err := s.skills.Import(req.SourcePath, req.Name, skills.ImportOptions{Force: req.Force, DryRun: req.DryRun})
	if err != nil {
		s.writeError(cw, frame.ID, mapSkillError(err), err.Error())
		return
	}
	skipped := make([]string, 0, len(res.Skipped))
	reasons := make([]string, 0, len(res.Skipped))
	for _, sk := range res.Skipped {
		skipped = append(skipped, sk.Name)
		reasons = append(reasons, sk.Reason)
	}
	s.writeResponse(cw, frame.ID, proto.ImportSkillResponse{
		Imported: res.Imported, Skipped: skipped, SkippedReasons: reasons, Shadowed: res.ShadowedBuiltins,
	})
}

func (s *Server) handleExportSkill(cw *connWriter, frame proto.Frame) {
	if !s.requireSkills(cw, frame.ID) {
		return
	}
	var req proto.ExportSkillRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	body, err := s.skills.Export(req.Name)
	if err != nil {
		s.writeError(cw, frame.ID, mapSkillError(err), err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.ExportSkillResponse{Name: req.Name, Tarball: body})
}

func (s *Server) handleValidateSkill(cw *connWriter, frame proto.Frame) {
	if !s.requireSkills(cw, frame.ID) {
		return
	}
	var req proto.ValidateSkillRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	res, err := s.skills.Validate(skills.ValidateSource{Name: req.Name, Path: req.Path})
	if err != nil {
		s.writeError(cw, frame.ID, proto.ErrInternal, err.Error())
		return
	}
	s.writeResponse(cw, frame.ID, proto.ValidateSkillResponse{
		Name:        res.Name,
		Description: res.Description,
		OK:          res.OK,
		Issues:      res.Issues,
	})
}

func mapSkillError(err error) string {
	switch {
	case errors.Is(err, skills.ErrNotFound):
		return proto.ErrNotFound
	case errors.Is(err, skills.ErrAlreadyExists):
		return proto.ErrConflict
	case errors.Is(err, skills.ErrBuiltinReadOnly):
		return proto.ErrPreconditionFailed
	case errors.Is(err, skills.ErrInvalidName):
		return proto.ErrBadRequest
	default:
		return proto.ErrInternal
	}
}
