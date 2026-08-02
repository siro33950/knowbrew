package progress

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

const fallbackTerminalWidth = 80

// Display keeps one replaceable phase line on terminals and emits only phase
// boundaries and fixed messages when output is redirected.
type Display struct {
	writer  io.Writer
	tty     bool
	width   int
	verbose bool

	mu     sync.Mutex
	active string
}

func New(writer io.Writer, tty bool, width int, verbose bool) *Display {
	if tty && width <= 0 {
		width = fallbackTerminalWidth
	}
	return &Display{writer: writer, tty: tty, width: width, verbose: verbose}
}

// From preserves terminal-aware displays while treating an ordinary writer as
// redirected output. A nil writer produces a silent display.
func From(writer io.Writer) *Display {
	if display, ok := writer.(*Display); ok {
		return display
	}
	return New(writer, false, 0, false)
}

func (display *Display) Verbose() bool {
	return display != nil && display.verbose
}

func (display *Display) Start(line string) {
	if display == nil || display.writer == nil {
		return
	}
	display.mu.Lock()
	defer display.mu.Unlock()
	display.active = line
	if display.tty {
		display.renderLocked()
		return
	}
	_, _ = fmt.Fprintln(display.writer, line)
}

func (display *Display) Update(line string) {
	if display == nil || display.writer == nil {
		return
	}
	display.mu.Lock()
	defer display.mu.Unlock()
	display.active = line
	if display.tty {
		display.renderLocked()
	}
}

func (display *Display) Complete(line string) {
	if display == nil || display.writer == nil {
		return
	}
	display.mu.Lock()
	defer display.mu.Unlock()
	if display.tty {
		display.clearLocked()
		line = clip(line, display.width)
	}
	display.active = ""
	_, _ = fmt.Fprintln(display.writer, line)
}

func (display *Display) Errorf(format string, args ...any) {
	display.fixed(fmt.Sprintf(format, args...))
}

func (display *Display) Verbosef(format string, args ...any) {
	if display == nil || !display.verbose {
		return
	}
	display.fixed(fmt.Sprintf(format, args...))
}

// Abort removes an unfinished terminal progress line before the caller prints
// a fatal error.
func (display *Display) Abort() {
	if display == nil || display.writer == nil {
		return
	}
	display.mu.Lock()
	defer display.mu.Unlock()
	if display.tty && display.active != "" {
		display.clearLocked()
	}
	display.active = ""
}

// Write lets warnings and other fixed diagnostics share the display without
// overwriting an active terminal progress line.
func (display *Display) Write(data []byte) (int, error) {
	if display == nil || display.writer == nil || len(data) == 0 {
		return len(data), nil
	}
	text := strings.TrimRight(string(data), "\r\n")
	if text == "" {
		return len(data), nil
	}
	for _, line := range strings.Split(text, "\n") {
		display.fixed(line)
	}
	return len(data), nil
}

func (display *Display) fixed(line string) {
	if display == nil || display.writer == nil {
		return
	}
	display.mu.Lock()
	defer display.mu.Unlock()
	if display.tty {
		display.clearLocked()
	}
	_, _ = fmt.Fprintln(display.writer, line)
	if display.tty && display.active != "" {
		display.renderLocked()
	}
}

func (display *Display) renderLocked() {
	display.clearLocked()
	_, _ = fmt.Fprint(display.writer, clip(display.active, display.width))
}

func (display *Display) clearLocked() {
	_, _ = fmt.Fprint(display.writer, "\r\x1b[2K")
}

func clip(line string, width int) string {
	if width <= 1 {
		return ""
	}
	limit := width - 1
	runes := []rune(line)
	if len(runes) <= limit {
		return line
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}
