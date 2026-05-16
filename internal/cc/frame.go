package cc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// MaxFrameBytes caps a single NDJSON line to 1 MiB per api.md §4.2.
const MaxFrameBytes = 1 << 20

const ProtocolVersion = 1

// Frame kinds (container -> agentd).
const (
	KindHello         = "runtime.hello"
	KindReady         = "runtime.ready"
	KindEvent         = "runtime.event"
	KindError         = "runtime.error"
	KindSessionID     = "runtime.session_id"
	KindHeartbeat     = "runtime.heartbeat"
	KindSnapshot      = "runtime.snapshot"
	KindMessageRecord = "runtime.message_record"
	KindRepoChanged   = "repo.changed"
	KindThrottled     = "runtime.throttled"
)

// Frame kinds (agentd -> container).
const (
	KindGreet           = "agentd.greet"
	KindMessage         = "agentd.message"
	KindInterrupt       = "agentd.interrupt"
	KindSnapshotRequest = "agentd.snapshot_request"
	KindShutdown        = "agentd.shutdown"
	KindAgentdError     = "agentd.error"
	// KindSetModel is the control frame the daemon sends to swap the runtime's
	// model id mid-session (ADR 0020 §2). Body: {"model": "<id>"}.
	KindSetModel        = "agentd.set_model"
)

// EventKindAssistantDelta is the only event-kind dropped on overflow.
const EventKindAssistantDelta = "assistant.delta"

type Frame struct {
	V    int             `json:"v"`
	Seq  int64           `json:"seq"`
	Kind string          `json:"kind"`
	TS   time.Time       `json:"ts"`
	Data json.RawMessage `json:"data,omitempty"`
}

var ErrFrameTooLarge = errors.New("control frame exceeds 1 MiB")

type FrameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{w: w}
}

func (fw *FrameWriter) Write(f Frame) error {
	if f.V == 0 {
		f.V = ProtocolVersion
	}
	if f.TS.IsZero() {
		f.TS = time.Now().UTC()
	}
	body, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	if len(body)+1 > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if _, err := fw.w.Write(body); err != nil {
		return err
	}
	if _, err := fw.w.Write([]byte("\n")); err != nil {
		return err
	}
	return nil
}

type FrameReader struct {
	r *bufio.Reader
}

func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{r: bufio.NewReaderSize(r, 64*1024)}
}

// Read returns the next frame. Lines longer than MaxFrameBytes return
// ErrFrameTooLarge; the caller is expected to close the connection.
func (fr *FrameReader) Read() (Frame, error) {
	line, err := readLineLimited(fr.r, MaxFrameBytes)
	if err != nil {
		return Frame{}, err
	}
	line = bytes.TrimRight(line, "\r\n")
	var f Frame
	if err := json.Unmarshal(line, &f); err != nil {
		return Frame{}, fmt.Errorf("decode frame: %w", err)
	}
	return f, nil
}

func readLineLimited(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			if len(buf)+len(chunk) > max {
				return nil, ErrFrameTooLarge
			}
			buf = append(buf, chunk...)
			continue
		}
		if err != nil {
			if len(chunk) > 0 {
				if len(buf)+len(chunk) > max {
					return nil, ErrFrameTooLarge
				}
				buf = append(buf, chunk...)
			}
			if len(buf) == 0 {
				return nil, err
			}
			return buf, err
		}
		if len(buf)+len(chunk) > max {
			return nil, ErrFrameTooLarge
		}
		buf = append(buf, chunk...)
		return buf, nil
	}
}
