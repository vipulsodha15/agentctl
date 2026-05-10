package recovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentlog "github.com/agentctl/agentctl/internal/log"
	"github.com/agentctl/agentctl/internal/store"
)

const (
	StatusStarting   = "starting"
	StatusRunning    = "running"
	StatusStopped    = "stopped"
	StatusTerminated = "terminated"
	StatusError      = "error"

	defaultAdoptTimeout = 2 * time.Second
	stopGrace           = 5 * time.Second
)

type Options struct {
	Store       *store.Store
	Containers  ContainerManager
	Logger      *slog.Logger
	Now         func() time.Time
	SessionsDir string
}

type ContainerManager interface {
	List(ctx context.Context, labelFilter string) ([]ContainerRef, error)
	Inspect(ctx context.Context, id string) (Status, error)
	Stop(ctx context.Context, id string, grace time.Duration) error
	Remove(ctx context.Context, id string, force bool) error
	NetworkList(ctx context.Context, labelFilter string) ([]NetworkRef, error)
	NetworkRemove(ctx context.Context, id string) error
	Adopt(ctx context.Context, sessionID, sockPath string, timeout time.Duration) error
}

type ContainerRef struct {
	ID        string
	Name      string
	SessionID string
	Running   bool
	State     string
}

type NetworkRef struct {
	ID        string
	Name      string
	SessionID string
}

type Status struct {
	State    string
	Running  bool
	ExitCode int
}

type Report struct {
	Sessions         int
	Adopted          int
	StoppedDirty     int
	AbortedStarts    int
	OrphanContainers int
	OrphanNetworks   int
	OrphanDirs       int
	Adoptions        []Adoption
}

type Adoption struct {
	SessionID    string
	ContainerID  string
	SockPath     string
	SessionToken string
}

type sessionRow struct {
	id              string
	status          string
	containerID     sql.NullString
	controlSockPath sql.NullString
	sessionToken    string
}

func Reconcile(ctx context.Context, opts Options) (Report, error) {
	if opts.Store == nil {
		return Report{}, errors.New("recovery: store required")
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Logger == nil {
		opts.Logger = agentlog.New(agentlog.Options{Component: agentlog.ComponentRecovery})
	}

	rep := Report{}

	rows, err := loadActiveSessions(ctx, opts.Store)
	if err != nil {
		return rep, fmt.Errorf("load sessions: %w", err)
	}
	rep.Sessions = len(rows)
	opts.Logger.Info("recovery.start", slog.Int("sessions", len(rows)))

	containers, networks, err := snapshotDocker(ctx, opts.Containers)
	if err != nil {
		opts.Logger.Warn("recovery.docker_snapshot_failed", slog.String("error", err.Error()))
	}

	known := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		known[row.id] = struct{}{}
		if err := reconcileRow(ctx, opts, row, containers, &rep); err != nil {
			opts.Logger.Warn("recovery.row_failed",
				slog.String("session_id", row.id),
				slog.String("status", row.status),
				slog.String("error", err.Error()))
		}
	}

	rep.OrphanContainers = sweepOrphanContainers(ctx, opts, containers, known)
	rep.OrphanNetworks = sweepOrphanNetworks(ctx, opts, networks, known)

	if opts.SessionsDir != "" {
		rep.OrphanDirs = sweepOrphanDirs(opts, known)
	}

	opts.Logger.Info("recovery.complete",
		slog.Int("sessions", rep.Sessions),
		slog.Int("adopted", rep.Adopted),
		slog.Int("stopped_dirty", rep.StoppedDirty),
		slog.Int("aborted_starts", rep.AbortedStarts),
		slog.Int("orphan_containers", rep.OrphanContainers),
		slog.Int("orphan_networks", rep.OrphanNetworks),
		slog.Int("orphan_dirs", rep.OrphanDirs),
	)
	return rep, nil
}

func loadActiveSessions(ctx context.Context, st *store.Store) ([]sessionRow, error) {
	rows, err := st.DB().QueryContext(ctx,
		`SELECT id, status, container_id, control_sock_path, session_token
		   FROM sessions
		  WHERE status IN ('starting','running','stopped')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []sessionRow
	for rows.Next() {
		var r sessionRow
		if err := rows.Scan(&r.id, &r.status, &r.containerID, &r.controlSockPath, &r.sessionToken); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func snapshotDocker(ctx context.Context, cm ContainerManager) (map[string][]ContainerRef, []NetworkRef, error) {
	if cm == nil {
		return nil, nil, nil
	}
	cs, err := cm.List(ctx, "agentctl.session")
	if err != nil {
		return nil, nil, fmt.Errorf("container list: %w", err)
	}
	bySession := make(map[string][]ContainerRef, len(cs))
	for _, c := range cs {
		if c.SessionID == "" {
			continue
		}
		bySession[c.SessionID] = append(bySession[c.SessionID], c)
	}
	nets, nerr := cm.NetworkList(ctx, "agentctl.session")
	if nerr != nil {
		return bySession, nil, fmt.Errorf("network list: %w", nerr)
	}
	return bySession, nets, nil
}

func reconcileRow(ctx context.Context, opts Options, row sessionRow, containers map[string][]ContainerRef, rep *Report) error {
	matches := containers[row.id]
	switch row.status {
	case StatusStarting:
		return reconcileStarting(ctx, opts, row, matches, rep)
	case StatusRunning:
		return reconcileRunning(ctx, opts, row, matches, rep)
	case StatusStopped:
		return reconcileStopped(ctx, opts, row, matches, rep)
	default:
		return nil
	}
}

func reconcileStarting(ctx context.Context, opts Options, row sessionRow, matches []ContainerRef, rep *Report) error {
	ts := opts.Now().Format(time.RFC3339Nano)
	reason := "aborted_during_create_" + ts
	if err := updateSessionStopped(ctx, opts.Store, row.id, reason); err != nil {
		return fmt.Errorf("mark stopped: %w", err)
	}
	for _, m := range matches {
		if opts.Containers != nil {
			if err := opts.Containers.Remove(ctx, m.ID, true); err != nil {
				opts.Logger.Warn("recovery.starting.rm_failed",
					slog.String("session_id", row.id),
					slog.String("container_id", m.ID),
					slog.String("error", err.Error()))
			}
		}
	}
	logLifecycle(opts.Store, row.id, opts.Now(), "reconciled", `{"from":"starting","to":"stopped"}`)
	rep.AbortedStarts++
	opts.Logger.Info("recovery.aborted_start",
		slog.String("session_id", row.id),
		slog.String("reason", reason),
		slog.Int("removed_containers", len(matches)))
	return nil
}

func reconcileRunning(ctx context.Context, opts Options, row sessionRow, matches []ContainerRef, rep *Report) error {
	if len(matches) == 0 {
		if err := updateSessionStopped(ctx, opts.Store, row.id, "container_missing_at_recovery"); err != nil {
			return fmt.Errorf("mark stopped: %w", err)
		}
		logLifecycle(opts.Store, row.id, opts.Now(), "reconciled", `{"from":"running","to":"stopped","reason":"container_missing"}`)
		rep.StoppedDirty++
		opts.Logger.Info("recovery.container_missing", slog.String("session_id", row.id))
		return nil
	}
	c := matches[0]
	if !c.Running {
		if err := updateSessionStopped(ctx, opts.Store, row.id, "container_exited_at_recovery"); err != nil {
			return fmt.Errorf("mark stopped: %w", err)
		}
		if opts.Containers != nil {
			if err := opts.Containers.Remove(ctx, c.ID, false); err != nil {
				opts.Logger.Warn("recovery.exited.rm_failed",
					slog.String("session_id", row.id),
					slog.String("container_id", c.ID),
					slog.String("error", err.Error()))
			}
		}
		logLifecycle(opts.Store, row.id, opts.Now(), "reconciled", `{"from":"running","to":"stopped","reason":"container_exited"}`)
		rep.StoppedDirty++
		opts.Logger.Info("recovery.container_exited",
			slog.String("session_id", row.id),
			slog.String("container_id", c.ID),
			slog.String("state", c.State))
		return nil
	}
	sock := row.controlSockPath.String
	if sock == "" {
		sock = filepath.Join(opts.SessionsDir, row.id, "control", "agentd.sock")
	}
	if opts.Containers != nil {
		if err := opts.Containers.Adopt(ctx, row.id, sock, defaultAdoptTimeout); err != nil {
			opts.Logger.Warn("recovery.adopt_failed",
				slog.String("session_id", row.id),
				slog.String("container_id", c.ID),
				slog.String("error", err.Error()))
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = opts.Containers.Stop(stopCtx, c.ID, stopGrace)
			_ = opts.Containers.Remove(stopCtx, c.ID, true)
			cancel()
			if err := updateSessionStopped(ctx, opts.Store, row.id, "adopt_failed_at_recovery"); err != nil {
				return fmt.Errorf("mark stopped after adopt failure: %w", err)
			}
			logLifecycle(opts.Store, row.id, opts.Now(), "reconciled", `{"from":"running","to":"stopped","reason":"adopt_failed"}`)
			rep.StoppedDirty++
			return nil
		}
	}
	rep.Adopted++
	rep.Adoptions = append(rep.Adoptions, Adoption{
		SessionID:    row.id,
		ContainerID:  c.ID,
		SockPath:     sock,
		SessionToken: row.sessionToken,
	})
	logLifecycle(opts.Store, row.id, opts.Now(), "reconciled", `{"from":"running","to":"running","reason":"adopted"}`)
	opts.Logger.Info("recovery.adopted",
		slog.String("session_id", row.id),
		slog.String("container_id", c.ID))
	return nil
}

func reconcileStopped(ctx context.Context, opts Options, row sessionRow, matches []ContainerRef, rep *Report) error {
	if len(matches) == 0 {
		return nil
	}
	c := matches[0]
	if !c.Running {
		if opts.Containers != nil {
			if err := opts.Containers.Remove(ctx, c.ID, false); err != nil {
				opts.Logger.Warn("recovery.stopped.rm_failed",
					slog.String("session_id", row.id),
					slog.String("container_id", c.ID),
					slog.String("error", err.Error()))
			}
		}
		return nil
	}
	sock := row.controlSockPath.String
	if sock == "" {
		sock = filepath.Join(opts.SessionsDir, row.id, "control", "agentd.sock")
	}
	if opts.Containers != nil {
		if err := opts.Containers.Adopt(ctx, row.id, sock, defaultAdoptTimeout); err != nil {
			opts.Logger.Warn("recovery.unexpected_adopt_failed",
				slog.String("session_id", row.id),
				slog.String("container_id", c.ID),
				slog.String("error", err.Error()))
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = opts.Containers.Stop(stopCtx, c.ID, stopGrace)
			_ = opts.Containers.Remove(stopCtx, c.ID, true)
			cancel()
			return nil
		}
	}
	if err := updateSessionRunning(ctx, opts.Store, row.id, c.ID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}
	rep.Adopted++
	rep.Adoptions = append(rep.Adoptions, Adoption{
		SessionID:    row.id,
		ContainerID:  c.ID,
		SockPath:     sock,
		SessionToken: row.sessionToken,
	})
	logLifecycle(opts.Store, row.id, opts.Now(), "reconciled", `{"from":"stopped","to":"running","reason":"unexpected_running_container"}`)
	opts.Logger.Info("recovery.unexpected_adopted",
		slog.String("session_id", row.id),
		slog.String("container_id", c.ID))
	return nil
}

func updateSessionStopped(ctx context.Context, st *store.Store, sessionID, reason string) error {
	if st == nil {
		return nil
	}
	_, err := st.DB().ExecContext(ctx,
		`UPDATE sessions SET status='stopped', container_id=NULL, last_error=? WHERE id=?`,
		reason, sessionID)
	return err
}

func updateSessionRunning(ctx context.Context, st *store.Store, sessionID, containerID string) error {
	if st == nil {
		return nil
	}
	_, err := st.DB().ExecContext(ctx,
		`UPDATE sessions SET status='running', container_id=? WHERE id=?`,
		containerID, sessionID)
	return err
}

func logLifecycle(st *store.Store, sessionID string, now time.Time, event, detailJSON string) {
	if st == nil {
		return
	}
	_, _ = st.DB().Exec(`INSERT INTO session_lifecycle (session_id, at, event, detail_json) VALUES (?, ?, ?, ?)`,
		sessionID, now.Format(time.RFC3339Nano), event, detailJSON)
}

func sweepOrphanContainers(ctx context.Context, opts Options, containers map[string][]ContainerRef, known map[string]struct{}) int {
	if opts.Containers == nil || containers == nil {
		return 0
	}
	count := 0
	for sessionID, refs := range containers {
		if _, ok := known[sessionID]; ok {
			continue
		}
		for _, c := range refs {
			if err := opts.Containers.Remove(ctx, c.ID, true); err != nil {
				opts.Logger.Warn("recovery.orphan_container.rm_failed",
					slog.String("session_id", sessionID),
					slog.String("container_id", c.ID),
					slog.String("error", err.Error()))
				continue
			}
			count++
			opts.Logger.Info("recovery.orphan_container_removed",
				slog.String("session_id", sessionID),
				slog.String("container_id", c.ID))
		}
	}
	return count
}

func sweepOrphanNetworks(ctx context.Context, opts Options, networks []NetworkRef, known map[string]struct{}) int {
	if opts.Containers == nil || len(networks) == 0 {
		return 0
	}
	count := 0
	for _, n := range networks {
		sid := n.SessionID
		if sid == "" {
			sid = strings.TrimPrefix(n.Name, "agentctl-")
		}
		if _, ok := known[sid]; ok {
			continue
		}
		if err := opts.Containers.NetworkRemove(ctx, n.ID); err != nil {
			opts.Logger.Warn("recovery.orphan_network.rm_failed",
				slog.String("network_id", n.ID),
				slog.String("name", n.Name),
				slog.String("error", err.Error()))
			continue
		}
		count++
		opts.Logger.Info("recovery.orphan_network_removed",
			slog.String("network_id", n.ID),
			slog.String("name", n.Name))
	}
	return count
}

func sweepOrphanDirs(opts Options, known map[string]struct{}) int {
	entries, err := os.ReadDir(opts.SessionsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			opts.Logger.Warn("recovery.dirs.read_failed", slog.String("dir", opts.SessionsDir), slog.String("error", err.Error()))
		}
		return 0
	}
	orphansDir := filepath.Join(opts.SessionsDir, ".orphans")
	count := 0
	ts := opts.Now().UTC().Format("20060102T150405Z")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if _, ok := known[name]; ok {
			continue
		}
		if err := os.MkdirAll(orphansDir, 0o700); err != nil {
			opts.Logger.Warn("recovery.orphans_mkdir_failed", slog.String("error", err.Error()))
			return count
		}
		src := filepath.Join(opts.SessionsDir, name)
		dst := filepath.Join(orphansDir, name+"-"+ts)
		if err := os.Rename(src, dst); err != nil {
			opts.Logger.Warn("recovery.orphan_dir.move_failed",
				slog.String("src", src),
				slog.String("dst", dst),
				slog.String("error", err.Error()))
			continue
		}
		count++
		opts.Logger.Info("recovery.orphan_dir_moved",
			slog.String("session_id", name),
			slog.String("dst", dst))
	}
	return count
}
