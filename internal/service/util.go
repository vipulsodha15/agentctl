package service

import (
	"errors"
	"os"
)

func writeIfChanged(path string, content []byte, perm os.FileMode) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == string(content) {
		return false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.WriteFile(path, content, perm); err != nil {
		return false, err
	}
	return true, nil
}
