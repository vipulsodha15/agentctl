package cliclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/socksrv"
	"github.com/agentctl/agentctl/internal/ulidgen"
)

type Client struct {
	conn net.Conn
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

func (c *Client) Health() (proto.HealthResponse, error) {
	id := ulidgen.New()
	req := proto.Frame{V: proto.ProtocolVersion, ID: id, Kind: proto.KindRequest, Op: proto.OpHealth, Data: []byte("{}")}
	body, err := json.Marshal(req)
	if err != nil {
		return proto.HealthResponse{}, err
	}
	if err := socksrv.WriteFrame(c.conn, body); err != nil {
		return proto.HealthResponse{}, err
	}
	if err := c.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return proto.HealthResponse{}, err
	}
	respBytes, err := socksrv.ReadFrame(c.conn)
	if err != nil {
		return proto.HealthResponse{}, err
	}
	var frame proto.Frame
	if err := json.Unmarshal(respBytes, &frame); err != nil {
		return proto.HealthResponse{}, err
	}
	if frame.Kind == proto.KindError {
		var ed proto.ErrorData
		_ = json.Unmarshal(frame.Data, &ed)
		return proto.HealthResponse{}, fmt.Errorf("agentd error %s: %s", ed.Code, ed.Message)
	}
	if frame.Kind != proto.KindResponse {
		return proto.HealthResponse{}, fmt.Errorf("unexpected kind %q", frame.Kind)
	}
	var hr proto.HealthResponse
	if err := json.Unmarshal(frame.Data, &hr); err != nil {
		return proto.HealthResponse{}, err
	}
	return hr, nil
}

var ErrSocketUnreachable = errors.New("agentd socket unreachable")
