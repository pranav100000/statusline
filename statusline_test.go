package statusline

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

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
