package socksrv

import (
	"context"
	"encoding/json"
	"time"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/usage"
)

func (s *Server) handleGetCost(cw *connWriter, frame proto.Frame) {
	if s.usage == nil {
		s.writeError(cw, frame.ID, proto.ErrUnavailable, "cost reporting unavailable")
		return
	}
	var req proto.GetCostRequest
	if len(frame.Data) > 0 {
		if err := json.Unmarshal(frame.Data, &req); err != nil {
			s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
			return
		}
	}
	ctx := context.Background()
	resp := proto.GetCostResponse{}

	if req.SessionID != "" && req.Since == "" {
		per, err := s.usage.PerSession(ctx, req.SessionID)
		if err != nil {
			s.writeError(cw, frame.ID, proto.ErrInternal, err.Error())
			return
		}
		resp.PerSession = perSessionToProto(per)
		s.writeResponse(cw, frame.ID, resp)
		return
	}
	now := time.Now().UTC()
	start, end, err := usage.ParseRange(req.Since, now)
	if err != nil {
		s.writeError(cw, frame.ID, proto.ErrBadRequest, err.Error())
		return
	}
	rng, err := s.usage.Range(ctx, start, end, req.SessionID)
	if err != nil {
		s.writeError(cw, frame.ID, proto.ErrInternal, err.Error())
		return
	}
	resp.Range = rangeToProto(rng)
	s.writeResponse(cw, frame.ID, resp)
}

func perSessionToProto(p usage.PerSessionTotals) *proto.SessionCostTotals {
	out := &proto.SessionCostTotals{
		SessionID:        p.SessionID,
		Turns:            p.Turns,
		InputTokens:      p.InputTokens,
		OutputTokens:     p.OutputTokens,
		CacheReadTokens:  p.CacheReadTokens,
		CacheWriteTokens: p.CacheWriteTokens,
		CostUSD:          p.CostUSD,
		HasUnknown:       p.HasUnknown,
	}
	for _, m := range p.ByModel {
		out.ByModel = append(out.ByModel, proto.CostModelTotals{
			Model:            m.Model,
			Turns:            m.Turns,
			InputTokens:      m.InputTokens,
			OutputTokens:     m.OutputTokens,
			CacheReadTokens:  m.CacheReadTokens,
			CacheWriteTokens: m.CacheWriteTokens,
			CostUSD:          m.CostUSD,
			HasUnknown:       m.HasUnknown,
		})
	}
	for _, t := range p.Timeline {
		row := proto.CostTurnRow{
			TurnID:           t.TurnID,
			At:               t.At,
			Model:            t.Model,
			InputTokens:      t.InputTokens,
			OutputTokens:     t.OutputTokens,
			CacheReadTokens:  t.CacheReadTokens,
			CacheWriteTokens: t.CacheWriteTokens,
			CostUSD:          t.CostUSD,
		}
		out.Timeline = append(out.Timeline, row)
	}
	return out
}

func rangeToProto(r usage.RangeTotals) *proto.RangeCostTotals {
	out := &proto.RangeCostTotals{
		Start:        r.Start,
		End:          r.End,
		Turns:        r.Turns,
		InputTokens:  r.InputTokens,
		OutputTokens: r.OutputTokens,
		CostUSD:      r.CostUSD,
		HasUnknown:   r.HasUnknown,
	}
	for _, s := range r.BySession {
		out.BySession = append(out.BySession, proto.RangeSessionTotals{
			SessionID:    s.SessionID,
			Name:         s.Name,
			Status:       s.Status,
			Turns:        s.Turns,
			InputTokens:  s.InputTokens,
			OutputTokens: s.OutputTokens,
			CostUSD:      s.CostUSD,
			HasUnknown:   s.HasUnknown,
		})
	}
	return out
}
