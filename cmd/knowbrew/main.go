package main

import (
	"fmt"
	"os"

	"github.com/siro33950/knowbrew/internal/adapters/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "knowbrew:", err)
		os.Exit(1)
	}
}
