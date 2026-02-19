# statusline

A pinned status line for Go CLIs. Content scrolls above, status stays at the bottom. No TUI framework needed.

The space between `fmt.Println` and [Bubbletea](https://github.com/charmbracelet/bubbletea).

```
┌──────────────────────────────────┐
│ Downloaded file 1                │
│ Downloaded file 2                │
│ Downloaded file 3                │  ← content scrolls normally
│ Downloaded file 4                │
│ Downloaded file 5                │
├──────────────────────────────────┤
│  ⠋ Compiling...                  │  ← pinned status line
└──────────────────────────────────┘
```

## Install

```bash
go get github.com/pranavp10/statusline
```

## Usage

```go
status := statusline.New(os.Stdout)
status.Start()
defer status.Stop()

status.Set("Downloading...")

// Write content through the status line — it scrolls above
fmt.Fprintln(status, "Got file 1")
fmt.Fprintln(status, "Got file 2")

status.Set("Compiling...")
fmt.Fprintln(status, "Built module A")

status.Set("Done")
```

`StatusLine` implements `io.Writer`, so you can pass it anywhere you'd pass `os.Stdout`:

```go
io.Copy(status, someReader)
log.SetOutput(status)
cmd.Stdout = status
```

## Options

```go
// Custom spinner
status := statusline.New(os.Stdout, statusline.WithSpinner(
    statusline.SpinnerLine,     // |, /, -, \
    100*time.Millisecond,
))

// No spinner (static text)
status := statusline.New(os.Stdout, statusline.WithoutSpinner())
status.Set("50% complete")

// Custom style
style := lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
status := statusline.New(os.Stdout, statusline.WithStyle(style))
```

### Built-in spinners

| Name | Frames |
|---|---|
| `SpinnerDots` (default) | ⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏ |
| `SpinnerLine` | \| / - \ |
| `SpinnerCircle` | ◐ ◓ ◑ ◒ |
| `SpinnerArrow` | ← ↖ ↑ ↗ → ↘ ↓ ↙ |

## How it works

Uses ANSI scroll regions to reserve the terminal's last row. Content written via `Write()` stays inside the scroll region and scrolls naturally. The status line is rendered on the reserved row via buffered escape sequences (single write per update, no flicker).

Handles terminal resize (`SIGWINCH`), cleanup on `SIGINT`/`SIGTERM`, and degrades to a passthrough `io.Writer` when stdout isn't a TTY.

## Examples

```bash
go run ./examples/basic     # simple download + compile simulation
go run ./examples/agent     # AI agent streaming text with tool activity
```

## License

MIT
