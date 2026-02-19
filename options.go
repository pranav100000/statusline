package statusline

import (
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Option configures a StatusLine.
type Option func(*StatusLine)

// WithSpinner sets custom spinner frames and animation interval.
//
//	status := statusline.New(os.Stdout, statusline.WithSpinner(
//	    []string{"|", "/", "-", "\\"},
//	    100*time.Millisecond,
//	))
func WithSpinner(frames []string, interval time.Duration) Option {
	return func(s *StatusLine) {
		if len(frames) > 0 {
			s.frames = frames
		}
		if interval > 0 {
			s.interval = interval
		}
	}
}

// WithoutSpinner disables the spinner animation. The status line will show
// only the text set via Set(). Useful for progress percentages or other
// self-updating text.
//
//	status := statusline.New(os.Stdout, statusline.WithoutSpinner())
//	status.Set("50% complete")
func WithoutSpinner() Option {
	return func(s *StatusLine) {
		s.noSpin = true
	}
}

// WithStyle sets the lipgloss style used to render the status line text.
// The default is a faint/dim style.
//
//	style := lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
//	status := statusline.New(os.Stdout, statusline.WithStyle(style))
func WithStyle(style lipgloss.Style) Option {
	return func(s *StatusLine) {
		s.style = style
	}
}

// WithWriter sets the file descriptor used for terminal size queries.
// This is only needed if the io.Writer passed to New() is not an *os.File.
// In most cases you don't need this.
func WithFd(fd int) Option {
	return func(s *StatusLine) {
		s.fd = fd
		s.isTTY = true
	}
}

// Convenience spinner sets.
var (
	// SpinnerDots is the default braille dot spinner.
	SpinnerDots = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

	// SpinnerLine is a simple line spinner.
	SpinnerLine = []string{"|", "/", "-", "\\"}

	// SpinnerCircle is a growing circle spinner.
	SpinnerCircle = []string{"◐", "◓", "◑", "◒"}

	// SpinnerBounce is a bouncing bar spinner.
	SpinnerBounce = []string{"[    ]", "[=   ]", "[==  ]", "[=== ]", "[====]", "[ ===]", "[  ==]", "[   =]"}

	// SpinnerArrow is a rotating arrow spinner.
	SpinnerArrow = []string{"←", "↖", "↑", "↗", "→", "↘", "↓", "↙"}
)
