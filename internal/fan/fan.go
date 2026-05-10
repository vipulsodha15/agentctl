package fan

import (
	"errors"
	"sync"

	"github.com/agentctl/agentctl/internal/proto"
)

const SubscriberBuffer = 64

const (
	StreamEndClientDisconnected = "client_disconnected"
	StreamEndSessionTerminated  = "session_terminated"
	StreamEndSlowConsumer       = "slow_consumer"
)

var ErrSlowConsumer = errors.New("subscriber dropped: slow consumer")

type Stream interface {
	Recv() (proto.Event, bool, string)
	Close()
}

type Hub interface {
	Subscribe(sessionID string) (Stream, func(), error)
	Broadcast(sessionID string, ev proto.Event)
	Close(sessionID string, reason string)
}

type subscriber struct {
	ch     chan proto.Event
	closed chan struct{}
	reason string
	once   sync.Once
}

func newSubscriber() *subscriber {
	return &subscriber{
		ch:     make(chan proto.Event, SubscriberBuffer),
		closed: make(chan struct{}),
	}
}

func (s *subscriber) terminate(reason string) {
	s.once.Do(func() {
		s.reason = reason
		close(s.closed)
	})
}

func (s *subscriber) Recv() (proto.Event, bool, string) {
	select {
	case ev, ok := <-s.ch:
		if !ok {
			return proto.Event{}, false, s.reason
		}
		return ev, true, ""
	case <-s.closed:
		select {
		case ev, ok := <-s.ch:
			if ok {
				return ev, true, ""
			}
		default:
		}
		return proto.Event{}, false, s.reason
	}
}

func (s *subscriber) Close() {
	s.terminate(StreamEndClientDisconnected)
}

type bus struct {
	subs []*subscriber
}

type hub struct {
	mu    sync.Mutex
	buses map[string]*bus
}

func NewHub() Hub {
	return &hub{buses: make(map[string]*bus)}
}

func (h *hub) Subscribe(sessionID string) (Stream, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.buses[sessionID]
	if b == nil {
		b = &bus{}
		h.buses[sessionID] = b
	}
	s := newSubscriber()
	b.subs = append(b.subs, s)
	cancel := func() {
		h.removeSub(sessionID, s, StreamEndClientDisconnected)
	}
	return s, cancel, nil
}

func (h *hub) Broadcast(sessionID string, ev proto.Event) {
	h.mu.Lock()
	b := h.buses[sessionID]
	if b == nil {
		h.mu.Unlock()
		return
	}
	subs := make([]*subscriber, len(b.subs))
	copy(subs, b.subs)
	h.mu.Unlock()

	var slow []*subscriber
	for _, s := range subs {
		select {
		case <-s.closed:
			continue
		default:
		}
		select {
		case s.ch <- ev:
		default:
			slow = append(slow, s)
		}
	}
	for _, s := range slow {
		h.removeSub(sessionID, s, StreamEndSlowConsumer)
	}
}

func (h *hub) Close(sessionID, reason string) {
	h.mu.Lock()
	b := h.buses[sessionID]
	if b == nil {
		h.mu.Unlock()
		return
	}
	subs := b.subs
	delete(h.buses, sessionID)
	h.mu.Unlock()
	for _, s := range subs {
		s.terminate(reason)
	}
}

func (h *hub) removeSub(sessionID string, s *subscriber, reason string) {
	h.mu.Lock()
	b := h.buses[sessionID]
	if b != nil {
		out := b.subs[:0]
		for _, e := range b.subs {
			if e != s {
				out = append(out, e)
			}
		}
		b.subs = out
		if len(b.subs) == 0 {
			delete(h.buses, sessionID)
		}
	}
	h.mu.Unlock()
	s.terminate(reason)
}
