package cc

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)
	body, _ := json.Marshal(map[string]any{"shim_version": "1.0.0"})
	in := Frame{Seq: 1, Kind: KindHello, TS: time.Unix(123456789, 0).UTC(), Data: body}
	if err := w.Write(in); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := NewFrameReader(&buf)
	out, err := r.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out.Kind != in.Kind || out.Seq != in.Seq || !bytes.Equal(out.Data, in.Data) {
		t.Errorf("mismatch: in=%+v out=%+v", in, out)
	}
	if out.V != ProtocolVersion {
		t.Errorf("V: got %d want %d", out.V, ProtocolVersion)
	}
}

func TestFrameRejectsOversizeOnRead(t *testing.T) {
	var buf bytes.Buffer
	huge := strings.Repeat("a", MaxFrameBytes+1)
	buf.WriteString(huge)
	buf.WriteByte('\n')
	r := NewFrameReader(&buf)
	if _, err := r.Read(); err != ErrFrameTooLarge {
		t.Errorf("got err=%v want ErrFrameTooLarge", err)
	}
}

func TestFrameRejectsOversizeOnWrite(t *testing.T) {
	body := append([]byte(`"`), bytes.Repeat([]byte("x"), MaxFrameBytes)...)
	body = append(body, '"')
	w := NewFrameWriter(&bytes.Buffer{})
	if err := w.Write(Frame{Kind: KindEvent, Data: body}); err != ErrFrameTooLarge {
		t.Errorf("got err=%v want ErrFrameTooLarge", err)
	}
}

func TestFrameNDJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)
	if err := w.Write(Frame{Kind: KindHeartbeat, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := buf.Bytes()
	if out[len(out)-1] != '\n' {
		t.Errorf("missing terminating newline: %q", out)
	}
	if bytes.Count(out, []byte("\n")) != 1 {
		t.Errorf("expected single newline, got %d", bytes.Count(out, []byte("\n")))
	}
}
