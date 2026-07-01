//go:build unix

package statusline

import (
	"os"
	"os/signal"
	"syscall"
)

func notifyResize(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGWINCH)
}

func notifyTermination(ch chan<- os.Signal) {
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
}
