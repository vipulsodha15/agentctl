package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	toml "github.com/pelletier/go-toml/v2"
)

const (
	FilePerm = 0o600
	DirPerm  = 0o700
)

type Config struct {
	Agentd  AgentdSection  `toml:"agentd"`
	Session SessionSection `toml:"session"`
	Image   ImageSection   `toml:"image"`
	Model   ModelSection   `toml:"model"`
	Pricing PricingSection `toml:"pricing"`
}

type AgentdSection struct {
	WebAddr  string `toml:"web_addr"`
	LogLevel string `toml:"log_level"`
}

type SessionSection struct {
	IdleTimeout   string  `toml:"idle_timeout"`
	MaxIdle       string  `toml:"max_idle"`
	MemLimit      string  `toml:"mem_limit"`
	CPULimit      float64 `toml:"cpu_limit"`
	QueuePolicy   string  `toml:"queue_policy"`
}

type ImageSection struct {
	LocalTag         string `toml:"local_tag"`
	BuildContextPath string `toml:"build_context_path"`
	PinnedID         string `toml:"pinned_id"`
	PreviousID       string `toml:"previous_id"`
}

type ModelSection struct {
	Default string `toml:"default"`
}

type PricingSection struct {
	Tables PricingTables `toml:"tables"`
}

type PricingTables struct {
	Version int                       `toml:"version"`
	Models  map[string]PricingEntry   `toml:"models"`
}

type PricingEntry struct {
	Input      float64 `toml:"input"`
	Output     float64 `toml:"output"`
	CacheRead  float64 `toml:"cache_read"`
	CacheWrite float64 `toml:"cache_write"`
}

func Default() Config {
	return Config{
		Agentd: AgentdSection{
			WebAddr:  "127.0.0.1:7777",
			LogLevel: "info",
		},
		Session: SessionSection{
			IdleTimeout: "15m",
			MaxIdle:     "24h",
			MemLimit:    "4GiB",
			CPULimit:    2.0,
			QueuePolicy: "queue",
		},
		Image: ImageSection{
			LocalTag:         "agentctl/session-base:local",
			BuildContextPath: "~/.local/share/agentctl/image",
		},
		Model: ModelSection{
			Default: "claude-sonnet-4-6",
		},
		Pricing: PricingSection{
			Tables: PricingTables{
				Version: 1,
				Models: map[string]PricingEntry{
					"claude-opus-4-7":   {Input: 15.00, Output: 75.00, CacheRead: 1.50, CacheWrite: 18.75},
					"claude-sonnet-4-6": {Input: 3.00, Output: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
					"claude-haiku-4-5":  {Input: 0.80, Output: 4.00, CacheRead: 0.08, CacheWrite: 1.00},
				},
			},
		},
	}
}

var loadMu sync.Mutex

func Load(path string) (Config, error) {
	loadMu.Lock()
	defer loadMu.Unlock()
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("config not found at %s: %w", path, err)
		}
		return cfg, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	d := Default()
	if c.Agentd.WebAddr == "" {
		c.Agentd.WebAddr = d.Agentd.WebAddr
	}
	if c.Agentd.LogLevel == "" {
		c.Agentd.LogLevel = d.Agentd.LogLevel
	}
	if c.Session.IdleTimeout == "" {
		c.Session.IdleTimeout = d.Session.IdleTimeout
	}
	if c.Session.MaxIdle == "" {
		c.Session.MaxIdle = d.Session.MaxIdle
	}
	if c.Session.MemLimit == "" {
		c.Session.MemLimit = d.Session.MemLimit
	}
	if c.Session.CPULimit == 0 {
		c.Session.CPULimit = d.Session.CPULimit
	}
	if c.Session.QueuePolicy == "" {
		c.Session.QueuePolicy = d.Session.QueuePolicy
	}
	if c.Image.LocalTag == "" {
		c.Image.LocalTag = d.Image.LocalTag
	}
	if c.Image.BuildContextPath == "" {
		c.Image.BuildContextPath = d.Image.BuildContextPath
	}
	if c.Model.Default == "" {
		c.Model.Default = d.Model.Default
	}
	if c.Pricing.Tables.Version == 0 {
		c.Pricing.Tables = d.Pricing.Tables
	}
}

func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), DirPerm); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", path, err)
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return writeFileAtomic(path, data, FilePerm)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".cfg-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func EnsurePerms(path string, perm os.FileMode) (fixed bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if info.Mode().Perm() == perm {
		return false, nil
	}
	if err := os.Chmod(path, perm); err != nil {
		return false, err
	}
	return true, nil
}

func EnsureDir(path string) (created bool, err error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		if mkErr := os.MkdirAll(path, DirPerm); mkErr != nil {
			return false, mkErr
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("expected dir at %s, got file", path)
	}
	if info.Mode().Perm() != DirPerm {
		_ = os.Chmod(path, DirPerm)
	}
	return false, nil
}

func Get(cfg Config, key string) (string, bool) {
	switch key {
	case "agentd.web_addr":
		return cfg.Agentd.WebAddr, true
	case "agentd.log_level":
		return cfg.Agentd.LogLevel, true
	case "session.idle_timeout":
		return cfg.Session.IdleTimeout, true
	case "session.max_idle":
		return cfg.Session.MaxIdle, true
	case "session.mem_limit":
		return cfg.Session.MemLimit, true
	case "session.cpu_limit":
		return fmt.Sprintf("%v", cfg.Session.CPULimit), true
	case "session.queue_policy":
		return cfg.Session.QueuePolicy, true
	case "image.local_tag":
		return cfg.Image.LocalTag, true
	case "image.build_context_path":
		return cfg.Image.BuildContextPath, true
	case "image.pinned_id":
		return cfg.Image.PinnedID, true
	case "image.previous_id":
		return cfg.Image.PreviousID, true
	case "model.default":
		return cfg.Model.Default, true
	}
	return "", false
}

func Set(cfg *Config, key, value string) error {
	switch key {
	case "agentd.web_addr":
		cfg.Agentd.WebAddr = value
	case "agentd.log_level":
		cfg.Agentd.LogLevel = value
	case "session.idle_timeout":
		cfg.Session.IdleTimeout = value
	case "session.max_idle":
		cfg.Session.MaxIdle = value
	case "session.mem_limit":
		cfg.Session.MemLimit = value
	case "session.cpu_limit":
		var f float64
		if _, err := fmt.Sscanf(value, "%f", &f); err != nil {
			return fmt.Errorf("session.cpu_limit must be a number, got %q", value)
		}
		cfg.Session.CPULimit = f
	case "session.queue_policy":
		cfg.Session.QueuePolicy = value
	case "image.local_tag":
		cfg.Image.LocalTag = value
	case "image.build_context_path":
		cfg.Image.BuildContextPath = value
	case "image.pinned_id":
		cfg.Image.PinnedID = value
	case "image.previous_id":
		cfg.Image.PreviousID = value
	case "model.default":
		cfg.Model.Default = value
	default:
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}

func ExpandHome(p string) string {
	if p == "" {
		return p
	}
	if p[0] != '~' {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if len(p) == 1 {
		return home
	}
	if p[1] == '/' || p[1] == os.PathSeparator {
		return filepath.Join(home, p[2:])
	}
	return p
}
