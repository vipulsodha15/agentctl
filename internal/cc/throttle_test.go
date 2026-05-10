package cc

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestThrottleActivatesAfterTenSecondsOfOverload(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m := newRateMeter(clock.Now)

	// Pump 200 frames in the first second; meter goes "over" but not yet
	// active. Each second we keep doing that for 10s before activation fires.
	var transitions []ThrottleEvent
	var anyDrop bool
	for second := 0; second < 11; second++ {
		for i := 0; i < 200; i++ {
			drop, ev := m.observe()
			if drop {
				anyDrop = true
			}
			if ev != nil {
				transitions = append(transitions, *ev)
			}
			clock.Advance(time.Millisecond)
		}
		clock.Advance(800 * time.Millisecond)
	}

	if !anyDrop {
		t.Fatalf("expected drops once throttle activated")
	}
	if len(transitions) == 0 {
		t.Fatalf("expected an active=true transition; got none")
	}
	if !transitions[0].Active {
		t.Errorf("first transition should be Active=true, got %+v", transitions[0])
	}
	if transitions[0].Fatal {
		t.Errorf("first transition should not be fatal")
	}
}

func TestThrottleDoesNotActivateBelowLimit(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m := newRateMeter(clock.Now)

	// 50 frames/s for 30s — well under the 100/s cap.
	for second := 0; second < 30; second++ {
		for i := 0; i < 50; i++ {
			drop, ev := m.observe()
			if drop {
				t.Errorf("unexpected drop at second %d frame %d", second, i)
			}
			if ev != nil {
				t.Errorf("unexpected transition: %+v", ev)
			}
			clock.Advance(20 * time.Millisecond)
		}
	}
	if m.IsActive() {
		t.Errorf("meter activated under sustained low load")
	}
}

func TestThrottleDeactivatesAfterCooldown(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m := newRateMeter(clock.Now)

	// 11 seconds at 200 fr/s to activate.
	var hadActivate bool
	for second := 0; second < 11; second++ {
		for i := 0; i < 200; i++ {
			_, ev := m.observe()
			if ev != nil && ev.Active {
				hadActivate = true
			}
			clock.Advance(time.Millisecond)
		}
		clock.Advance(800 * time.Millisecond)
	}
	if !hadActivate {
		t.Fatalf("did not activate")
	}

	// Now go silent for 6s — under the limit.
	var deactivated bool
	for second := 0; second < 6; second++ {
		for i := 0; i < 10; i++ {
			_, ev := m.observe()
			if ev != nil && !ev.Active {
				deactivated = true
			}
			clock.Advance(100 * time.Millisecond)
		}
	}
	if !deactivated {
		t.Errorf("expected deactivation event after sustained silence")
	}
}

func TestThrottleFatalAfterSixtySeconds(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m := newRateMeter(clock.Now)

	var fatal bool
	for second := 0; second < 70; second++ {
		for i := 0; i < 200; i++ {
			_, ev := m.observe()
			if ev != nil && ev.Fatal {
				fatal = true
			}
			clock.Advance(time.Millisecond)
		}
		clock.Advance(800 * time.Millisecond)
		if fatal {
			break
		}
	}
	if !fatal {
		t.Errorf("expected fatal transition by 60s of sustained overload")
	}
}
