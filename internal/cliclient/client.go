package cliclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/socksrv"
	"github.com/agentctl/agentctl/internal/ulidgen"
)

type Client struct {
	conn net.Conn
	mu   sync.Mutex
}

func Dial(socketPath string, timeout time.Duration) (*Client, error) {
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial agentd socket %s: %w", socketPath, err)
	}
	return &Client{conn: conn}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) Conn() net.Conn { return c.conn }

func (c *Client) Health() (proto.HealthResponse, error) {
	var hr proto.HealthResponse
	if err := c.Call(proto.OpHealth, proto.HealthRequest{}, &hr, 5*time.Second); err != nil {
		return proto.HealthResponse{}, err
	}
	return hr, nil
}

func (c *Client) Call(op string, in any, out any, timeout time.Duration) error {
	id := ulidgen.New()
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req := proto.Frame{V: proto.ProtocolVersion, ID: id, Kind: proto.KindRequest, Op: op, Data: body}
	frameBytes, err := json.Marshal(req)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := socksrv.WriteFrame(c.conn, frameBytes); err != nil {
		return err
	}
	if timeout > 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
	}
	for {
		respBytes, err := socksrv.ReadFrame(c.conn)
		if err != nil {
			return err
		}
		var frame proto.Frame
		if err := json.Unmarshal(respBytes, &frame); err != nil {
			return err
		}
		if frame.ID != id {
			continue
		}
		if frame.Kind == proto.KindError {
			var ed proto.ErrorData
			_ = json.Unmarshal(frame.Data, &ed)
			return &APIError{Code: ed.Code, Message: ed.Message}
		}
		if frame.Kind != proto.KindResponse {
			return fmt.Errorf("unexpected kind %q", frame.Kind)
		}
		if out != nil {
			if err := json.Unmarshal(frame.Data, out); err != nil {
				return err
			}
		}
		return nil
	}
}

type StreamFrame struct {
	Kind    string
	Data    json.RawMessage
	EndCode string
	Err     error
}

type Stream struct {
	c    *Client
	id   string
	done chan struct{}
	once sync.Once
}

func (c *Client) StartStream(op string, in any) (*Stream, error) {
	id := ulidgen.New()
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	req := proto.Frame{V: proto.ProtocolVersion, ID: id, Kind: proto.KindRequest, Op: op, Data: body}
	frameBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if err := socksrv.WriteFrame(c.conn, frameBytes); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()
	_ = c.conn.SetReadDeadline(time.Time{})
	return &Stream{c: c, id: id, done: make(chan struct{})}, nil
}

func (s *Stream) Recv() StreamFrame {
	select {
	case <-s.done:
		return StreamFrame{EndCode: "closed"}
	default:
	}
	respBytes, err := socksrv.ReadFrame(s.c.conn)
	if err != nil {
		return StreamFrame{Err: err}
	}
	var frame proto.Frame
	if err := json.Unmarshal(respBytes, &frame); err != nil {
		return StreamFrame{Err: err}
	}
	if frame.ID != s.id {
		return s.Recv()
	}
	switch frame.Kind {
	case proto.KindStreamChunk:
		return StreamFrame{Kind: "chunk", Data: frame.Data}
	case proto.KindStreamEnd:
		return StreamFrame{EndCode: "end", Data: frame.Data}
	case proto.KindError:
		var ed proto.ErrorData
		_ = json.Unmarshal(frame.Data, &ed)
		return StreamFrame{Err: &APIError{Code: ed.Code, Message: ed.Message}}
	default:
		return StreamFrame{Err: fmt.Errorf("unexpected kind %q on stream", frame.Kind)}
	}
}

func (s *Stream) Close() {
	s.once.Do(func() { close(s.done) })
}

type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("agentd error %s: %s", e.Code, e.Message) }

var ErrSocketUnreachable = errors.New("agentd socket unreachable")
