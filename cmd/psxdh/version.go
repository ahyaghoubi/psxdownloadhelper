package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// version is overridden at build time via `-ldflags "-X main.version=v1.2.3"`.
var version = "dev"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the psxdh version",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	}
}
