package main

import (
	"fmt"
	"os"
	"time"

	"github.com/pranavp10/statusline"
)

func main() {
	status := statusline.New(os.Stdout)
	status.Start()
	defer status.Stop()

	status.Set("Connecting...")
	time.Sleep(500 * time.Millisecond)

	status.Set("Downloading assets...")
	for i := 1; i <= 10; i++ {
		fmt.Fprintln(status, "Downloaded file", i)
		time.Sleep(200 * time.Millisecond)
	}

	status.Set("Compiling...")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(status, "Compiled module %d/5\n", i)
		time.Sleep(300 * time.Millisecond)
	}

	status.Set("Running tests...")
	time.Sleep(1 * time.Second)

	fmt.Fprintln(status, "")
	fmt.Fprintln(status, "✓ All tasks complete.")
}
