package cli

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/agentctl/agentctl/internal/cli/tui"
	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
)

// rpcSender implements tui.Sender on top of the cliclient socket. We keep our
// own conn separate from the attach-stream one so per-RPC read deadlines on
// SendMessage / Interrupt don't fire on the stream goroutine's blocking Recv.
type rpcSender struct {
	mu        sync.Mutex
	socket    string
	sessionID string
	conn      *cliclient.Client
}

func newRPCSender(socketPath, sessionID string) *rpcSender {
	return &rpcSender{socket: socketPath, sessionID: sessionID}
}

func (r *rpcSender) ensureConn() (*cliclient.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn != nil {
		return r.conn, nil
	}
	c, err := cliclient.Dial(r.socket, 3*time.Second)
	if err != nil {
		return nil, err
	}
	r.conn = c
	return c, nil
}

func (r *rpcSender) drop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn != nil {
		_ = r.conn.Close()
		r.conn = nil
	}
}

func (r *rpcSender) Close() {
	r.drop()
}

func (r *rpcSender) Send(content string) error {
	c, err := r.ensureConn()
	if err != nil {
		return err
	}
	var resp proto.SendMessageResponse
	if err := c.Call(proto.OpSendMessage, proto.SendMessageRequest{
		SessionID: r.sessionID, Content: content, ClientID: "tui",
	}, &resp, 5*time.Second); err != nil {
		// A dropped connection survives one reconnect.
		r.drop()
		c2, derr := r.ensureConn()
		if derr != nil {
			return fmt.Errorf("send: %w", err)
		}
		if err2 := c2.Call(proto.OpSendMessage, proto.SendMessageRequest{
			SessionID: r.sessionID, Content: content, ClientID: "tui",
		}, &resp, 5*time.Second); err2 != nil {
			return err2
		}
	}
	return nil
}

func (r *rpcSender) Interrupt() error {
	c, err := r.ensureConn()
	if err != nil {
		return err
	}
	var resp proto.InterruptResponse
	if err := c.Call(proto.OpInterrupt, proto.InterruptRequest{SessionID: r.sessionID}, &resp, 5*time.Second); err != nil {
		r.drop()
		return err
	}
	return nil
}

func (r *rpcSender) ListSkills() ([]tui.Skill, error) {
	c, err := r.ensureConn()
	if err != nil {
		return nil, err
	}
	var resp proto.ListInstalledSkillsResponse
	if err := c.Call(proto.OpListInstalledSkills, proto.ListInstalledSkillsRequest{}, &resp, 5*time.Second); err != nil {
		r.drop()
		return nil, err
	}
	out := make([]tui.Skill, 0, len(resp.Skills))
	for _, s := range resp.Skills {
		out = append(out, tui.Skill{Name: s.Name})
	}
	return out, nil
}

var errSenderClosed = errors.New("sender closed")
