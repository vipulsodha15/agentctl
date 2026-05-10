package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	DefaultPerProbeTimeout = 1500 * time.Millisecond
	DefaultBatchTimeout    = 3 * time.Second
)

type ProbeResult struct {
	Name     string
	OK       bool
	Reason   string
	Status   int
	Duration time.Duration
}

type ProbeOptions struct {
	PerProbe time.Duration
	Batch    time.Duration
	HTTP     *http.Client
	Now      func() time.Time
}

func ProbeAll(ctx context.Context, entries []Entry, opts ProbeOptions) []ProbeResult {
	if opts.PerProbe == 0 {
		opts.PerProbe = DefaultPerProbeTimeout
	}
	if opts.Batch == 0 {
		opts.Batch = DefaultBatchTimeout
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{
			Timeout: opts.PerProbe,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	batchCtx, cancel := context.WithTimeout(ctx, opts.Batch)
	defer cancel()

	out := make([]ProbeResult, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(idx int, ent Entry) {
			defer wg.Done()
			start := now()
			out[idx] = probeOne(batchCtx, ent, opts.PerProbe, opts.HTTP)
			out[idx].Duration = now().Sub(start)
		}(i, e)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-batchCtx.Done():
	}
	for i := range out {
		if out[i].Name == "" {
			out[i] = ProbeResult{Name: entries[i].Name, OK: false, Reason: "batch timeout"}
		}
	}
	return out
}

func probeOne(ctx context.Context, e Entry, timeout time.Duration, client *http.Client) ProbeResult {
	res := ProbeResult{Name: e.Name}
	if e.URL == "" {
		res.Reason = "empty url"
		return res
	}
	u, err := url.Parse(e.URL)
	if err != nil {
		res.Reason = "invalid url: " + err.Error()
		return res
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if hasMeaningfulPath(u) {
		hr := httpProbe(probeCtx, e.URL, client)
		hr.Name = e.Name
		return hr
	}
	host := u.Host
	if host == "" {
		host = u.Path
	}
	if !strings.Contains(host, ":") {
		switch u.Scheme {
		case "https":
			host += ":443"
		case "http", "":
			host += ":80"
		}
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(probeCtx, "tcp", host)
	if err != nil {
		res.Reason = classifyDialError(err)
		return res
	}
	_ = conn.Close()
	res.OK = true
	return res
}

func httpProbe(ctx context.Context, target string, client *http.Client) ProbeResult {
	res := ProbeResult{}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		res.Reason = "request build: " + err.Error()
		return res
	}
	req.Header.Set("User-Agent", "agentctl-probe")
	resp, err := client.Do(req)
	if err != nil {
		res.Reason = classifyDialError(err)
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	res.Status = resp.StatusCode
	if resp.StatusCode >= 500 {
		res.Reason = fmt.Sprintf("http %d", resp.StatusCode)
		return res
	}
	res.OK = true
	return res
}

func hasMeaningfulPath(u *url.URL) bool {
	if u.Path == "" || u.Path == "/" {
		return false
	}
	return true
}

func classifyDialError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "i/o timeout"):
		return "connect timeout"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "NXDOMAIN"):
		return "dns: no such host"
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "EOF"):
		return "eof"
	default:
		return msg
	}
}

// ProbeResultsToStatusMap returns a map of name → "ok" or reason for storage in
// sessions.mcp_status_json.
func ProbeResultsToStatusMap(results []ProbeResult) map[string]string {
	out := make(map[string]string, len(results))
	for _, r := range results {
		if r.OK {
			out[r.Name] = "ok"
		} else {
			out[r.Name] = r.Reason
		}
	}
	return out
}
