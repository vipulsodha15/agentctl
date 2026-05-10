package socksrv

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	cases := [][]byte{
		[]byte("hello"),
		{},
		bytes.Repeat([]byte("x"), 4096),
	}
	for _, payload := range cases {
		var buf bytes.Buffer
		if err := WriteFrame(&buf, payload); err != nil {
			t.Fatalf("write: %v", err)
		}
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("round-trip mismatch (len=%d)", len(payload))
		}
	}
}

func TestWriteFrameRejectsTooLarge(t *testing.T) {
	var buf bytes.Buffer
	huge := make([]byte, MaxFrameBytes+1)
	err := WriteFrame(&buf, huge)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("expected size error, got %v", err)
	}
}

func TestReadFrameRejectsTooLarge(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	if _, err := ReadFrame(&buf); err == nil {
		t.Errorf("expected size error")
	}
}
