// Command psxdh is the psxdownloadhelper CLI entry point.
// See docs/configuration.md for the CLI surface and docs/architecture.md
// for how the proxy command wires the internal packages together.
package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
)

func main() {
	bareStart := defaultProxyWhenNoArgs()
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if bareStart {
			waitBeforeExit()
		}
		os.Exit(1)
	}
}

// defaultProxyWhenNoArgs makes double-click / Finder launches useful: running
// the binary with no subcommand starts `proxy` (the usual entry point).
func defaultProxyWhenNoArgs() bool {
	if len(os.Args) != 1 {
		return false
	}
	os.Args = append(os.Args, "proxy")
	return true
}

// waitBeforeExit keeps a console window open after a failed bare launch so
// Windows users who double-click the .exe can read the error message.
func waitBeforeExit() {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return
	}
	fmt.Fprintln(os.Stderr, "\nPress Enter to close…")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
