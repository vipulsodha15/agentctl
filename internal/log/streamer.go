package log

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

type SessionLogStreamer struct {
	SessionsDir string
	PollEvery   time.Duration
}

func (s *SessionLogStreamer) Stream(ctx context.Context, sessionID string, follow bool, send func(line []byte) error) error {
	if sessionID == "" {
		return errors.New("session id required")
	}
	path := filepath.Join(s.SessionsDir, sessionID, "agentd.log")
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if err := send(line); err != nil {
				return err
			}
		}
		if err == io.EOF {
			if !follow {
				return nil
			}
			poll := s.PollEvery
			if poll == 0 {
				poll = 500 * time.Millisecond
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(poll):
			}
			continue
		}
		if err != nil {
			return err
		}
	}
}
