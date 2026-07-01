//go:build !unix

package statusline

import (
	"os"
	"os/signal"
)

func notifyResize(ch chan<- os.Signal) {
	// Platforms without SIGWINCH do not provide resize notifications here.
}

func notifyTermination(ch chan<- os.Signal) {
	signal.Notify(ch, os.Interrupt)
}
