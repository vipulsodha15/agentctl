package agentd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/docker/docker/pkg/stdcopy"

	"github.com/agentctl/agentctl/internal/cm"
	"github.com/agentctl/agentctl/internal/sm"
)

type containerLogStreamer struct {
	manager sm.Manager
	cm      cm.Manager
}

func newContainerLogStreamer(manager sm.Manager, cmMgr cm.Manager) *containerLogStreamer {
	return &containerLogStreamer{manager: manager, cm: cmMgr}
}

func (s *containerLogStreamer) Stream(ctx context.Context, sessionID string, follow bool, send func(line []byte) error) error {
	if s.manager == nil {
		return errors.New("container logs: session manager unavailable")
	}
	if s.cm == nil {
		return errors.New("container logs: docker manager unavailable")
	}
	detail, err := s.manager.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if detail.ContainerID == "" {
		return errors.New("container logs: session has no container")
	}
	rc, err := s.cm.Logs(ctx, detail.ContainerID, follow)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	return demuxAndSend(rc, send)
}

func demuxAndSend(rc io.ReadCloser, send func(line []byte) error) error {
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	var demuxErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, demuxErr = stdcopy.StdCopy(stdoutW, stderrW, rc)
		_ = stdoutW.Close()
		_ = stderrW.Close()
	}()
	sendErrCh := make(chan error, 2)
	go forwardLines(stdoutR, send, sendErrCh)
	go forwardLines(stderrR, send, sendErrCh)
	var sendErr error
	for i := 0; i < 2; i++ {
		if err := <-sendErrCh; err != nil && sendErr == nil {
			sendErr = err
		}
	}
	wg.Wait()
	if sendErr != nil {
		return sendErr
	}
	if demuxErr != nil && !errors.Is(demuxErr, io.EOF) {
		return demuxErr
	}
	return nil
}

func forwardLines(r io.Reader, send func([]byte) error, done chan<- error) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if serr := send(line); serr != nil {
				done <- serr
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				done <- nil
				return
			}
			done <- err
			return
		}
	}
}
