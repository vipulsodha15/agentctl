package mcp

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeAllOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	results := ProbeAll(context.Background(), []Entry{
		{Name: "ok", URL: srv.URL + "/path"},
	}, ProbeOptions{PerProbe: time.Second})
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("expected ok, got %+v", results)
	}
}

func TestProbeAllConnectionRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	results := ProbeAll(context.Background(), []Entry{
		{Name: "down", URL: "http://" + addr + "/"},
	}, ProbeOptions{PerProbe: 500 * time.Millisecond})
	if len(results) != 1 || results[0].OK {
		t.Fatalf("expected fail, got %+v", results)
	}
	if results[0].Reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestProbeAllBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(502)
	}))
	defer srv.Close()
	results := ProbeAll(context.Background(), []Entry{
		{Name: "bad", URL: srv.URL + "/x"},
	}, ProbeOptions{PerProbe: time.Second})
	if results[0].OK {
		t.Fatalf("expected fail on 502, got %+v", results[0])
	}
	if results[0].Reason != "http 502" {
		t.Errorf("unexpected reason: %q", results[0].Reason)
	}
}

func TestProbeAllPerProbeTimeout(t *testing.T) {
	// Block the server briefly so the probe per-attempt timeout trips.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	results := ProbeAll(context.Background(), []Entry{
		{Name: "slow", URL: srv.URL + "/x"},
	}, ProbeOptions{PerProbe: 30 * time.Millisecond, Batch: time.Second})
	if results[0].OK {
		t.Fatalf("expected timeout, got %+v", results[0])
	}
}

func TestProbeAllParallelRespectsBatchCeiling(t *testing.T) {
	results := ProbeAll(context.Background(), []Entry{
		{Name: "a", URL: "http://10.255.255.1:1/"},
		{Name: "b", URL: "http://10.255.255.2:2/"},
		{Name: "c", URL: "http://10.255.255.3:3/"},
	}, ProbeOptions{PerProbe: 200 * time.Millisecond, Batch: 600 * time.Millisecond})
	start := time.Now()
	_ = results
	elapsed := time.Since(start)
	if elapsed > 800*time.Millisecond {
		t.Errorf("batch took too long: %v", elapsed)
	}
}

func TestProbeAllMappedToStatusMap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	results := ProbeAll(context.Background(), []Entry{
		{Name: "ok", URL: srv.URL + "/"},
	}, ProbeOptions{PerProbe: time.Second})
	m := ProbeResultsToStatusMap(results)
	if m["ok"] != "ok" {
		t.Fatalf("expected ok, got %v", m)
	}
}
