package fan

import (
	"sync"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/proto"
)

func TestSubscribeBroadcastUnsubscribe(t *testing.T) {
	h := NewHub()
	s, cancel, err := h.Subscribe("sess_1")
	if err != nil {
		t.Fatal(err)
	}
	go h.Broadcast("sess_1", proto.Event{EventID: "e1", Kind: "turn.start"})
	ev, ok, _ := s.Recv()
	if !ok || ev.Kind != "turn.start" {
		t.Fatalf("got ev=%+v ok=%v", ev, ok)
	}
	cancel()
	_, ok, reason := s.Recv()
	if ok {
		t.Fatalf("expected stream end after cancel")
	}
	if reason != StreamEndClientDisconnected {
		t.Fatalf("reason=%q want=%q", reason, StreamEndClientDisconnected)
	}
}

func TestSlowConsumerEviction(t *testing.T) {
	h := NewHub()
	s, _, err := h.Subscribe("sess_1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < SubscriberBuffer+10; i++ {
		h.Broadcast("sess_1", proto.Event{EventID: "x", Kind: "assistant.delta"})
	}
	deadline := time.After(2 * time.Second)
	drained := 0
	for {
		select {
		case <-deadline:
			t.Fatalf("expected stream end with slow_consumer")
		default:
		}
		_, ok, reason := s.Recv()
		if !ok {
			if reason != StreamEndSlowConsumer {
				t.Fatalf("reason=%q want=%q", reason, StreamEndSlowConsumer)
			}
			return
		}
		drained++
		if drained > SubscriberBuffer+50 {
			t.Fatalf("never received slow_consumer signal")
		}
	}
}

func TestCloseDuringBroadcast(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		s, _, _ := h.Subscribe("sess_x")
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, ok, _ := s.Recv()
				if !ok {
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.Broadcast("sess_x", proto.Event{EventID: "n", Kind: "assistant.delta"})
		}
		close(done)
	}()
	time.Sleep(5 * time.Millisecond)
	h.Close("sess_x", StreamEndSessionTerminated)
	<-done
	wg.Wait()
}

func TestMultipleSubscribersReceiveSameEvent(t *testing.T) {
	h := NewHub()
	s1, _, _ := h.Subscribe("s")
	s2, _, _ := h.Subscribe("s")
	h.Broadcast("s", proto.Event{EventID: "e", Kind: "turn.end"})
	for _, s := range []Stream{s1, s2} {
		ev, ok, _ := s.Recv()
		if !ok || ev.Kind != "turn.end" {
			t.Fatalf("got ev=%+v ok=%v", ev, ok)
		}
	}
}

func TestBroadcastUnknownSessionIsNoop(t *testing.T) {
	h := NewHub()
	h.Broadcast("missing", proto.Event{EventID: "e"})
}
