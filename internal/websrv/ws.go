package websrv

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
)

const (
	wsPingInterval = 20 * time.Second
	wsPongWait     = 60 * time.Second
	wsWriteWait    = 10 * time.Second
)

func (s *Server) handleAttachStream(w http.ResponseWriter, r *http.Request, sessionID string) {
	if !s.requireManager(w) {
		return
	}
	if !s.originAllowed(r) {
		writeError(w, http.StatusForbidden, proto.ErrForbidden, "origin mismatch")
		return
	}
	requested := websocket.Subprotocols(r)
	if !containsString(requested, WSSubprotocol) {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, "missing subprotocol "+WSSubprotocol)
		return
	}
	upgrader := &websocket.Upgrader{
		Subprotocols:    []string{WSSubprotocol},
		CheckOrigin:     func(req *http.Request) bool { return s.originAllowed(req) },
		ReadBufferSize:  4096,
		WriteBufferSize: 16 << 10,
	}
	conn, err := upgrader.Upgrade(w, r, http.Header{})
	if err != nil {
		s.logger.Debug("web.ws_upgrade_failed", slog.String("error", err.Error()))
		return
	}
	defer func() { _ = conn.Close() }()

	stream, err := s.manager.Attach(r.Context(), sessionID)
	if err != nil {
		reason := "session_not_found"
		if errors.Is(err, sm.ErrSnapshotFailed) {
			reason = "snapshot_failed"
		}
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, reason),
			time.Now().Add(wsWriteWait))
		return
	}
	defer stream.Close()

	conn.SetReadLimit(1 << 16)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})

	// Drain client frames so the connection stays alive and pong handlers
	// fire; we never act on what the client sends (api.md §3.5).
	go func() {
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	pingTicker := time.NewTicker(wsPingInterval)
	defer pingTicker.Stop()

	type item struct {
		ev     proto.Event
		ok     bool
		reason string
	}
	items := make(chan item, 8)
	go func() {
		for {
			ev, ok, reason := stream.Recv()
			items <- item{ev: ev, ok: ok, reason: reason}
			if !ok {
				close(items)
				return
			}
		}
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-pingTicker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case it, open := <-items:
			if !open {
				return
			}
			if !it.ok {
				endBody, _ := json.Marshal(map[string]string{"reason": it.reason})
				frame := proto.Frame{
					V:    proto.ProtocolVersion,
					Kind: proto.KindStreamEnd,
					Data: endBody,
				}
				out, _ := json.Marshal(frame)
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
				_ = conn.WriteMessage(websocket.TextMessage, out)
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, it.reason),
					time.Now().Add(wsWriteWait))
				return
			}
			body, _ := json.Marshal(it.ev)
			frame := proto.Frame{
				V:    proto.ProtocolVersion,
				ID:   it.ev.EventID,
				Kind: proto.KindEvent,
				Data: body,
			}
			out, _ := json.Marshal(frame)
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.TextMessage, out); err != nil {
				return
			}
		}
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
