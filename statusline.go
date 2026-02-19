// Package statusline provides a pinned status line for Go CLIs.
//
// Content scrolls above, status stays at the bottom. No TUI framework needed.
// Think of it as the space between fmt.Println and Bubbletea.
//
// The status line works by setting an ANSI scroll region that reserves the
// terminal's last row. Content written via Write() flows into the scroll
// region naturally. The status line is updated independently via Set().
//
// All writes are synchronized internally — safe for concurrent use from
// multiple goroutines.
//
// When stdout is not a TTY (e.g. piped to a file), all operations degrade
// gracefully: Write() passes through, Set()/Start()/Stop() are no-ops.
package statusline

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Default braille spinner frames.
var defaultFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// StatusLine pins a single line at the bottom of the terminal while allowing
// normal content to scroll above it. It implements io.Writer so it can be
// used as a drop-in replacement for os.Stdout.
type StatusLine struct {
	w  io.Writer // underlying writer (typically os.Stdout)
	fd int       // file descriptor for terminal size queries

	// terminal state
	width  int
	height int
	isTTY  bool

	// status content
	text string

	// spinner
	frames   []string
	frameIdx int
	interval time.Duration
	noSpin   bool

	// styling
	style lipgloss.Style

	// state tracking
	active             bool
	statusRendered     bool
	statusBelowPartial bool
	lastWasNewline     bool
	partialWidth       int

	// concurrency
	mu     sync.Mutex
	ticker *time.Ticker
	done   chan struct{}
	wg     sync.WaitGroup

	// signal handling
	sigWinch chan os.Signal
	sigTerm  chan os.Signal
}

// New creates a StatusLine that writes to w. If w is an *os.File, it will be
// used for TTY detection and terminal size queries. If w is not a TTY, all
// operations are no-ops and Write() passes through directly.
//
// Typically called with os.Stdout:
//
//	status := statusline.New(os.Stdout)
func New(w io.Writer, opts ...Option) *StatusLine {
	s := &StatusLine{
		w:              w,
		frames:         defaultFrames,
		interval:       80 * time.Millisecond,
		style:          lipgloss.NewStyle().Faint(true),
		lastWasNewline: true,
		done:           make(chan struct{}),
	}

	// Detect TTY and get file descriptor
	if f, ok := w.(*os.File); ok {
		s.fd = int(f.Fd())
		s.isTTY = term.IsTerminal(s.fd)
	}

	for _, opt := range opts {
		opt(s)
	}

	if s.isTTY {
		s.width, s.height, _ = term.GetSize(s.fd)
	}

	return s
}

// Start activates the scroll region, reserving the bottom row for the status
// line. Content written via Write() will scroll in the region above.
//
// Must call Stop() to restore normal terminal behavior. Using defer is
// recommended:
//
//	status.Start()
//	defer status.Stop()
func (s *StatusLine) Start() {
	if !s.isTTY {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active {
		return
	}

	s.active = true

	// Set scroll region to all rows except the last
	fmt.Fprintf(s.w, "\033[1;%dr", s.height-1)
	// Move cursor to top-left of scroll region
	fmt.Fprint(s.w, "\033[1;1H")

	// Install SIGWINCH handler for terminal resize
	s.sigWinch = make(chan os.Signal, 1)
	signal.Notify(s.sigWinch, syscall.SIGWINCH)

	// Install SIGINT/SIGTERM safety net to reset scroll region
	s.sigTerm = make(chan os.Signal, 1)
	signal.Notify(s.sigTerm, syscall.SIGINT, syscall.SIGTERM)

	// Start background goroutine for spinner animation + signal handling
	s.wg.Add(1)
	go s.loop()
}

// Stop deactivates the scroll region and restores normal terminal behavior.
// Safe to call multiple times. Blocks until the background goroutine exits.
func (s *StatusLine) Stop() {
	if !s.isTTY {
		return
	}

	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	s.active = false
	s.mu.Unlock()

	// Signal goroutine to stop and wait for it
	close(s.done)
	s.wg.Wait()

	// Clean up signals
	if s.sigWinch != nil {
		signal.Stop(s.sigWinch)
	}
	if s.sigTerm != nil {
		signal.Stop(s.sigTerm)
	}

	// Stop ticker
	if s.ticker != nil {
		s.ticker.Stop()
	}

	// Final cleanup under lock
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetTerminal()
}

// Set updates the status line text. Thread-safe. Can be called from any
// goroutine at any time while the status line is active.
func (s *StatusLine) Set(text string) {
	if !s.isTTY {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.text = text
	if s.active {
		s.redraw()
	}
}

// Clear removes the status text. The bottom row remains reserved.
func (s *StatusLine) Clear() {
	s.Set("")
}

// Write implements io.Writer. Content flows into the scroll region above the
// status line. Writes are synchronized with status updates to prevent
// interleaved ANSI sequences.
//
// When not a TTY, writes pass through directly to the underlying writer.
func (s *StatusLine) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isTTY || !s.active {
		return s.w.Write(p)
	}

	var buf bytes.Buffer

	// Clear status if rendered
	if s.statusRendered {
		s.writeClearStatus(&buf)
	}

	// Write content
	buf.Write(p)

	// Track whether last byte was a newline (for redraw positioning)
	if len(p) > 0 {
		s.trackContent(p)
	}

	// Redraw status
	s.writeRedraw(&buf)

	_, err = s.w.Write(buf.Bytes())
	return len(p), err
}

// loop is the background goroutine that handles spinner animation and signals.
func (s *StatusLine) loop() {
	defer s.wg.Done()

	if !s.noSpin {
		s.ticker = time.NewTicker(s.interval)
	}

	var tickCh <-chan time.Time
	if s.ticker != nil {
		tickCh = s.ticker.C
	}

	for {
		select {
		case <-s.done:
			return
		case <-tickCh:
			s.mu.Lock()
			s.frameIdx++
			if s.active {
				s.redraw()
			}
			s.mu.Unlock()
		case <-s.sigWinch:
			s.mu.Lock()
			s.handleResize()
			s.mu.Unlock()
		case <-s.sigTerm:
			// Safety net: reset scroll region before process exits
			s.mu.Lock()
			s.resetTerminal()
			s.mu.Unlock()
			// Re-raise the signal with default handler
			signal.Reset(syscall.SIGINT, syscall.SIGTERM)
			p, _ := os.FindProcess(os.Getpid())
			p.Signal(syscall.SIGINT)
			return
		}
	}
}

// redraw renders the status line on the bottom row. Must be called with mu held.
// Writes directly to s.w — use writeRedraw() for buffered writes.
func (s *StatusLine) redraw() {
	var buf bytes.Buffer
	s.writeRedraw(&buf)
	s.w.Write(buf.Bytes())
}

// writeRedraw writes the status line escape sequence to buf. Must be called with mu held.
func (s *StatusLine) writeRedraw(buf *bytes.Buffer) {
	content := s.renderStatus()

	if s.lastWasNewline {
		fmt.Fprintf(buf, "\r\033[K%s", content)
		s.statusBelowPartial = false
	} else {
		col := (s.partialWidth % s.width) + 1
		fmt.Fprintf(buf, "\n\r\033[K%s\033[1A\033[%dG", content, col)
		s.statusBelowPartial = true
	}

	s.statusRendered = true
}

// writeClearStatus writes the escape sequence to clear the status line to buf.
// Must be called with mu held.
func (s *StatusLine) writeClearStatus(buf *bytes.Buffer) {
	if s.statusBelowPartial {
		// Status is on the line below partial content — move down, clear, move back
		col := (s.partialWidth % s.width) + 1
		fmt.Fprintf(buf, "\n\r\033[K\033[1A\033[%dG", col)
	} else {
		// Status is on current line — just clear it
		buf.WriteString("\r\033[K")
	}
	s.statusRendered = false
}

// renderStatus builds the formatted status string.
func (s *StatusLine) renderStatus() string {
	if s.text == "" && s.noSpin {
		return ""
	}

	var parts []string

	if !s.noSpin {
		frame := s.frames[s.frameIdx%len(s.frames)]
		parts = append(parts, frame)
	}

	if s.text != "" {
		parts = append(parts, s.text)
	}

	raw := "  " + strings.Join(parts, " ")

	// Truncate by visible rune count to prevent wrapping
	runes := []rune(raw)
	if s.width > 0 && len(runes) > s.width-1 {
		raw = string(runes[:s.width-1])
	}

	return s.style.Render(raw)
}

// trackContent updates cursor tracking state based on written content.
// Must be called with mu held.
func (s *StatusLine) trackContent(p []byte) {
	for _, b := range p {
		if b == '\n' {
			s.lastWasNewline = true
			s.partialWidth = 0
		} else {
			s.lastWasNewline = false
			s.partialWidth++
		}
	}
}

// handleResize re-queries terminal size and re-sets the scroll region.
// Must be called with mu held.
func (s *StatusLine) handleResize() {
	w, h, err := term.GetSize(s.fd)
	if err != nil {
		return
	}
	s.width = w
	s.height = h

	// Re-set scroll region for new dimensions
	fmt.Fprintf(s.w, "\033[1;%dr", s.height-1)

	if s.active {
		s.redraw()
	}
}

// resetTerminal restores the terminal to normal state. Must be called with mu held.
// Safe to call multiple times.
func (s *StatusLine) resetTerminal() {
	// Reset scroll region to full screen
	fmt.Fprint(s.w, "\033[r")
	// Move to bottom and print newline for clean shell prompt
	fmt.Fprintf(s.w, "\033[%d;1H\n", s.height)
}
