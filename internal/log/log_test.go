package log

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactStaticPatterns(t *testing.T) {
	cases := map[string]string{
		"sk-ant-api03-XYZ123abcDEF":              "***REDACTED***",
		"ghp_AAAAAAAAAAAAAAAAAAAAA":              "***REDACTED***",
		"ghs_AAAAAAAAAAAAAAAAAAAAA":              "***REDACTED***",
		"github_pat_11AAAAA0_BBBBBBBBBBBBBBBBBB": "***REDACTED***",
	}
	for in, want := range cases {
		got := Redact("token=" + in)
		if !strings.Contains(got, want) {
			t.Errorf("Redact(%q) = %q; want contains %q", in, got, want)
		}
		if strings.Contains(got, in) {
			t.Errorf("Redact(%q) leaked the secret: %q", in, got)
		}
	}
}

func TestRedactDynamicSecret(t *testing.T) {
	defer ClearDynamicSecrets()
	RegisterSecret("supersekret-token-value-1234567890")
	got := Redact("authorization: Bearer supersekret-token-value-1234567890")
	if strings.Contains(got, "supersekret") {
		t.Errorf("dynamic secret leaked: %q", got)
	}
}

func TestNewWritesNDJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Options{
		Level:     slog.LevelInfo,
		Output:    &buf,
		Component: "test",
	})
	logger.Info("hello.world", slog.String("session_id", "sess_X"))
	out := buf.String()
	for _, want := range []string{`"ts":`, `"level":"INFO"`, `"msg":"hello.world"`, `"component":"test"`, `"session_id":"sess_X"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q: %s", want, out)
		}
	}
}

func TestRedactorWrapsLogger(t *testing.T) {
	defer ClearDynamicSecrets()
	var buf bytes.Buffer
	logger := New(Options{Output: &buf, Component: "test"})
	logger.Info("token-leak", slog.String("k", "sk-ant-api03-DEADBEEFcafe"))
	if strings.Contains(buf.String(), "DEADBEEFcafe") {
		t.Errorf("redactor failed: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "***REDACTED***") {
		t.Errorf("redactor placeholder missing: %s", buf.String())
	}
}
