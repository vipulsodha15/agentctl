// Package mcpimport parses MCP server entries from external CLI tools'
// configuration files (Claude Code's ~/.claude.json and Codex CLI's
// ~/.codex/config.toml) into a neutral form the registry can consume.
//
// Both tools support two server shapes:
//   - "stdio": launched as a subprocess with command + args + env
//   - "http"/"sse": addressed by URL
//
// agentctl's registry today only renders http/sse transports (see
// internal/mcp/render.go). Parsers therefore preserve stdio metadata so
// callers can report it back to the user, but the importer marks those
// entries as Skip == "stdio transport not supported" rather than dropping
// them silently.
package mcpimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// ParsedEntry is the neutral representation of one MCP server pulled from
// an external config. Skip is non-empty when the entry isn't importable
// into the current registry; the importer surfaces it as a skip reason.
type ParsedEntry struct {
	Name        string
	URL         string
	Transport   string // "http" or "sse" — empty when Skip is set
	Description string
	// Command/Args/Env are populated for stdio servers so callers can
	// echo them back to the user even though the registry can't store
	// them today.
	Command string
	Args    []string
	Env     map[string]string
	Skip    string // reason; non-empty means do not insert
}

// DefaultClaudePath returns ~/.claude.json (Claude Code's user-level config).
func DefaultClaudePath(home string) string {
	return filepath.Join(home, ".claude.json")
}

// DefaultCodexPath returns $CODEX_HOME/config.toml or ~/.codex/config.toml.
func DefaultCodexPath(home string) string {
	if codex := os.Getenv("CODEX_HOME"); codex != "" {
		return filepath.Join(codex, "config.toml")
	}
	return filepath.Join(home, ".codex", "config.toml")
}

// claudeConfig mirrors the subset of ~/.claude.json we care about.
// Claude stores both user-level entries under top-level mcpServers and
// project-scoped entries under projects.<path>.mcpServers; we only read
// the top-level map (user-global) for now to keep the import idempotent.
type claudeConfig struct {
	MCPServers map[string]claudeServer `json:"mcpServers"`
}

type claudeServer struct {
	// stdio fields
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// http/sse fields
	URL  string `json:"url,omitempty"`
	Type string `json:"type,omitempty"` // "http", "sse", or "stdio"
}

// ParseClaude reads a Claude Code config file at path and returns one
// ParsedEntry per server. A missing file returns (nil, os.ErrNotExist) so
// callers can distinguish "no config" from a real error.
func ParseClaude(path string) ([]ParsedEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg claudeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for n := range cfg.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]ParsedEntry, 0, len(names))
	for _, name := range names {
		s := cfg.MCPServers[name]
		out = append(out, claudeServerToEntry(name, s))
	}
	return out, nil
}

func claudeServerToEntry(name string, s claudeServer) ParsedEntry {
	e := ParsedEntry{Name: name, Description: "imported from claude code"}
	// stdio if a command is set, OR if type explicitly says "stdio"
	if s.Command != "" || strings.EqualFold(s.Type, "stdio") {
		e.Command = s.Command
		e.Args = append([]string(nil), s.Args...)
		e.Env = copyMap(s.Env)
		e.Skip = "stdio transport not supported by registry"
		return e
	}
	if s.URL == "" {
		e.Skip = "no url or command specified"
		return e
	}
	e.URL = s.URL
	e.Transport = normalizeTransport(s.Type)
	return e
}

// codexConfig mirrors the [mcp_servers.NAME] sections in ~/.codex/config.toml.
// Codex's documented schema is stdio-only (command/args/env); we also
// tolerate a url field so users who hand-edit their configs aren't
// silently dropped.
type codexConfig struct {
	MCPServers map[string]codexServer `toml:"mcp_servers"`
}

type codexServer struct {
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	URL     string            `toml:"url"`
	Type    string            `toml:"type"`
}

// ParseCodex reads a Codex CLI config file at path and returns one
// ParsedEntry per server. A missing file returns (nil, os.ErrNotExist).
func ParseCodex(path string) ([]ParsedEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg codexConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for n := range cfg.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]ParsedEntry, 0, len(names))
	for _, name := range names {
		s := cfg.MCPServers[name]
		out = append(out, codexServerToEntry(name, s))
	}
	return out, nil
}

func codexServerToEntry(name string, s codexServer) ParsedEntry {
	e := ParsedEntry{Name: name, Description: "imported from codex cli"}
	if s.Command != "" || strings.EqualFold(s.Type, "stdio") {
		e.Command = s.Command
		e.Args = append([]string(nil), s.Args...)
		e.Env = copyMap(s.Env)
		e.Skip = "stdio transport not supported by registry"
		return e
	}
	if s.URL == "" {
		e.Skip = "no url or command specified"
		return e
	}
	e.URL = s.URL
	e.Transport = normalizeTransport(s.Type)
	return e
}

func normalizeTransport(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "sse":
		return "sse"
	case "", "http", "https", "streamable-http":
		return "http"
	default:
		return strings.ToLower(t)
	}
}

func copyMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ErrNoSource is returned by Parse* helpers when the file is missing in
// contexts where callers want a typed error.
var ErrNoSource = errors.New("mcpimport: source file not found")
