package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

const volumeWarnPctUsed = 80

type sessionUsage struct {
	Name  string
	Bytes int64
}

func checkVolumesDisk(sessionsDir string) Check {
	if _, err := os.Stat(sessionsDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Check{
				Name:    "volumes.disk",
				Status:  StatusOK,
				Message: "sessions dir empty",
			}
		}
		return Check{Name: "volumes.disk", Status: StatusFail, Message: err.Error()}
	}
	usages, totalBytes, err := walkSessionVolumes(sessionsDir)
	if err != nil {
		return Check{Name: "volumes.disk", Status: StatusFail, Message: err.Error()}
	}
	pctUsed, partTotal, partAvail, err := partitionUsage(sessionsDir)
	if err != nil {
		return Check{
			Name:    "volumes.disk",
			Status:  StatusWarn,
			Message: fmt.Sprintf("%d sessions, %s in volumes; partition usage unknown", len(usages), humanBytes(totalBytes)),
			Detail:  err.Error(),
		}
	}
	top3 := topNUsage(usages, 3)
	msg := fmt.Sprintf("%d session(s); volumes=%s; partition=%d%% used (%s free of %s)",
		len(usages), humanBytes(totalBytes), pctUsed, humanBytes(partAvail), humanBytes(partTotal))
	status := StatusOK
	if pctUsed >= volumeWarnPctUsed {
		status = StatusWarn
	}
	return Check{
		Name:    "volumes.disk",
		Status:  status,
		Message: msg,
		Detail:  formatTopUsage(top3),
	}
}

func walkSessionVolumes(sessionsDir string) ([]sessionUsage, int64, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, 0, err
	}
	var usages []sessionUsage
	var total int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		volDir := filepath.Join(sessionsDir, e.Name(), "volume")
		size, err := dirSize(volDir)
		if err != nil {
			continue
		}
		usages = append(usages, sessionUsage{Name: e.Name(), Bytes: size})
		total += size
	}
	return usages, total, nil
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func partitionUsage(path string) (pctUsed int, total, avail int64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	total = int64(st.Blocks) * int64(st.Bsize)
	avail = int64(st.Bavail) * int64(st.Bsize)
	used := total - avail
	if total <= 0 {
		return 0, total, avail, nil
	}
	pctUsed = int((used * 100) / total)
	return pctUsed, total, avail, nil
}

func topNUsage(usages []sessionUsage, n int) []sessionUsage {
	cp := make([]sessionUsage, len(usages))
	copy(cp, usages)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Bytes > cp[j].Bytes })
	if len(cp) > n {
		cp = cp[:n]
	}
	return cp
}

func formatTopUsage(top []sessionUsage) string {
	if len(top) == 0 {
		return ""
	}
	out := "top: "
	for i, u := range top {
		if i > 0 {
			out += ", "
		}
		out += u.Name + " " + humanBytes(u.Bytes)
	}
	return out
}

func humanBytes(b int64) string {
	const k = 1024
	if b < k {
		return fmt.Sprintf("%dB", b)
	}
	units := []string{"K", "M", "G", "T"}
	v := float64(b)
	i := -1
	for v >= k && i < len(units)-1 {
		v /= k
		i++
	}
	return fmt.Sprintf("%.1f%sB", v, units[i])
}
