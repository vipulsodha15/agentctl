package usage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

const timelineCap = 5000

func (s *Service) PerSession(ctx context.Context, sessionID string) (PerSessionTotals, error) {
	out := PerSessionTotals{SessionID: sessionID}
	if s.store == nil {
		return out, ErrNoStore
	}
	rows, err := s.store.DB().QueryContext(ctx,
		`SELECT turn_id, at, model,
                input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
                cost_usd
         FROM usage WHERE session_id = ? ORDER BY id ASC LIMIT ?`,
		sessionID, timelineCap,
	)
	if err != nil {
		return out, fmt.Errorf("usage per-session: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byModel := map[string]*ModelTotals{}
	for rows.Next() {
		var (
			turnID, atStr, model string
			in, outT, cr, cw     int64
			costN                sql.NullFloat64
		)
		if err := rows.Scan(&turnID, &atStr, &model,
			&in, &outT, &cr, &cw, &costN); err != nil {
			return out, err
		}
		t, _ := time.Parse(time.RFC3339Nano, atStr)
		row := TurnRow{
			TurnID:           turnID,
			At:               t,
			Model:            model,
			InputTokens:      in,
			OutputTokens:     outT,
			CacheReadTokens:  cr,
			CacheWriteTokens: cw,
		}
		if costN.Valid {
			c := costN.Float64
			row.CostUSD = &c
			out.CostUSD += c
		} else {
			out.HasUnknown = true
		}
		out.Timeline = append(out.Timeline, row)
		out.Turns++
		out.InputTokens += in
		out.OutputTokens += outT
		out.CacheReadTokens += cr
		out.CacheWriteTokens += cw

		mt, ok := byModel[model]
		if !ok {
			mt = &ModelTotals{Model: model}
			byModel[model] = mt
		}
		mt.Turns++
		mt.InputTokens += in
		mt.OutputTokens += outT
		mt.CacheReadTokens += cr
		mt.CacheWriteTokens += cw
		if costN.Valid {
			mt.CostUSD += costN.Float64
		} else {
			mt.HasUnknown = true
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, m := range byModel {
		out.ByModel = append(out.ByModel, *m)
	}
	sort.Slice(out.ByModel, func(i, j int) bool { return out.ByModel[i].Model < out.ByModel[j].Model })
	return out, nil
}

func (s *Service) Range(ctx context.Context, start, end time.Time, sessionFilter string) (RangeTotals, error) {
	out := RangeTotals{Start: start, End: end}
	if s.store == nil {
		return out, ErrNoStore
	}
	args := []any{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	q := `SELECT u.session_id, COALESCE(s.name, ''), COALESCE(s.status, ''),
                 COUNT(*) AS turns,
                 COALESCE(SUM(u.input_tokens),0),
                 COALESCE(SUM(u.output_tokens),0),
                 COALESCE(SUM(u.cost_usd),0),
                 SUM(CASE WHEN u.cost_usd IS NULL THEN 1 ELSE 0 END)
          FROM usage u LEFT JOIN sessions s ON s.id = u.session_id
          WHERE u.at >= ? AND u.at < ?`
	if sessionFilter != "" {
		q += ` AND u.session_id = ?`
		args = append(args, sessionFilter)
	}
	q += ` GROUP BY u.session_id, s.name, s.status ORDER BY SUM(u.cost_usd) DESC`

	rows, err := s.store.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return out, fmt.Errorf("usage range: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			sid, name, status   string
			turns, unknownTurns int
			in, outT            int64
			cost                float64
		)
		if err := rows.Scan(&sid, &name, &status, &turns, &in, &outT, &cost, &unknownTurns); err != nil {
			return out, err
		}
		st := SessionTotals{
			SessionID:    sid,
			Name:         name,
			Status:       status,
			Turns:        turns,
			InputTokens:  in,
			OutputTokens: outT,
			CostUSD:      cost,
			HasUnknown:   unknownTurns > 0,
		}
		out.BySession = append(out.BySession, st)
		out.Turns += turns
		out.InputTokens += in
		out.OutputTokens += outT
		out.CostUSD += cost
		if unknownTurns > 0 {
			out.HasUnknown = true
		}
	}
	return out, rows.Err()
}

func (s *Service) RunningTotals(ctx context.Context, sessionIDs []string) (map[string]float64, error) {
	totals := make(map[string]float64, len(sessionIDs))
	if s.store == nil || len(sessionIDs) == 0 {
		return totals, nil
	}
	rows, err := s.store.DB().QueryContext(ctx,
		`SELECT session_id, COALESCE(SUM(cost_usd),0) FROM usage GROUP BY session_id`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	all := map[string]float64{}
	for rows.Next() {
		var sid string
		var cost float64
		if err := rows.Scan(&sid, &cost); err != nil {
			return nil, err
		}
		all[sid] = cost
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, sid := range sessionIDs {
		if v, ok := all[sid]; ok {
			totals[sid] = v
		}
	}
	return totals, nil
}
