package cc

import (
	"sync"
	"time"
)

// Per agentd.md §4 / api.md §4.5:
// - 100 frames/s sustained, 200 burst
// - Sustained 10s overage activates throttle (drops only assistant.delta)
// - Falling under for >5s deactivates
// - Sustained 60s overage marks the session error-fatal
const (
	rateCapPerSec        = 100
	rateBurst            = 200
	throttleActivateMs   = 10_000
	throttleDeactivateMs = 5_000
	throttleFatalMs      = 60_000
)

type throttleState int

const (
	throttleIdle   throttleState = iota
	throttleOver                 // over the limit; not yet activated
	throttleActive               // dropping deltas
)

// ThrottleEvent is emitted to the actor as a synthetic frame.
type ThrottleEvent struct {
	Active bool
	Fatal  bool
}

// rateMeter tracks frames within the last 1s in a fixed-size circular buffer.
// When the count exceeds rateCapPerSec for >activateAfter we activate; when it
// falls under rateCapPerSec for >deactivateAfter we deactivate.
type rateMeter struct {
	mu           sync.Mutex
	now          func() time.Time
	timestamps   []time.Time
	head         int
	count        int
	state        throttleState
	overSince    time.Time
	underSince   time.Time
	activateMs   int
	deactivateMs int
	fatalMs      int
	cap          int
}

func newRateMeter(now func() time.Time) *rateMeter {
	if now == nil {
		now = time.Now
	}
	return &rateMeter{
		now:          now,
		timestamps:   make([]time.Time, rateBurst*2),
		activateMs:   throttleActivateMs,
		deactivateMs: throttleDeactivateMs,
		fatalMs:      throttleFatalMs,
		cap:          rateCapPerSec,
	}
}

// observe records that one inbound frame has arrived at time t. It returns:
//   - drop: whether this frame should be dropped (only meaningful when the
//     caller has already verified it is an assistant.delta event).
//   - transition: a non-nil ThrottleEvent if the active/fatal state changed,
//     else nil.
func (m *rateMeter) observe() (drop bool, transition *ThrottleEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.appendTimestamp(now)
	rate := m.ratePerSecondLocked(now)

	switch m.state {
	case throttleIdle:
		if rate > m.cap {
			m.state = throttleOver
			m.overSince = now
		}
	case throttleOver:
		if rate <= m.cap {
			m.state = throttleIdle
			m.overSince = time.Time{}
		} else if now.Sub(m.overSince) >= time.Duration(m.activateMs)*time.Millisecond {
			m.state = throttleActive
			m.underSince = time.Time{}
			drop = true
			transition = &ThrottleEvent{Active: true}
		}
	case throttleActive:
		if rate <= m.cap {
			if m.underSince.IsZero() {
				m.underSince = now
			}
			if now.Sub(m.underSince) >= time.Duration(m.deactivateMs)*time.Millisecond {
				m.state = throttleIdle
				m.overSince = time.Time{}
				m.underSince = time.Time{}
				transition = &ThrottleEvent{Active: false}
				return
			}
			drop = true
			return
		}
		m.underSince = time.Time{}
		drop = true
		if !m.overSince.IsZero() && now.Sub(m.overSince) >= time.Duration(m.fatalMs)*time.Millisecond {
			transition = &ThrottleEvent{Active: true, Fatal: true}
		}
	}
	return
}

func (m *rateMeter) appendTimestamp(t time.Time) {
	if m.count == len(m.timestamps) {
		m.head = (m.head + 1) % len(m.timestamps)
		m.timestamps[(m.head+m.count-1)%len(m.timestamps)] = t
		return
	}
	m.timestamps[(m.head+m.count)%len(m.timestamps)] = t
	m.count++
}

func (m *rateMeter) ratePerSecondLocked(now time.Time) int {
	cutoff := now.Add(-time.Second)
	for m.count > 0 {
		if m.timestamps[m.head].After(cutoff) {
			break
		}
		m.head = (m.head + 1) % len(m.timestamps)
		m.count--
	}
	return m.count
}

// IsActive reports whether the meter is currently throttling (testing only).
func (m *rateMeter) IsActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state == throttleActive
}
