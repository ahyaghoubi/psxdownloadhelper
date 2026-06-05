package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/captureio"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/export"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/persist"
)

func newExportCmd() *cobra.Command {
	var (
		from    string
		format  string
		title   string
		out     string
		libDir  string
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export captured URLs from a JSONL log",
		Long: `Read a capture.jsonl file and emit a downloader-ready URL list.

This is the offline half of the "capture at home, download at work" workflow:
run psxdh on the home network with capture.persist enabled, copy the JSONL
file to your work machine, then export it as either a plain text list or an
aria2 input file. No proxy needs to be running.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" {
				return errors.New("--from is required")
			}
			if format != "txt" && format != "aria2" {
				return fmt.Errorf("--format must be txt or aria2, got %q", format)
			}
			events, err := persist.ReadAll(from)
			if err != nil {
				return fmt.Errorf("read capture log: %w", err)
			}
			byTitle := captureio.AggregateByTitle(events)
			if title != "" {
				if _, ok := byTitle[title]; !ok {
					return fmt.Errorf("no pushable parts found for title %q", title)
				}
			}
			urls := captureio.URLsForExport(byTitle, title)
			if len(urls) == 0 {
				return errors.New("no pushable URLs found in capture log")
			}

			w, closeFn, err := openExportOutput(out)
			if err != nil {
				return err
			}
			defer closeFn()

			switch format {
			case "txt":
				err = export.WriteTxt(w, urls)
			case "aria2":
				err = export.WriteAria2(w, urls, libDir)
			}
			if err != nil {
				return fmt.Errorf("write %s: %w", format, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "exported %d URLs\n", len(urls))
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Path to capture.jsonl (required)")
	cmd.Flags().StringVar(&format, "format", "aria2", "Output format: txt or aria2")
	cmd.Flags().StringVar(&title, "title", "", "Filter to a single title (e.g. PPSA01649)")
	cmd.Flags().StringVar(&out, "out", "", "Output file path (default: stdout)")
	cmd.Flags().StringVar(&libDir, "library-dir", "", "Sets dir= in aria2 output (where to land downloads)")
	return cmd
}

func openExportOutput(path string) (io.Writer, func(), error) {
	if path == "" || path == "-" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("create %s: %w", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}
