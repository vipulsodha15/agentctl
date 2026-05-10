package service

import (
	"errors"
	"runtime"
)

type Manager interface {
	Install(opts InstallOptions) (Status, error)
	Start() error
	Stop() error
	IsActive() (bool, error)
	UnitPath() string
	Platform() string
}

type InstallOptions struct {
	BinaryPath string
	Home       string
}

type Status struct {
	UnitPath string
	Wrote    bool
	Reason   string
}

var ErrUnsupportedPlatform = errors.New("service install not supported on this platform")

func New(home string) Manager {
	if mgr := platformManager(home); mgr != nil {
		return mgr
	}
	return &noopManager{platform: runtime.GOOS}
}

type noopManager struct {
	platform string
}

func (noopManager) Install(InstallOptions) (Status, error) { return Status{}, ErrUnsupportedPlatform }
func (noopManager) Start() error                           { return ErrUnsupportedPlatform }
func (noopManager) Stop() error                            { return ErrUnsupportedPlatform }
func (noopManager) IsActive() (bool, error)                { return false, ErrUnsupportedPlatform }
func (noopManager) UnitPath() string                       { return "" }
func (n noopManager) Platform() string                     { return n.platform }
