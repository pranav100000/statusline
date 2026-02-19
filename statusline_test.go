package statusline

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// ansiCsiRe matches CSI sequences: \033[ ... letter
var ansiCsiRe = regexp.MustCompile(`\033\[[^a-zA-Z]*[a-zA-Z]`)

// ansiEscRe matches ESC + single character sequences (e.g., \0337 DECSC, \0338 DECRC)
var ansiEscRe = regexp.MustCompile(`\033[^[\x1b]`)

// extractContent strips all ANSI sequences, control characters, spinner frames,
// and known status strings from raw output, then removes all spaces so that
// character-by-character streamed content can be reassembled. The caller checks
// for space-stripped versions of expected phrases.
func extractContent(b []byte, statuses []string) []byte {
	b = ansiCsiRe.ReplaceAll(b, nil)
	b = ansiEscRe.ReplaceAll(b, nil)
	b = bytes.ReplaceAll(b, []byte("\r"), nil)
	b = bytes.ReplaceAll(b, []byte("\n"), nil)
	for _, frame := range defaultFrames {
		b = bytes.ReplaceAll(b, []byte(frame), nil)
	}
	for _, s := range statuses {
		b = bytes.ReplaceAll(b, []byte(s), nil)
	}
	// Remove all spaces — status rendering inserts padding that ends up
	// between every content character after the above removals.
	b = bytes.ReplaceAll(b, []byte(" "), nil)
	return b
}

// stripSpaces removes all spaces from a string for content comparison.
func stripSpaces(s string) string {
	return strings.ReplaceAll(s, " ", "")
}

// writeRecorder records each individual Write() call separately.
// If flicker exists, you'll see multiple writes where there should be one.
type writeRecorder struct {
	mu     sync.Mutex
	writes [][]byte // each element is one Write() call
}

func (r *writeRecorder) Write(p []byte) (n int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]byte, len(p))
	copy(cp, p)
	r.writes = append(r.writes, cp)
	return len(p), nil
}

func (r *writeRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.writes)
}

func (r *writeRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = nil
}

func TestWriteNonTTY(t *testing.T) {
	var buf bytes.Buffer
	s := New(&buf)

	if s.isTTY {
		t.Fatal("expected non-TTY detection for bytes.Buffer")
	}

	// Set/Start/Stop should be no-ops
	s.Start()
	s.Set("hello")
	s.Stop()

	// Write should pass through
	msg := "hello world\n"
	n, err := s.Write([]byte(msg))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != len(msg) {
		t.Fatalf("Write returned %d, want %d", n, len(msg))
	}

	got := buf.String()
	if got != msg {
		t.Fatalf("got %q, want %q", got, msg)
	}

	// No ANSI escape codes should be present
	if strings.Contains(got, "\033[") {
		t.Fatalf("output contains ANSI codes: %q", got)
	}
}

func TestRenderStatusTruncation(t *testing.T) {
	s := &StatusLine{
		width:  20,
		height: 24,
		isTTY:  true,
		frames: defaultFrames,
		style:  lipgloss.NewStyle(),
	}
	s.text = "This is a very long status message that should be truncated"

	result := s.renderStatus()
	runes := []rune(result)
	if len(runes) > s.width-1 {
		t.Fatalf("renderStatus returned %d runes, want <= %d: %q", len(runes), s.width-1, result)
	}
}

func TestRenderStatusFormats(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		noSpin bool
		want   string // substring that must appear
		empty  bool   // expect empty string
	}{
		{
			name:   "spinner and text",
			text:   "Loading",
			noSpin: false,
			want:   "Loading",
		},
		{
			name:   "spinner only",
			text:   "",
			noSpin: false,
			want:   defaultFrames[0],
		},
		{
			name:   "text only no spinner",
			text:   "Progress",
			noSpin: true,
			want:   "Progress",
		},
		{
			name:   "empty no spinner",
			text:   "",
			noSpin: true,
			empty:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &StatusLine{
				width:  80,
				height: 24,
				isTTY:  true,
				frames: defaultFrames,
				style:  lipgloss.NewStyle(),
				noSpin: tt.noSpin,
			}
			s.text = tt.text

			result := s.renderStatus()
			if tt.empty {
				if result != "" {
					t.Fatalf("expected empty, got %q", result)
				}
				return
			}
			if !strings.Contains(result, tt.want) {
				t.Fatalf("result %q does not contain %q", result, tt.want)
			}
		})
	}
}

func TestTrackContent(t *testing.T) {
	tests := []struct {
		name           string
		writes         []string
		wantNewline    bool
		wantPartialW   int
	}{
		{
			name:         "newline terminated",
			writes:       []string{"hello\n"},
			wantNewline:  true,
			wantPartialW: 0,
		},
		{
			name:         "no newline",
			writes:       []string{"hello"},
			wantNewline:  false,
			wantPartialW: 5,
		},
		{
			name:         "multi-line newline terminated",
			writes:       []string{"abc\ndef\n"},
			wantNewline:  true,
			wantPartialW: 0,
		},
		{
			name:         "multi-line partial",
			writes:       []string{"abc\nde"},
			wantNewline:  false,
			wantPartialW: 2,
		},
		{
			name:         "just newline",
			writes:       []string{"\n"},
			wantNewline:  true,
			wantPartialW: 0,
		},
		{
			name:         "multi-write sequence",
			writes:       []string{"abc", "def\n", "gh"},
			wantNewline:  false,
			wantPartialW: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &StatusLine{
				lastWasNewline: true,
				partialWidth:   0,
			}
			for _, w := range tt.writes {
				s.trackContent([]byte(w))
			}
			if s.lastWasNewline != tt.wantNewline {
				t.Fatalf("lastWasNewline = %v, want %v", s.lastWasNewline, tt.wantNewline)
			}
			if s.partialWidth != tt.wantPartialW {
				t.Fatalf("partialWidth = %d, want %d", s.partialWidth, tt.wantPartialW)
			}
		})
	}
}

func TestSetAndClear(t *testing.T) {
	t.Run("direct struct access", func(t *testing.T) {
		s := &StatusLine{
			isTTY: true,
			style: lipgloss.NewStyle(),
		}
		// Set stores text (no Start needed for pure text storage via direct assignment)
		s.mu.Lock()
		s.text = "hello"
		s.mu.Unlock()

		s.mu.Lock()
		if s.text != "hello" {
			t.Fatalf("text = %q, want %q", s.text, "hello")
		}
		s.mu.Unlock()
	})

	t.Run("non-TTY no-op", func(t *testing.T) {
		var buf bytes.Buffer
		s := New(&buf)
		s.Set("should be ignored")
		if s.text != "" {
			t.Fatalf("Set on non-TTY should be no-op, got text = %q", s.text)
		}
		s.Clear()
		if s.text != "" {
			t.Fatalf("Clear on non-TTY should be no-op, got text = %q", s.text)
		}
	})
}

func TestStartStopIdempotent(t *testing.T) {
	var buf bytes.Buffer
	s := New(&buf)

	// On non-TTY, Start and Stop are no-ops. Should not panic.
	s.Stop()
	s.Stop()
	s.Start()
	s.Start()
	s.Stop()
}

func TestConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	s := New(&buf)

	const goroutines = 10
	const writesPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < writesPerGoroutine; j++ {
				fmt.Fprintf(s, "g%d w%d\n", id, j)
			}
		}(i)
	}
	wg.Wait()

	// Verify all data arrived
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	expected := goroutines * writesPerGoroutine
	if len(lines) != expected {
		t.Fatalf("got %d lines, want %d", len(lines), expected)
	}
}

func TestTruncationUnicode(t *testing.T) {
	s := &StatusLine{
		width:  10,
		height: 24,
		isTTY:  true,
		frames: defaultFrames,
		style:  lipgloss.NewStyle(),
	}
	s.text = "你好世界这是一个很长的字符串"

	result := s.renderStatus()
	if !utf8.ValidString(result) {
		t.Fatalf("result is not valid UTF-8: %q", result)
	}
	runes := []rune(result)
	if len(runes) > s.width-1 {
		t.Fatalf("renderStatus returned %d runes, want <= %d: %q", len(runes), s.width-1, result)
	}
}

// --- Flicker-detection tests ---
// Core invariant: every visible update must be a single Write() call to the
// underlying writer. Multiple writes = multiple repaints = flicker.

func TestWriteProducesSingleWrite(t *testing.T) {
	rec := &writeRecorder{}
	s := &StatusLine{
		w: rec, isTTY: true, active: true,
		width: 80, height: 24,
		frames:         defaultFrames,
		style:          lipgloss.NewStyle(),
		lastWasNewline: true,
		statusRendered: true, // status is already drawn
	}

	rec.reset()
	s.Write([]byte("hello\n"))

	if rec.count() != 1 {
		t.Errorf("Write produced %d underlying writes, want 1 (flicker!)", rec.count())
	}

	// Verify the single write contains all three parts
	blob := string(rec.writes[0])
	// Should contain: clear sequence + "hello\n" + redraw sequence
	if !strings.Contains(blob, "hello\n") {
		t.Error("content missing from write")
	}
	if !strings.Contains(blob, "\033[K") {
		t.Error("clear/redraw sequence missing from write")
	}
}

func TestSpinnerTickSingleWrite(t *testing.T) {
	rec := &writeRecorder{}
	s := &StatusLine{
		w: rec, isTTY: true, active: true,
		width: 80, height: 24,
		frames:         defaultFrames,
		style:          lipgloss.NewStyle(),
		lastWasNewline: true,
		text:           "Loading...",
	}

	rec.reset()
	// Simulate what the spinner goroutine does
	s.mu.Lock()
	s.frameIdx++
	s.redraw()
	s.mu.Unlock()

	if rec.count() != 1 {
		t.Errorf("spinner tick produced %d writes, want 1", rec.count())
	}
}

func TestRapidWritesEachSingleWrite(t *testing.T) {
	rec := &writeRecorder{}
	s := &StatusLine{
		w: rec, isTTY: true, active: true,
		width: 80, height: 24,
		frames:         defaultFrames,
		style:          lipgloss.NewStyle(),
		lastWasNewline: true,
		statusRendered: true,
	}

	for i := 0; i < 100; i++ {
		rec.reset()
		s.Write([]byte(fmt.Sprintf("line %d\n", i)))
		if rec.count() != 1 {
			t.Fatalf("write %d produced %d underlying writes, want 1", i, rec.count())
		}
	}
}

func TestPartialLineThenNewlineSingleWrites(t *testing.T) {
	rec := &writeRecorder{}
	s := &StatusLine{
		w: rec, isTTY: true, active: true,
		width: 80, height: 24,
		frames:         defaultFrames,
		style:          lipgloss.NewStyle(),
		lastWasNewline: true,
		statusRendered: true,
	}

	// Write partial line (no newline)
	rec.reset()
	s.Write([]byte("partial"))
	if rec.count() != 1 {
		t.Fatalf("partial write: %d underlying writes, want 1", rec.count())
	}

	// Write more to same line
	rec.reset()
	s.Write([]byte(" content"))
	if rec.count() != 1 {
		t.Fatalf("continuation write: %d underlying writes, want 1", rec.count())
	}

	// Finish the line
	rec.reset()
	s.Write([]byte("\n"))
	if rec.count() != 1 {
		t.Fatalf("newline write: %d underlying writes, want 1", rec.count())
	}
}

func TestSetProducesSingleWrite(t *testing.T) {
	rec := &writeRecorder{}
	s := &StatusLine{
		w: rec, isTTY: true, active: true,
		width: 80, height: 24,
		frames:         defaultFrames,
		style:          lipgloss.NewStyle(),
		lastWasNewline: true,
		statusRendered: true,
	}

	rec.reset()
	s.Set("new status text")
	if rec.count() != 1 {
		t.Errorf("Set produced %d writes, want 1", rec.count())
	}
}

func TestConcurrentWriteAndSetNoInterleaving(t *testing.T) {
	rec := &writeRecorder{}
	s := &StatusLine{
		w: rec, isTTY: true, active: true,
		width: 80, height: 24,
		frames:         defaultFrames,
		style:          lipgloss.NewStyle(),
		lastWasNewline: true,
		statusRendered: true,
	}

	var wg sync.WaitGroup
	// Writers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.Write([]byte(fmt.Sprintf("goroutine %d line %d\n", id, j)))
			}
		}(i)
	}
	// Status updaters
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.Set(fmt.Sprintf("status %d-%d", id, j))
			}
		}(i)
	}
	wg.Wait()

	// Structural guard: if save/restore cursor is ever added, catch interleaving
	for i, w := range rec.writes {
		blob := string(w)
		saves := strings.Count(blob, "\033[s")
		restores := strings.Count(blob, "\033[u")
		if saves != restores {
			t.Errorf("write %d has %d saves but %d restores — interleaved!", i, saves, restores)
		}
	}

	// Verify every write is non-empty
	for i, w := range rec.writes {
		if len(w) == 0 {
			t.Errorf("write %d is empty", i)
		}
	}

	// Verify total content arrived intact
	all := bytes.Join(rec.writes, nil)
	for i := 0; i < 5; i++ {
		for j := 0; j < 50; j++ {
			expected := fmt.Sprintf("goroutine %d line %d\n", i, j)
			if !bytes.Contains(all, []byte(expected)) {
				t.Errorf("missing content: %q", expected)
			}
		}
	}
}

func TestStreamingWithConcurrentStatusUpdates(t *testing.T) {
	rec := &writeRecorder{}
	s := &StatusLine{
		w: rec, isTTY: true, active: true,
		width: 80, height: 24,
		frames:         defaultFrames,
		style:          lipgloss.NewStyle(),
		lastWasNewline: true,
		statusRendered: true,
	}

	streamText := `Here's what I found after analyzing the codebase:

The database connection pool is configured in config/database.go
with a max of 25 connections. The pool uses a FIFO strategy
and connections are recycled every 5 minutes.

Key findings:
- Connection timeouts are set to 30 seconds
- Idle connections are closed after 10 minutes
- The pool size scales based on CPU count

Run go test to verify the changes work correctly.
`

	statuses := []string{
		"Reading src/main.go",
		"Searching for references...",
		"Analyzing imports...",
		"Reading config/database.go",
		"Writing changes...",
		"Formatting code...",
	}

	var wg sync.WaitGroup

	// Goroutine 1: stream text rune-by-rune
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, ch := range streamText {
			s.Write([]byte(string(ch)))
		}
	}()

	// Goroutine 2: cycle through status updates
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, status := range statuses {
			s.Set(status)
			time.Sleep(1 * time.Millisecond)
		}
	}()

	// Goroutine 3: simulate spinner ticks
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			s.mu.Lock()
			s.frameIdx++
			s.redraw()
			s.mu.Unlock()
			time.Sleep(2 * time.Millisecond)
		}
	}()

	wg.Wait()

	// Assertion 1: no empty writes
	for i, w := range rec.writes {
		if len(w) == 0 {
			t.Errorf("write %d is empty", i)
		}
	}

	// Assertion 2: all content arrives (after stripping ANSI and status text)
	// Since character-by-character writes interleave status redraws between
	// every char, we strip everything non-content (including spaces) and
	// compare against space-stripped phrases.
	content := extractContent(bytes.Join(rec.writes, nil), statuses)
	phrases := []string{
		"Here's what I found",
		"database connection pool",
		"Run go test to verify",
	}
	for _, phrase := range phrases {
		if !bytes.Contains(content, []byte(stripSpaces(phrase))) {
			t.Errorf("missing phrase in output: %q", phrase)
		}
	}

	// Assertion 3: no merged operations — each write has <= 2 occurrences of \033[K
	for i, w := range rec.writes {
		count := bytes.Count(w, []byte("\033[K"))
		if count > 2 {
			t.Errorf("write %d has %d \\033[K sequences (max 2), possible merged operations", i, count)
		}
	}

	// Assertion 4: escape sequence integrity — no write ends with a truncated escape
	for i, w := range rec.writes {
		if bytes.HasSuffix(w, []byte("\033")) {
			t.Errorf("write %d ends with truncated escape \\033", i)
		}
		if bytes.HasSuffix(w, []byte("\033[")) {
			t.Errorf("write %d ends with truncated escape \\033[", i)
		}
	}
}
