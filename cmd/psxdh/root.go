package main

import "github.com/spf13/cobra"

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "psxdh",
		Short:         "PlayStation download helper proxy",
		Long:          "psxdh proxies PlayStation CDN traffic, captures URLs so an external downloader (FDM, aria2, IDM, …) can fetch them on the PC, watches a library folder for the downloaded files, and serves them back to the console over LAN with full Range support.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newVersionCmd())
	cmd.AddCommand(newProxyCmd())
	cmd.AddCommand(newDoctorCmd())
	cmd.AddCommand(newProbeCmd())
	return cmd
}
