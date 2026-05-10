package websrv

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"

	"github.com/agentctl/agentctl/internal/proto"
)

func (s *Server) handleSessionLogs(w http.ResponseWriter, r *http.Request, id string) {
	if s.logs == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "log streaming unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	follow := r.URL.Query().Get("follow") == "1" || r.URL.Query().Get("follow") == "true"

	send := func(line []byte) error {
		clean := bytes.TrimRight(line, "\n")
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", proto.EventLogLine, clean); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if err := s.logs.Stream(r.Context(), id, follow, send); err != nil && !errors.Is(err, r.Context().Err()) {
		// SSE has no clean way to surface mid-stream errors; emit a final
		// log.line carrying the error so the client at least sees it.
		_, _ = fmt.Fprintf(w, "event: %s\ndata: {\"error\":%q}\n\n", proto.EventLogLine, err.Error())
		flusher.Flush()
	}
}
