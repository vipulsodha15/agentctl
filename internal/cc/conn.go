package cc

import (
	"errors"
	"net"
	"sync"
)

type Conn interface {
	Send(f Frame) error
	Recv() (Frame, error)
	Close() error
	SessionID() string
}

type conn struct {
	sessionID string
	raw       net.Conn
	reader    *FrameReader
	writer    *FrameWriter
	closeOnce sync.Once
	closed    chan struct{}
}

func newConn(sessionID string, raw net.Conn) *conn {
	return &conn{
		sessionID: sessionID,
		raw:       raw,
		reader:    NewFrameReader(raw),
		writer:    NewFrameWriter(raw),
		closed:    make(chan struct{}),
	}
}

func (c *conn) SessionID() string { return c.sessionID }

func (c *conn) Send(f Frame) error {
	select {
	case <-c.closed:
		return net.ErrClosed
	default:
	}
	return c.writer.Write(f)
}

func (c *conn) Recv() (Frame, error) {
	return c.reader.Read()
}

func (c *conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.raw.Close()
	})
	return err
}

// closedConnError reports whether an error is the result of using a closed
// network connection. The net.ErrClosed sentinel is the canonical signal.
func closedConnError(err error) bool {
	return errors.Is(err, net.ErrClosed)
}
