package registry

import (
	_ "embed"
	"errors"
	"fmt"
	"os"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/agentctl/agentctl/internal/store"
)

//go:embed registry.seed.toml
var embeddedSeed []byte

type fileFormat struct {
	MCP []entry `toml:"mcp"`
}

type entry struct {
	Name           string `toml:"name"`
	URL            string `toml:"url"`
	Transport      string `toml:"transport"`
	Kind           string `toml:"kind"`
	DefaultEnabled bool   `toml:"default_enabled"`
	Description    string `toml:"description"`
	AuthConfig     string `toml:"auth_config_json,omitempty"`
}

type Source struct {
	Path     string
	FromDisk bool
}

func Resolve(userPath, sitePath string) ([]byte, Source, error) {
	if userPath != "" {
		if data, err := os.ReadFile(userPath); err == nil {
			return data, Source{Path: userPath, FromDisk: true}, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, Source{}, fmt.Errorf("read user seed %s: %w", userPath, err)
		}
	}
	if sitePath != "" {
		if data, err := os.ReadFile(sitePath); err == nil {
			return data, Source{Path: sitePath, FromDisk: true}, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, Source{}, fmt.Errorf("read site seed %s: %w", sitePath, err)
		}
	}
	return embeddedSeed, Source{Path: "embedded"}, nil
}

func Parse(data []byte) ([]store.MCPSeedRow, error) {
	var f fileFormat
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse seed: %w", err)
	}
	out := make([]store.MCPSeedRow, 0, len(f.MCP))
	for _, e := range f.MCP {
		if e.Name == "" || e.URL == "" || e.Transport == "" || e.Kind == "" {
			return nil, fmt.Errorf("seed entry missing required fields: %+v", e)
		}
		out = append(out, store.MCPSeedRow{
			Name:           e.Name,
			URL:            e.URL,
			Transport:      e.Transport,
			Kind:           e.Kind,
			AuthConfigJSON: e.AuthConfig,
			DefaultEnabled: e.DefaultEnabled,
			Description:    e.Description,
		})
	}
	return out, nil
}

func EmbeddedBytes() []byte {
	return embeddedSeed
}
