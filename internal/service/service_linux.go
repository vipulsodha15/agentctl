//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const linuxUnitTemplate = `[Unit]
Description=agentctl daemon
After=docker.service default.target
Wants=docker.service
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
ExecStart={{BIN}} agentd
Restart=on-failure
RestartSec=2s
TimeoutStopSec=60
Environment=AGENTCTL_HOME={{HOME}}
StandardOutput=journal
StandardError=journal

ProtectSystem=full
ProtectHome=read-write
NoNewPrivileges=true
PrivateTmp=true
LockPersonality=true
RestrictSUIDSGID=true

[Install]
WantedBy=default.target
`

type linuxManager struct {
	home string
}

func platformManager(home string) Manager {
	return &linuxManager{home: home}
}

func (m *linuxManager) UnitPath() string {
	return filepath.Join(m.home, ".config", "systemd", "user", "agentd.service")
}

func (m *linuxManager) Platform() string { return "linux" }

func (m *linuxManager) Install(opts InstallOptions) (Status, error) {
	unit := strings.NewReplacer("{{BIN}}", opts.BinaryPath, "{{HOME}}", m.home).Replace(linuxUnitTemplate)
	path := m.UnitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Status{}, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	wrote, err := writeIfChanged(path, []byte(unit), 0o644)
	if err != nil {
		return Status{}, err
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return Status{UnitPath: path, Wrote: wrote, Reason: "systemctl unavailable; foreground fallback required"}, ErrUnsupportedPlatform
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return Status{UnitPath: path, Wrote: wrote}, fmt.Errorf("systemctl daemon-reload: %v: %s", err, string(out))
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "agentd.service").CombinedOutput(); err != nil {
		return Status{UnitPath: path, Wrote: wrote}, fmt.Errorf("systemctl enable: %v: %s", err, string(out))
	}
	return Status{UnitPath: path, Wrote: wrote}, nil
}

func (m *linuxManager) Start() error {
	out, err := exec.Command("systemctl", "--user", "start", "agentd.service").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl start: %v: %s", err, string(out))
	}
	return nil
}

func (m *linuxManager) Stop() error {
	out, err := exec.Command("systemctl", "--user", "stop", "agentd.service").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl stop: %v: %s", err, string(out))
	}
	return nil
}

func (m *linuxManager) IsActive() (bool, error) {
	out, err := exec.Command("systemctl", "--user", "is-active", "agentd.service").CombinedOutput()
	state := strings.TrimSpace(string(out))
	if err == nil && state == "active" {
		return true, nil
	}
	if state == "" {
		return false, err
	}
	return false, nil
}
