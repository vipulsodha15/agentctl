package cli

import (
	"os"

	"github.com/mattn/go-isatty"
)

// stdoutIsTTY reports whether the user's stdout is connected to a terminal.
// The TUI requires a real TTY (alt-screen + raw mode); when stdout is piped
// or redirected we fall back to the line-based streaming renderer.
func stdoutIsTTY(env *Env) bool {
	f, ok := env.Stdout.(*os.File)
	if !ok {
		return false
	}
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}
