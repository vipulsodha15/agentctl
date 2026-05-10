package agentd

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/agentctl/agentctl/internal/cc"
	"github.com/agentctl/agentctl/internal/cm"
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
