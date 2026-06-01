// Command psxdh is the psxdownloadhelper CLI entry point.
// See docs/configuration.md for the CLI surface and docs/architecture.md
// for how the proxy command wires the internal packages together.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
