package paths

import (
	"os"
	"path/filepath"
)

type Layout struct {
	Home            string
	ConfigDir       string
	DataDir         string
	StateDir        string
	ConfigFile      string
	SecretsFile     string
	WebTokenFile    string
	DBFile          string
	SocketFile      string
	SessionsDir     string
	ImageDir        string
	BuiltinSkills   string
	CustomSkills    string
	UserSeedFile    string
	SiteSeedFile    string
	InstallMeta     string
	LastErrorLog    string
	ClaudeCredsDir  string
	ClaudeCredsFile string
}

func From(home string) Layout {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	cfg := filepath.Join(home, ".config", "agentctl")
	data := filepath.Join(home, ".local", "share", "agentctl")
	state := filepath.Join(home, ".local", "state", "agentctl")
	claudeCreds := filepath.Join(cfg, "claude")
	return Layout{
		Home:            home,
		ConfigDir:       cfg,
		DataDir:         data,
		StateDir:        state,
		ConfigFile:      filepath.Join(cfg, "config.toml"),
		SecretsFile:     filepath.Join(cfg, "secrets.json"),
		WebTokenFile:    filepath.Join(cfg, "web_token"),
		DBFile:          filepath.Join(data, "agentd.db"),
		SocketFile:      filepath.Join(data, "agentd.sock"),
		SessionsDir:     filepath.Join(data, "sessions"),
		ImageDir:        filepath.Join(data, "image"),
		BuiltinSkills:   filepath.Join(data, "builtin-skills"),
		CustomSkills:    filepath.Join(data, "custom-skills"),
		UserSeedFile:    filepath.Join(cfg, "registry.seed.toml"),
		SiteSeedFile:    "/etc/agentctl/registry.seed.toml",
		InstallMeta:     filepath.Join(data, "install_metadata.json"),
		LastErrorLog:    filepath.Join(state, "last-error.log"),
		ClaudeCredsDir:  claudeCreds,
		ClaudeCredsFile: filepath.Join(claudeCreds, ".credentials.json"),
	}
}

func Resolve() Layout {
	if h := os.Getenv("AGENTCTL_HOME"); h != "" {
		return From(h)
	}
	return From("")
}
