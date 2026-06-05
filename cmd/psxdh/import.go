package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newImportCmd() *cobra.Command {
	var (
		from      string
		master    string
		token     string
		enumerate bool
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Push a captured JSONL log into a running master",
		Long: `Read a capture.jsonl file and POST it to a running psxdh master's
/api/jobs/import endpoint. The master replays the events into its session store
and (optionally) enumerates and submits each title to its cluster.

This is the work-side counterpart of psxdh export: take the JSONL produced at
home, hand it to the running cluster master at work, and downloads start.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" {
				return errors.New("--from is required")
			}
			if master == "" {
				return errors.New("--url is required (master base URL, e.g. http://192.168.1.10:8081)")
			}

			f, err := os.Open(from)
			if err != nil {
				return fmt.Errorf("open %s: %w", from, err)
			}
			defer f.Close()

			body := &bytes.Buffer{}
			mw := multipart.NewWriter(body)
			part, err := mw.CreateFormFile("capture", "capture.jsonl")
			if err != nil {
				return fmt.Errorf("multipart: %w", err)
			}
			if _, err := io.Copy(part, f); err != nil {
				return fmt.Errorf("copy capture file: %w", err)
			}
			if err := mw.Close(); err != nil {
				return err
			}

			endpoint := strings.TrimRight(master, "/") + "/api/jobs/import"
			q := url.Values{}
			if enumerate {
				q.Set("enumerate", "true")
			} else {
				q.Set("enumerate", "false")
			}
			endpoint += "?" + q.Encode()

			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, endpoint, body)
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", mw.FormDataContentType())
			if token != "" {
				req.Header.Set("X-Psxdh-Token", token)
			}

			client := &http.Client{Timeout: 60 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("post import: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				msg, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("master rejected import (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
			}
			var res struct {
				Titles     int `json:"titles"`
				Parts      int `json:"parts"`
				Enumerated int `json:"enumerated"`
				Submitted  int `json:"submitted"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"imported %d title(s), %d part(s); enumerated %d, submitted %d to cluster\n",
				res.Titles, res.Parts, res.Enumerated, res.Submitted,
			)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Path to capture.jsonl (required)")
	cmd.Flags().StringVar(&master, "url", "", "Master base URL, e.g. http://192.168.1.10:8081 (required)")
	cmd.Flags().StringVar(&token, "token", "", "Admin token (required when master binds non-loopback)")
	cmd.Flags().BoolVar(&enumerate, "enumerate", true, "Probe CDN to expand each title's full part series")
	return cmd
}
