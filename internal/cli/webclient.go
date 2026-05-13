package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/secrets"
)

// webClient is a thin HTTP client that talks to agentd's web API. It is
// intentionally small: it loads the bearer token + addr each construction,
// attaches the right Origin header for state-changing requests, and surfaces
// JSON error bodies as readable error strings.
type webClient struct {
	addr   string // host:port, e.g. 127.0.0.1:7777
	token  string
	http   *http.Client
	origin string // e.g. http://127.0.0.1:7777
}

func newWebClient(env *Env) (*webClient, int) {
	cfg, err := config.Load(env.Layout.ConfigFile)
	if err != nil {
		fmt.Fprintf(env.Stderr, "load config: %v\n", err)
		return nil, ExitEnvironment
	}
	token, err := secrets.ReadWebToken(env.Layout.WebTokenFile)
	if err != nil {
		fmt.Fprintf(env.Stderr, "read web token: %v\n", err)
		return nil, ExitAuth
	}
	addr := strings.TrimSpace(cfg.Agentd.WebAddr)
	if addr == "" {
		fmt.Fprintln(env.Stderr, "agentd.web_addr is empty")
		return nil, ExitEnvironment
	}
	return &webClient{
		addr:   addr,
		token:  strings.TrimSpace(token),
		origin: "http://" + addr,
		http:   &http.Client{Timeout: 15 * time.Second},
	}, ExitOK
}

// do issues an HTTP request to the daemon. body may be nil. contentType is
// used (and Origin attached) for non-GET methods.
func (c *webClient) do(ctx context.Context, env *Env, method, path string, body io.Reader, contentType string) ([]byte, int) {
	url := "http://" + c.addr + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		fmt.Fprintf(env.Stderr, "build request: %v\n", err)
		return nil, ExitGeneric
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", c.origin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s %s: %v\n", method, path, err)
		return nil, ExitEnvironment
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return respBody, ExitOK
	}
	msg := strings.TrimSpace(string(respBody))
	// Try to pull a friendly error message out of the JSON body.
	var errEnv struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(respBody, &errEnv) == nil && errEnv.Error.Message != "" {
		msg = errEnv.Error.Message
	}
	fmt.Fprintf(env.Stderr, "%s %s: %s (HTTP %d)\n", method, path, msg, resp.StatusCode)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return respBody, ExitAuth
	case http.StatusPreconditionFailed, http.StatusConflict:
		return respBody, ExitSessionState
	case http.StatusNotFound, http.StatusBadRequest:
		return respBody, ExitUsage
	default:
		return respBody, ExitGeneric
	}
}
