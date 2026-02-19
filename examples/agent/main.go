package main

import (
	"fmt"
	"os"
	"time"

	"github.com/pranavp10/statusline"
)

// Simulates an AI agent streaming text while using tools in the background.
func main() {
	status := statusline.New(os.Stdout)
	status.Start()
	defer status.Stop()

	// Simulate agent thinking
	status.Set("Reading src/main.go")
	time.Sleep(800 * time.Millisecond)

	// Stream some text while tools run in the background
	streamText(status, "I've analyzed your codebase. Here's what I found:\n\n")

	status.Set("Searching for references...")
	streamText(status, "The main entry point initializes the HTTP server and sets up routing. ")
	streamText(status, "There are a few issues I'd like to address:\n\n")

	status.Set("Reading config/database.go")
	streamText(status, "1. The database connection pool isn't properly configured for production loads. ")
	streamText(status, "You're using the default pool size of 5, but with your traffic patterns ")
	streamText(status, "you'd want at least 25.\n\n")

	status.Set("Reading middleware/auth.go")
	time.Sleep(600 * time.Millisecond)
	streamText(status, "2. The auth middleware is checking tokens synchronously on every request. ")
	streamText(status, "Consider adding a local cache with a short TTL.\n\n")

	status.Set("Writing changes...")
	time.Sleep(1 * time.Second)
	streamText(status, "I've made both changes. Run `go test ./...` to verify.\n")
}

func streamText(w *statusline.StatusLine, text string) {
	for _, ch := range text {
		fmt.Fprint(w, string(ch))
		time.Sleep(15 * time.Millisecond)
	}
}
