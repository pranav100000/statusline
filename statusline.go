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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

const (
	defaultInterval = 80 * time.Millisecond
	defaultWidth    = 80
	defaultHeight   = 24
	minWidth        = 1
	minHeight       = 2
)

// Default braille spinner frames. The slice is never assigned directly to a
// StatusLine so callers cannot mutate a running status line by changing a
// package-level spinner slice.
var defaultFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// StatusLine pins a single line at the bottom of the terminal while allowing
// normal content to scroll above it. It implements io.Writer so it can be
// used as a drop-in replacement for os.Stdout.
type StatusLine struct {
	w  io.Writer // underlying writer (typically os.Stdout)
	fd int       // file descriptor for terminal size queries

	// terminal state
	width     int
	height    int
	isTTY     bool
	cursorRow func() int

	// status content
	text string
	raw  bool

	// spinner
	frames   []string
	frameIdx int
	interval time.Duration
	noSpin   bool

	// styling
	style    lipgloss.Style
	ellipsis string

	// state tracking
	active         bool
	paused         bool
	statusRendered bool

	// concurrency
	mu   sync.Mutex
	done chan struct{}
	wg   sync.WaitGroup

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
		w:        w,
		frames:   cloneStrings(defaultFrames),
		interval: defaultInterval,
		style:    lipgloss.NewStyle().Faint(true),
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
		s.updateTerminalSize()
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

	s.updateTerminalSize()
	s.active = true
	s.done = make(chan struct{})

	// Query current cursor position before DECSTBM resets it to 1;1
	cursorRow := s.currentCursorRow()

	var buf bytes.Buffer

	// If cursor is on the last row (status bar territory), scroll up to make room
	if cursorRow >= s.height {
		fmt.Fprint(&buf, "\n")
		cursorRow = s.height - 1
	}

	// Fallback: if query failed, start at bottom of scroll region
	if cursorRow == 0 {
		cursorRow = s.height - 1
	}

	// Install SIGWINCH handler for terminal resize
	s.sigWinch = make(chan os.Signal, 1)
	notifyResize(s.sigWinch)

	// Install SIGINT/SIGTERM safety net to reset scroll region
	s.sigTerm = make(chan os.Signal, 1)
	notifyTermination(s.sigTerm)

	// Set scroll region to all rows except the last
	fmt.Fprintf(&buf, "\033[1;%dr", s.height-1)
	// Restore cursor to where it was (DECSTBM resets to 1;1)
	fmt.Fprintf(&buf, "\033[%d;1H", cursorRow)
	if s.text != "" {
		s.writeRedraw(&buf)
	}
	s.w.Write(buf.Bytes())

	// Start background goroutine for spinner animation + signal handling
	s.wg.Add(1)
	go s.loop(s.done, s.sigWinch, s.sigTerm)
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
	done := s.done
	sigWinch := s.sigWinch
	sigTerm := s.sigTerm
	s.active = false
	s.paused = false
	s.mu.Unlock()

	// Signal goroutine to stop and wait for it
	if done != nil {
		close(done)
	}
	s.wg.Wait()

	// Clean up signals
	if sigWinch != nil {
		signal.Stop(sigWinch)
	}
	if sigTerm != nil {
		signal.Stop(sigTerm)
	}

	// Final cleanup under lock
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetTerminal()
	s.done = nil
	s.sigWinch = nil
	s.sigTerm = nil
}

// Pause temporarily clears the status line and resets the scroll region,
// allowing other programs (e.g. interactive prompts) to use the full terminal.
// The background goroutine stays alive — call Resume() to restore the status line.
func (s *StatusLine) Pause() {
	if !s.isTTY {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.active || s.paused {
		return
	}
	s.paused = true

	var buf bytes.Buffer
	fmt.Fprint(&buf, "\0337")                       // DECSC — save cursor
	fmt.Fprintf(&buf, "\033[%d;1H\033[K", s.height) // clear status row
	fmt.Fprint(&buf, "\033[r")                      // reset scroll region (cursor → 1;1)
	fmt.Fprint(&buf, "\0338")                       // DECRC — restore cursor
	s.w.Write(buf.Bytes())
	s.statusRendered = false
}

// Resume restores the status line after a Pause(). Re-queries terminal size
// (which may have changed), re-sets the scroll region, and redraws.
func (s *StatusLine) Resume() {
	if !s.isTTY {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.active || !s.paused {
		return
	}
	s.paused = false

	// Re-query terminal size (may have changed while paused).
	s.updateTerminalSize()
	s.statusRendered = false

	// Re-set scroll region and redraw in one write
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\033[1;%dr", s.height-1)
	s.writeRedraw(&buf)
	s.w.Write(buf.Bytes())
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
	s.raw = false
	if s.active && !s.paused {
		s.redraw()
	}
}

// SetRaw updates the status line with pre-styled text. Unlike Set(), the text
// is not passed through the lipgloss style — it is rendered as-is (with an
// optional spinner prefix). Use Width() to measure available space when
// formatting your own styled text.
func (s *StatusLine) SetRaw(text string) {
	if !s.isTTY {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.text = text
	s.raw = true
	if s.active && !s.paused {
		s.redraw()
	}
}

// Width returns the current terminal width. Useful for pre-formatting styled
// text for SetRaw().
func (s *StatusLine) Width() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.width
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
	if s.paused {
		return s.w.Write(p)
	}

	var buf bytes.Buffer

	// Clear status if rendered
	if s.statusRendered {
		s.writeClearStatus(&buf)
	}

	// Write content
	buf.Write(p)

	// Redraw status
	s.writeRedraw(&buf)

	if buf.Len() == 0 {
		return len(p), nil
	}
	written, err := s.w.Write(buf.Bytes())
	if err != nil {
		return 0, err
	}
	if written != buf.Len() {
		return 0, io.ErrShortWrite
	}
	return len(p), err
}

// loop is the background goroutine that handles spinner animation and signals.
func (s *StatusLine) loop(done <-chan struct{}, sigWinch <-chan os.Signal, sigTerm <-chan os.Signal) {
	defer s.wg.Done()

	var tickCh <-chan time.Time
	if !s.noSpin {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		tickCh = ticker.C
	}

	for {
		select {
		case <-done:
			return
		case <-tickCh:
			s.mu.Lock()
			s.advanceFrame()
			if s.active && !s.paused {
				s.redraw()
			}
			s.mu.Unlock()
		case <-sigWinch:
			s.mu.Lock()
			s.handleResize()
			s.mu.Unlock()
		case sig := <-sigTerm:
			// Safety net: reset scroll region before process exits
			s.mu.Lock()
			s.resetTerminal()
			s.mu.Unlock()
			// Re-raise the signal with default handler
			signal.Reset(sig)
			p, _ := os.FindProcess(os.Getpid())
			p.Signal(sig)
			return
		}
	}
}

// redraw renders the status line on the bottom row. Must be called with mu held.
// Writes directly to s.w — use writeRedraw() for buffered writes.
func (s *StatusLine) redraw() {
	var buf bytes.Buffer
	s.writeRedraw(&buf)
	if buf.Len() == 0 {
		return
	}
	s.w.Write(buf.Bytes())
}

// writeRedraw writes the status line escape sequence to buf. Must be called with mu held.
// Uses DECSC/DECRC to save and restore the cursor position, then draws the
// status on the fixed row s.height (outside the scroll region). This avoids
// relative cursor movement that breaks at terminal-width wrap boundaries.
func (s *StatusLine) writeRedraw(buf *bytes.Buffer) {
	content := s.renderStatus()
	if content == "" {
		if s.statusRendered {
			s.writeClearStatus(buf)
		}
		return
	}
	fmt.Fprintf(buf, "\0337\033[%d;1H\033[K%s\0338", s.height, content)
	s.statusRendered = true
}

// writeClearStatus writes the escape sequence to clear the status line to buf.
// Must be called with mu held.
func (s *StatusLine) writeClearStatus(buf *bytes.Buffer) {
	fmt.Fprintf(buf, "\0337\033[%d;1H\033[K\0338", s.height)
	s.statusRendered = false
}

// renderStatus builds the formatted status string.
func (s *StatusLine) renderStatus() string {
	frame := s.spinnerFrame()

	// Raw mode: text is pre-styled, only add optional spinner prefix
	if s.raw && s.text != "" {
		var prefix string
		if frame != "" {
			prefix = s.style.Render("  "+frame) + " "
		}
		result := prefix + s.text
		if s.width > 0 && ansi.StringWidth(result) > s.width-1 {
			result = ansi.Truncate(result, s.width-1, s.ellipsis)
		}
		return result
	}

	if s.text == "" && frame == "" {
		return ""
	}

	var parts []string

	if frame != "" {
		parts = append(parts, frame)
	}

	if s.text != "" {
		parts = append(parts, s.text)
	}

	raw := "  " + strings.Join(parts, " ")

	// Truncate by visible width to prevent wrapping
	if s.width > 0 && ansi.StringWidth(raw) > s.width-1 {
		raw = ansi.Truncate(raw, s.width-1, s.ellipsis)
	}

	return s.style.Render(raw)
}

// spinnerFrame returns the current spinner frame, or an empty string when the
// spinner is disabled or has no frames.
func (s *StatusLine) spinnerFrame() string {
	if s.noSpin || len(s.frames) == 0 {
		return ""
	}
	idx := s.frameIdx % len(s.frames)
	if idx < 0 {
		idx += len(s.frames)
	}
	return s.frames[idx]
}

func (s *StatusLine) advanceFrame() {
	if len(s.frames) == 0 {
		s.frameIdx = 0
		return
	}
	if s.frameIdx < 0 || s.frameIdx >= len(s.frames)-1 {
		s.frameIdx = 0
		return
	}
	s.frameIdx++
}

func (s *StatusLine) currentCursorRow() int {
	if s.cursorRow != nil {
		return s.cursorRow()
	}
	return s.getCursorRow()
}

// getCursorRow queries the terminal for the current cursor row using DSR
// (Device Status Report). Returns 0 if the query fails.
func (s *StatusLine) getCursorRow() int {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0
	}
	defer tty.Close()

	ttyFd := int(tty.Fd())

	oldState, err := term.MakeRaw(ttyFd)
	if err != nil {
		return 0
	}
	defer term.Restore(ttyFd, oldState)

	// Request cursor position — terminal responds with \033[row;colR.
	fmt.Fprint(tty, "\033[6n")

	tty.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

	var response []byte
	buf := make([]byte, 1)
	for {
		n, err := tty.Read(buf)
		if err != nil || n == 0 {
			return 0
		}
		response = append(response, buf[0])
		if buf[0] == 'R' {
			break
		}
	}

	// Parse \033[row;colR
	resp := string(response)
	start := strings.Index(resp, "[")
	semi := strings.Index(resp, ";")
	if start < 0 || semi < 0 {
		return 0
	}
	row, err := strconv.Atoi(resp[start+1 : semi])
	if err != nil {
		return 0
	}
	return row
}

// handleResize re-queries terminal size and re-sets the scroll region.
// Must be called with mu held.
func (s *StatusLine) handleResize() {
	s.updateTerminalSize()
	if s.paused {
		return
	}

	// Save cursor, re-set scroll region, restore cursor.
	// DECSTBM resets cursor to 1;1, so we bracket with DECSC/DECRC.
	var buf bytes.Buffer
	fmt.Fprint(&buf, "\0337")
	fmt.Fprintf(&buf, "\033[1;%dr", s.height-1)
	fmt.Fprint(&buf, "\0338")
	s.writeRedraw(&buf)
	s.w.Write(buf.Bytes())
}

// resetTerminal restores the terminal to normal state. Must be called with mu held.
// Safe to call multiple times.
func (s *StatusLine) resetTerminal() {
	s.normalizeTerminalSize()
	var buf bytes.Buffer
	// Clear the status row before resetting scroll region
	fmt.Fprintf(&buf, "\033[%d;1H\033[K", s.height)
	// Reset scroll region to full screen
	fmt.Fprint(&buf, "\033[r")
	// Move to bottom and print newline for clean shell prompt
	fmt.Fprintf(&buf, "\033[%d;1H\n", s.height)
	s.w.Write(buf.Bytes())
	s.statusRendered = false
}

// updateTerminalSize refreshes the cached terminal size and preserves a
// sensible minimum when the query fails or returns an unusable dimension.
func (s *StatusLine) updateTerminalSize() {
	if w, h, err := term.GetSize(s.fd); err == nil {
		s.width = w
		s.height = h
	}
	s.normalizeTerminalSize()
}

func (s *StatusLine) normalizeTerminalSize() {
	if s.width < minWidth {
		s.width = defaultWidth
	}
	if s.height < minHeight {
		s.height = defaultHeight
	}
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
