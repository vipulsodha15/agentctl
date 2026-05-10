package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
)

type ComposeResult struct {
	Path       string
	Hash       string
	Skills     []string
	Collisions []string
}

type Composer interface {
	Compose(dest string) (ComposeResult, error)
}

func (m *manager) Compose(dest string) (ComposeResult, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return ComposeResult{}, err
	}
	written := map[string]string{}

	if m.builtinDir != "" {
		entries, err := readSkillSubdirs(m.builtinDir)
		if err != nil {
			return ComposeResult{}, err
		}
		for _, name := range entries {
			src := filepath.Join(m.builtinDir, name)
			dst := filepath.Join(dest, name)
			if err := copyDir(src, dst); err != nil {
				return ComposeResult{}, err
			}
			written[name] = SourceBuiltin
		}
	}

	collisions := []string{}
	if m.customDir != "" {
		entries, err := readSkillSubdirs(m.customDir)
		if err != nil {
			return ComposeResult{}, err
		}
		for _, name := range entries {
			src := filepath.Join(m.customDir, name)
			dst := filepath.Join(dest, name)
			if existing, ok := written[name]; ok && existing == SourceBuiltin {
				if err := os.RemoveAll(dst); err != nil {
					return ComposeResult{}, err
				}
				collisions = append(collisions, name)
			}
			if err := copyDir(src, dst); err != nil {
				return ComposeResult{}, err
			}
			written[name] = SourceCustom
		}
	}

	names := make([]string, 0, len(written))
	for n := range written {
		names = append(names, n)
	}
	sort.Strings(names)
	sort.Strings(collisions)

	hash, err := hashTree(dest)
	if err != nil {
		return ComposeResult{}, err
	}
	return ComposeResult{
		Path:       dest,
		Hash:       hash,
		Skills:     names,
		Collisions: collisions,
	}, nil
}

func readSkillSubdirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, mfErr := readManifest(dir); mfErr != nil {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

func hashTree(root string) (string, error) {
	type fileEntry struct {
		rel  string
		path string
	}
	var files []fileEntry
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		files = append(files, fileEntry{rel: filepath.ToSlash(rel), path: p})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })

	h := sha256.New()
	for _, f := range files {
		_, _ = io.WriteString(h, f.rel)
		_, _ = h.Write([]byte{0})
		fh, ferr := os.Open(f.path)
		if ferr != nil {
			return "", ferr
		}
		if _, copyErr := io.Copy(h, fh); copyErr != nil {
			_ = fh.Close()
			return "", copyErr
		}
		_ = fh.Close()
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
