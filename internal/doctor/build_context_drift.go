package doctor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// checkBuildContextDrift compares the build-context snapshot of the shim
// (under ~/.local/share/agentctl/image/shim/) against the repo it was
// installed from (recorded as `source_url` in install_metadata.json).
//
// The footgun: editing image/shim/*.py in the repo doesn't reach the
// container unless those edits are also synced into the build context,
// because `agentctl update` rebuilds from the snapshot — not from the
// source repo. A no-op CACHED docker build off the stale snapshot is
// the visible symptom; the warn here points the user at the cause.
//
// Skipped (not failed) when source_url is absent or the source dir is
// gone — that just means we can't tell, and a non-checkout install is
// a valid state.
func checkBuildContextDrift(home string) Check {
	name := "image.build_context_drift"
	metaPath := filepath.Join(home, ".local", "share", "agentctl", "install_metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return Check{Name: name, Status: StatusSkip, Message: "install_metadata.json missing"}
	}
	var meta struct {
		SourceURL string `json:"source_url"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return Check{Name: name, Status: StatusSkip, Message: "install_metadata.json unreadable"}
	}
	if meta.SourceURL == "" {
		return Check{Name: name, Status: StatusSkip, Message: "no source_url recorded (non-checkout install)"}
	}
	srcShim := filepath.Join(meta.SourceURL, "image", "shim")
	if _, err := os.Stat(srcShim); err != nil {
		return Check{
			Name:    name,
			Status:  StatusSkip,
			Message: "source shim dir not present",
			Detail:  srcShim,
		}
	}
	ctxShim := filepath.Join(home, ".local", "share", "agentctl", "image", "shim")
	if _, err := os.Stat(ctxShim); err != nil {
		return Check{
			Name:    name,
			Status:  StatusFail,
			Message: "build-context shim missing",
			Detail:  ctxShim,
		}
	}
	diff, err := diffShimTrees(srcShim, ctxShim)
	if err != nil {
		return Check{Name: name, Status: StatusWarn, Message: "drift scan failed", Detail: err.Error()}
	}
	if len(diff) == 0 {
		return Check{Name: name, Status: StatusOK, Message: "build context matches repo"}
	}
	sample := diff
	extra := ""
	if len(sample) > 5 {
		sample = diff[:5]
		extra = fmt.Sprintf("\n  … (%d more)", len(diff)-5)
	}
	return Check{
		Name:    name,
		Status:  StatusWarn,
		Message: fmt.Sprintf("%d file(s) diverge from %s — run `agentctl doctor --fix`", len(diff), meta.SourceURL),
		Detail:  joinLines(sample) + extra,
	}
}

// diffShimTrees walks both shim directories, hashes every regular file
// (skipping __pycache__), and returns a sorted list of relative paths
// that differ between source and build context. `__pycache__` is
// excluded because it's a runtime artifact of `python -m unittest` and
// would otherwise flag drift after every test run.
func diffShimTrees(src, dst string) ([]string, error) {
	srcHashes, err := hashShimDir(src)
	if err != nil {
		return nil, fmt.Errorf("hash source: %w", err)
	}
	dstHashes, err := hashShimDir(dst)
	if err != nil {
		return nil, fmt.Errorf("hash build context: %w", err)
	}
	var diff []string
	for path, sh := range srcHashes {
		dh, ok := dstHashes[path]
		if !ok {
			diff = append(diff, path+" (missing in build context)")
			continue
		}
		if dh != sh {
			diff = append(diff, path+" (different)")
		}
	}
	for path := range dstHashes {
		if _, ok := srcHashes[path]; !ok {
			diff = append(diff, path+" (extra in build context)")
		}
	}
	sort.Strings(diff)
	return diff, nil
}

func hashShimDir(root string) (map[string]string, error) {
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		// Only hash regular files — skip symlinks, sockets, etc.
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			_ = f.Close()
			return err
		}
		_ = f.Close()
		out[filepath.ToSlash(rel)] = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
