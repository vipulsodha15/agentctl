package doctor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/service"
	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/version"
)

type Status string

const (
	StatusOK   Status = "ok"
	StatusFail Status = "fail"
	StatusWarn Status = "warn"
	StatusSkip Status = "skip"
)

type Check struct {
	Name    string
	Status  Status
	Message string
	Detail  string
}

type Result struct {
	Checks []Check
}

func (r *Result) Add(c Check) {
	r.Checks = append(r.Checks, c)
}

func (r *Result) HasFailures() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

type RunOptions struct {
	Home         string
	ConfigPath   string
	SecretsPath  string
	WebTokenPath string
	DBPath       string
	SocketPath   string
	WebAddr      string
	BuiltinDir   string
	CustomDir    string
	SessionsDir  string
}

func DefaultPaths(home string) RunOptions {
	dataDir := filepath.Join(home, ".local", "share", "agentctl")
	return RunOptions{
		Home:         home,
		ConfigPath:   filepath.Join(home, ".config", "agentctl", "config.toml"),
		SecretsPath:  filepath.Join(home, ".config", "agentctl", "secrets.json"),
		WebTokenPath: filepath.Join(home, ".config", "agentctl", "web_token"),
		DBPath:       filepath.Join(dataDir, "agentd.db"),
		SocketPath:   filepath.Join(dataDir, "agentd.sock"),
		WebAddr:      "127.0.0.1:7777",
		BuiltinDir:   filepath.Join(dataDir, "builtin-skills"),
		CustomDir:    filepath.Join(dataDir, "custom-skills"),
		SessionsDir:  filepath.Join(dataDir, "sessions"),
	}
}

func Run(opts RunOptions) Result {
	var r Result
	r.Add(checkBinVersions())
	r.Add(checkFSPerms(opts))
	r.Add(checkDBIntegrity(opts.DBPath))
	r.Add(checkBuildContext(opts.Home))
	r.Add(checkBuildContextDrift(opts.Home))
	r.Add(checkImageBuilt(opts.ConfigPath))
	r.Add(checkServiceActive(opts.Home))
	r.Add(checkAgentdHealth(opts.SocketPath, opts.WebAddr))
	docker := checkDockerReachable()
	r.Add(docker)
	if docker.Status == StatusOK {
		r.Add(checkDockerAPI())
		r.Add(checkImagePresent(opts.ConfigPath))
		r.Add(checkNetworkPeerIsolation())
	} else {
		r.Add(Check{Name: "docker.api", Status: StatusSkip, Message: "skipped (docker unreachable)"})
		r.Add(Check{Name: "image.present", Status: StatusSkip, Message: "skipped (docker unreachable)"})
		r.Add(Check{Name: "network.peer_isolation", Status: StatusSkip, Message: "skipped (docker unreachable)"})
	}
	r.Add(checkSkillsBuiltin(opts.BuiltinDir))
	r.Add(checkSkillsCustom(opts.CustomDir, opts.BuiltinDir))
	r.Add(checkMCPRegistry(opts.DBPath))
	r.Add(checkSecretsFresh(opts.SecretsPath, nil))
	r.Add(checkVolumesDisk(opts.SessionsDir))
	return r
}

func checkBinVersions() Check {
	return Check{
		Name:    "bin.versions",
		Status:  StatusOK,
		Message: fmt.Sprintf("agentctl=%s build=%s", version.Version, version.Build),
	}
}

func checkFSPerms(opts RunOptions) Check {
	type entry struct {
		path string
		want os.FileMode
		dir  bool
	}
	candidates := []entry{
		{filepath.Dir(opts.ConfigPath), 0o700, true},
		{filepath.Dir(opts.DBPath), 0o700, true},
		{opts.ConfigPath, 0o600, false},
		{opts.SecretsPath, 0o600, false},
		{opts.WebTokenPath, 0o600, false},
	}
	var bad []string
	for _, c := range candidates {
		info, err := os.Stat(c.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			bad = append(bad, fmt.Sprintf("%s: %v", c.path, err))
			continue
		}
		mode := info.Mode().Perm()
		if mode != c.want {
			bad = append(bad, fmt.Sprintf("%s: mode %o (want %o)", c.path, mode, c.want))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return Check{Name: "fs.perms", Status: StatusFail, Message: "perms drift", Detail: joinLines(bad)}
	}
	return Check{Name: "fs.perms", Status: StatusOK, Message: "all paths 0700/0600"}
}

func checkDBIntegrity(dbPath string) Check {
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Check{Name: "db.integrity", Status: StatusFail, Message: "agentd.db missing", Detail: dbPath}
		}
		return Check{Name: "db.integrity", Status: StatusFail, Message: err.Error()}
	}
	st, err := store.Open(store.Options{Path: dbPath})
	if err != nil {
		return Check{Name: "db.integrity", Status: StatusFail, Message: err.Error()}
	}
	defer func() { _ = st.Close() }()
	res, err := st.IntegrityCheck()
	if err != nil {
		return Check{Name: "db.integrity", Status: StatusFail, Message: err.Error()}
	}
	if res != "ok" {
		return Check{Name: "db.integrity", Status: StatusFail, Message: "integrity_check=" + res}
	}
	v, _ := st.SchemaVersion()
	return Check{Name: "db.integrity", Status: StatusOK, Message: fmt.Sprintf("integrity ok, schema=%d", v)}
}

func checkBuildContext(home string) Check {
	dir := filepath.Join(home, ".local", "share", "agentctl", "image")
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil {
		return Check{Name: "image.build_context", Status: StatusFail, Message: "Dockerfile missing", Detail: dir}
	}
	if _, err := os.Stat(filepath.Join(dir, "shim", "__main__.py")); err != nil {
		return Check{Name: "image.build_context", Status: StatusFail, Message: "shim source missing", Detail: dir}
	}
	return Check{Name: "image.build_context", Status: StatusOK, Message: "Dockerfile + shim present", Detail: dir}
}

func checkImageBuilt(configPath string) Check {
	cfg, err := config.Load(configPath)
	if err != nil {
		return Check{Name: "image.built", Status: StatusFail, Message: err.Error()}
	}
	if cfg.Image.PinnedID == "" {
		return Check{Name: "image.built", Status: StatusFail, Message: "no pinned image id (run agentctl init)"}
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return Check{Name: "image.built", Status: StatusWarn, Message: "docker not on PATH; cannot verify image"}
	}
	out, err := exec.Command("docker", "image", "inspect", cfg.Image.PinnedID).CombinedOutput()
	if err != nil {
		return Check{Name: "image.built", Status: StatusFail, Message: "image not present", Detail: string(out)}
	}
	return Check{Name: "image.built", Status: StatusOK, Message: cfg.Image.LocalTag + " id=" + truncID(cfg.Image.PinnedID)}
}

func checkServiceActive(home string) Check {
	mgr := service.New(home)
	active, err := mgr.IsActive()
	if errors.Is(err, service.ErrUnsupportedPlatform) {
		return Check{Name: "service.active", Status: StatusSkip, Message: "service manager unsupported on this platform"}
	}
	if err != nil {
		return Check{Name: "service.active", Status: StatusWarn, Message: err.Error()}
	}
	if !active {
		return Check{Name: "service.active", Status: StatusWarn, Message: "service not active (foreground mode is acceptable)"}
	}
	return Check{Name: "service.active", Status: StatusOK, Message: "system service active"}
}

func checkAgentdHealth(socketPath, webAddr string) Check {
	if c, err := cliclient.Dial(socketPath, 1*time.Second); err == nil {
		defer func() { _ = c.Close() }()
		hr, err := c.Health()
		if err == nil {
			if hr.OK {
				return Check{Name: "agentd.health", Status: StatusOK, Message: fmt.Sprintf("uptime=%ds reconciling=%v", hr.UptimeS, hr.Reconciling)}
			}
			return Check{Name: "agentd.health", Status: StatusFail, Message: "Health.ok=false"}
		}
	}
	client := http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get("http://" + webAddr + "/healthz")
	if err != nil {
		return Check{Name: "agentd.health", Status: StatusFail, Message: "agentd unreachable", Detail: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return Check{Name: "agentd.health", Status: StatusFail, Message: fmt.Sprintf("/healthz status=%d", resp.StatusCode)}
	}
	return Check{Name: "agentd.health", Status: StatusOK, Message: "/healthz ok"}
}

func checkDockerReachable() Check {
	if _, err := exec.LookPath("docker"); err != nil {
		return Check{Name: "docker.reachable", Status: StatusFail, Message: "docker not on PATH"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput()
	if err != nil {
		return Check{Name: "docker.reachable", Status: StatusFail, Message: "docker info failed", Detail: string(out)}
	}
	return Check{Name: "docker.reachable", Status: StatusOK, Message: "Docker " + sanitize(string(out))}
}

func truncID(id string) string {
	if len(id) > 19 {
		return id[:19]
	}
	return id
}

func sanitize(s string) string {
	return string([]byte(s[:min(len(s), 64)]))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func joinLines(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "\n"
		}
		out += s
	}
	return out
}
