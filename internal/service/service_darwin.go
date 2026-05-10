//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const darwinPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>             <string>com.agentctl.agentd</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{BIN}}</string>
    <string>agentd</string>
  </array>
  <key>RunAtLoad</key>         <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>  <false/>
    <key>NetworkState</key>    <true/>
  </dict>
  <key>ProcessType</key>       <string>Background</string>
  <key>ThrottleInterval</key>  <integer>2</integer>
  <key>EnvironmentVariables</key>
  <dict>
    <key>AGENTCTL_HOME</key>   <string>{{HOME}}</string>
  </dict>
  <key>StandardOutPath</key>   <string>{{HOME}}/Library/Logs/agentctl/agentd.log</string>
  <key>StandardErrorPath</key> <string>{{HOME}}/Library/Logs/agentctl/agentd.log</string>
</dict>
</plist>
`

type darwinManager struct {
	home string
}

func platformManager(home string) Manager {
	return &darwinManager{home: home}
}

func (m *darwinManager) UnitPath() string {
	return filepath.Join(m.home, "Library", "LaunchAgents", "com.agentctl.agentd.plist")
}

func (m *darwinManager) Platform() string { return "darwin" }

func (m *darwinManager) Install(opts InstallOptions) (Status, error) {
	plist := strings.NewReplacer("{{BIN}}", opts.BinaryPath, "{{HOME}}", m.home).Replace(darwinPlistTemplate)
	path := m.UnitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Status{}, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	wrote, err := writeIfChanged(path, []byte(plist), 0o644)
	if err != nil {
		return Status{}, err
	}
	logsDir := filepath.Join(m.home, "Library", "Logs", "agentctl")
	_ = os.MkdirAll(logsDir, 0o755)
	if _, err := exec.LookPath("launchctl"); err != nil {
		return Status{UnitPath: path, Wrote: wrote, Reason: "launchctl unavailable"}, ErrUnsupportedPlatform
	}
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid, path).Run()
	if out, err := exec.Command("launchctl", "bootstrap", uid, path).CombinedOutput(); err != nil {
		return Status{UnitPath: path, Wrote: wrote}, fmt.Errorf("launchctl bootstrap: %v: %s", err, string(out))
	}
	return Status{UnitPath: path, Wrote: wrote}, nil
}

func (m *darwinManager) Start() error {
	uid := fmt.Sprintf("gui/%d/com.agentctl.agentd", os.Getuid())
	out, err := exec.Command("launchctl", "kickstart", "-k", uid).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl kickstart: %v: %s", err, string(out))
	}
	return nil
}

func (m *darwinManager) Stop() error {
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	out, err := exec.Command("launchctl", "bootout", uid, m.UnitPath()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootout: %v: %s", err, string(out))
	}
	return nil
}

func (m *darwinManager) IsActive() (bool, error) {
	uid := fmt.Sprintf("gui/%d/com.agentctl.agentd", os.Getuid())
	out, err := exec.Command("launchctl", "print", uid).CombinedOutput()
	if err != nil {
		return false, nil
	}
	return strings.Contains(string(out), "state = running"), nil
}
