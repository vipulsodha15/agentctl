package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
)

// Run starts a fullscreen TUI attached to sessionID. Events are read off the
// AttachStream; sender is used to push user messages and interrupt requests
// back to agentd. The function blocks until the user quits (Ctrl+D / Ctrl+C),
// the stream ends, or ctx is cancelled.
//
// stderr receives any out-of-band diagnostics; the alt-screen UI never writes
// to stdout while running.
func Run(ctx context.Context, c *cliclient.Client, sessionID string, sender Sender, stderr io.Writer) int {
	// Resolve the terminal's background colour BEFORE Bubble Tea grabs stdin.
	// Glamour's AutoStyle calls termenv.HasDarkBackground() each render, which
	// issues an OSC 11 query every time (no cache by default). If any of
	// those queries fires after the program has started, the terminal's reply
	// (e.g. "]11;rgb:fdfd/f6f6/e3e3\") leaks back as typed characters into
	// the textarea. Installing a cached default Output makes the query happen
	// once, now, and subsequent calls hit the cache.
	termenv.SetDefaultOutput(termenv.NewOutput(os.Stdout, termenv.WithColorCache(true)))

	stream, err := c.StartStream(proto.OpAttachStream, proto.AttachStreamRequest{SessionID: sessionID})
	if err != nil {
		fmt.Fprintf(stderr, "attach: %v\n", err)
		return 1
	}

	model := New(sessionID, sender)
	prog := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	// Close the underlying socket connection when ctx is done so the blocking
	// stream.Recv unwinds. Same trick the streaming renderer uses.
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-stop:
		}
	}()
	defer close(stop)

	// Pump stream events into the program. tea.Program.Send is safe from any
	// goroutine.
	go func() {
		for {
			fr := stream.Recv()
			if fr.Err != nil {
				if ctx.Err() != nil || errors.Is(fr.Err, io.EOF) {
					prog.Send(streamEndMsg{})
					return
				}
				prog.Send(streamEndMsg{err: fr.Err})
				return
			}
			if fr.EndCode != "" {
				prog.Send(streamEndMsg{code: fr.EndCode})
				return
			}
			var ev proto.Event
			if err := json.Unmarshal(fr.Data, &ev); err != nil {
				continue
			}
			prog.Send(eventMsg{ev: ev})
		}
	}()

	if _, err := prog.Run(); err != nil {
		fmt.Fprintf(stderr, "tui: %v\n", err)
		return 1
	}
	if model.endNote != "" {
		fmt.Fprintln(stderr, model.endNote)
	}
	return 0
}
