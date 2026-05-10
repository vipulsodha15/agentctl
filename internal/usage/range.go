package usage

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseRange resolves the spec to a [start, end) UTC range. Supported forms:
//
//   - `today`                 — midnight UTC of `now` to `now`.
//   - `Nd` (e.g. `7d`, `30d`) — `now - N*24h` to `now`.
//   - `Nh`                    — `now - N*1h` to `now`.
//   - `YYYY-MM-DD..YYYY-MM-DD` — explicit half-open day range, end exclusive
//     (so `2026-05-01..2026-05-02` covers May 1 only).
//   - `YYYY-MM-DD`            — single day (midnight to next midnight).
//
// `now` is the caller's reference time. An empty spec defaults to `7d`.
func ParseRange(spec string, now time.Time) (time.Time, time.Time, error) {
	now = now.UTC()
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return now.Add(-7 * 24 * time.Hour), now, nil
	}
	if spec == "today" {
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return start, now, nil
	}
	if i := strings.Index(spec, ".."); i >= 0 {
		s := strings.TrimSpace(spec[:i])
		e := strings.TrimSpace(spec[i+2:])
		if s == "" || e == "" {
			return time.Time{}, time.Time{}, fmt.Errorf("range %q: both endpoints required", spec)
		}
		st, err := time.Parse("2006-01-02", s)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("range %q: bad start: %w", spec, err)
		}
		en, err := time.Parse("2006-01-02", e)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("range %q: bad end: %w", spec, err)
		}
		return st.UTC(), en.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", spec); err == nil {
		return t.UTC(), t.UTC().Add(24 * time.Hour), nil
	}
	if d, err := parseShortDuration(spec); err == nil {
		return now.Add(-d), now, nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("range %q: unrecognized; expected `Nd`, `Nh`, `today`, `YYYY-MM-DD`, or `YYYY-MM-DD..YYYY-MM-DD`", spec)
}

func parseShortDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("duration too short")
	}
	suffix := s[len(s)-1]
	num, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || num < 0 {
		return 0, fmt.Errorf("not a duration")
	}
	switch suffix {
	case 'd':
		return time.Duration(num) * 24 * time.Hour, nil
	case 'h':
		return time.Duration(num) * time.Hour, nil
	case 'm':
		return time.Duration(num) * time.Minute, nil
	}
	return 0, fmt.Errorf("unknown unit %q", string(suffix))
}
