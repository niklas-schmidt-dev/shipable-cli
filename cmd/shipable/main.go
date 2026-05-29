package main

import (
	"os"

	"github.com/niklas-schmidt-dev/shipable-cli/internal/shipablecli"
)

func main() {
	if err := shipablecli.Run(shipablecli.RunOptions{
		Args:   os.Args[1:],
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}); err != nil {
		_, _ = os.Stderr.WriteString("shipable: " + err.Error() + "\n")
		os.Exit(1)
	}
}
