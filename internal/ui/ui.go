package ui

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

func URLForToken(addr, token string) string {
	u := &url.URL{Scheme: "http", Host: addr, Path: "/"}
	u.Fragment = "t=" + token
	return u.String()
}

func Open(target string) error {
	var bin string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		bin = "open"
		args = []string{target}
	case "linux":
		if _, err := exec.LookPath("xdg-open"); err == nil {
			bin = "xdg-open"
			args = []string{target}
		} else {
			return fmt.Errorf("xdg-open not found; visit %s manually", target)
		}
	case "windows":
		bin = "cmd"
		args = []string{"/c", "start", target}
	default:
		return fmt.Errorf("unsupported platform %s; visit %s manually", runtime.GOOS, target)
	}
	cmd := exec.Command(bin, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}
	return nil
}
