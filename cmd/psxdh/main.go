// Command psxdh is the psxdownloadhelper CLI entry point.
// See plan.md §5.1 and the implementation plan Step 1.7.
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
