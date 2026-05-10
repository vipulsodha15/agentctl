package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/agentctl/agentctl/internal/cc"
	"github.com/agentctl/agentctl/internal/cm"
	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/skills"
	"github.com/agentctl/agentctl/internal/sm"
)

type cmAdapter struct {
	inner cm.Manager
}

func newCmAdapter(inner cm.Manager) *cmAdapter {
	return &cmAdapter{inner: inner}
}

func (a *cmAdapter) Create(ctx context.Context, spec sm.ContainerSpec) (sm.ContainerHandle, error) {
	mounts := make([]cm.Mount, 0, len(spec.Mounts))
	for _, m := range spec.Mounts {
		mt := cm.MountBind
		if m.Type == sm.MountVolume {
			mt = cm.MountVolume
		}
		mounts = append(mounts, cm.Mount{
			Type:     mt,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	c, err := a.inner.Create(ctx, cm.Spec{
		SessionID: spec.SessionID,
		ImageID:   spec.ImageID,
		Name:      spec.Name,
		Labels:    spec.Labels,
		EnvFile:   spec.EnvFile,
		Mounts:    mounts,
		MemBytes:  spec.MemBytes,
		CPUs:      spec.CPUs,
		NetworkID: spec.NetworkID,
	})
	if err != nil {
		return sm.ContainerHandle{}, err
	}
	return sm.ContainerHandle{ID: c.ID, Image: spec.ImageID}, nil
}

func (a *cmAdapter) Start(ctx context.Context, id string) error {
	return a.inner.Start(ctx, id)
}

func (a *cmAdapter) Stop(ctx context.Context, id string, grace time.Duration) error {
	return a.inner.Stop(ctx, id, grace)
}

func (a *cmAdapter) Remove(ctx context.Context, id string, force bool) error {
	return a.inner.Remove(ctx, id, force)
}

type ccSession struct {
	sessionToken string
	handler      sm.ControlHandler
}

type ccAdapter struct {
	inner cc.Server

	mu       sync.Mutex
	sessions map[string]*ccSession
	tokens   map[string]string
}

func newCcAdapter(inner cc.Server) *ccAdapter {
	return &ccAdapter{
		inner:    inner,
		sessions: map[string]*ccSession{},
		tokens:   map[string]string{},
	}
}

func (a *ccAdapter) Listen(sessionID, sockPath, sessionToken string, handler sm.ControlHandler) error {
	a.mu.Lock()
	a.sessions[sessionID] = &ccSession{sessionToken: sessionToken, handler: handler}
	a.tokens[sessionToken] = sessionID
	a.mu.Unlock()
	if err := a.inner.Listen(sessionID, sockPath); err != nil {
		a.mu.Lock()
		delete(a.sessions, sessionID)
		delete(a.tokens, sessionToken)
		a.mu.Unlock()
		return err
	}
	return nil
}

func (a *ccAdapter) Stop(sessionID string) error {
	a.mu.Lock()
	if s, ok := a.sessions[sessionID]; ok {
		delete(a.tokens, s.sessionToken)
		delete(a.sessions, sessionID)
	}
	a.mu.Unlock()
	return a.inner.Stop(sessionID)
}

func (a *ccAdapter) Verify(token string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.tokens[token]
	return id, ok
}

func (a *ccAdapter) Adopt(_ context.Context, sessionID string, conn cc.Conn, events <-chan cc.Frame) {
	a.mu.Lock()
	s := a.sessions[sessionID]
	a.mu.Unlock()
	if s == nil {
		_ = conn.Close()
		return
	}
	wrapped := &ccConnAdapter{
		conn:   conn,
		events: events,
		done:   make(chan struct{}),
	}
	s.handler(wrapped)
}

type ccConnAdapter struct {
	conn   cc.Conn
	events <-chan cc.Frame

	once sync.Once
	done chan struct{}
}

func (c *ccConnAdapter) Send(f sm.ControlFrame) error {
	select {
	case <-c.done:
		return errAdapterClosed
	default:
	}
	return c.conn.Send(cc.Frame{
		V:    f.V,
		Seq:  f.Seq,
		Kind: f.Kind,
		TS:   f.TS,
		Data: f.Data,
	})
}

func (c *ccConnAdapter) Recv() (sm.ControlFrame, error) {
	select {
	case fr, ok := <-c.events:
		if !ok {
			return sm.ControlFrame{}, errAdapterClosed
		}
		return sm.ControlFrame{
			V:    fr.V,
			Seq:  fr.Seq,
			Kind: fr.Kind,
			TS:   fr.TS,
			Data: fr.Data,
		}, nil
	case <-c.done:
		return sm.ControlFrame{}, errAdapterClosed
	}
}

func (c *ccConnAdapter) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.conn.Close()
}

var errAdapterClosed = errors.New("control adapter closed")

type mcpAdapter struct{ inner mcp.Registry }

func newMcpAdapter(r mcp.Registry) *mcpAdapter { return &mcpAdapter{inner: r} }

func (a *mcpAdapter) List(ctx context.Context) ([]byte, error) {
	entries, err := a.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"mcps": entries})
}

func (a *mcpAdapter) Add(ctx context.Context, body []byte) ([]byte, error) {
	var e mcp.Entry
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	if err := a.inner.Add(ctx, e); err != nil {
		return nil, err
	}
	out, err := a.inner.Get(ctx, e.Name)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (a *mcpAdapter) Update(ctx context.Context, name string, body []byte) ([]byte, error) {
	var upd mcp.EntryUpdate
	if err := json.Unmarshal(body, &upd); err != nil {
		return nil, err
	}
	if err := a.inner.Update(ctx, name, upd); err != nil {
		return nil, err
	}
	out, err := a.inner.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (a *mcpAdapter) Remove(ctx context.Context, name string, force bool) error {
	return a.inner.Remove(ctx, name, force)
}

type skillsAdapter struct{ inner skills.Manager }

func newSkillsAdapter(s skills.Manager) *skillsAdapter { return &skillsAdapter{inner: s} }

func (a *skillsAdapter) ListInstalled(_ context.Context) ([]byte, error) {
	list, err := a.inner.ListInstalled()
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"skills": list})
}

func (a *skillsAdapter) ListForSession(ctx context.Context, _ string) ([]byte, error) {
	return a.ListInstalled(ctx)
}

func (a *skillsAdapter) Add(_ context.Context, _ string, _ io.Reader) ([]byte, error) {
	return nil, errors.New("skills add via HTTP upload lands in M4")
}

func (a *skillsAdapter) Import(_ context.Context, body []byte) ([]byte, error) {
	var req struct {
		SourcePath string `json:"source_path"`
		Name       string `json:"name"`
		Force      bool   `json:"force"`
		DryRun     bool   `json:"dry_run"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	res, err := a.inner.Import(req.SourcePath, req.Name, skills.ImportOptions{Force: req.Force, DryRun: req.DryRun})
	if err != nil {
		return nil, err
	}
	return json.Marshal(res)
}

func (a *skillsAdapter) Validate(_ context.Context, name string) ([]byte, error) {
	res, err := a.inner.Validate(skills.ValidateSource{Name: name})
	if err != nil {
		return nil, err
	}
	return json.Marshal(res)
}

func (a *skillsAdapter) Export(_ context.Context, name string, w io.Writer) error {
	data, err := a.inner.Export(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func (a *skillsAdapter) Remove(_ context.Context, name string, _ bool) error {
	return a.inner.Remove(name)
}
