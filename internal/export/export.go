// Package export writes captured URL lists in formats that external
// downloaders (FDM, aria2, IDM, etc.) can consume. Phase 1 ships only
// WriteTxt — the plain one-URL-per-line format. WriteFDMBatch and
// WriteAria2 land in Phase 2 (see docs/roadmap.md).
package export

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// WriteTxt writes urls to w, one per line with LF terminators. The full
// URL is preserved exactly (including query strings) per the proxy design
// rule "preserve query strings end-to-end" in docs/architecture.md.
//
// Empty or whitespace-only entries are skipped — a paste from a dashboard
// list shouldn't produce a 0-byte URL line that confuses the downloader.
func WriteTxt(w io.Writer, urls []string) error {
	if w == nil {
		return errors.New("export: nil writer")
	}
	bw := bufio.NewWriter(w)
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, err := bw.WriteString(u); err != nil {
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
	}
	return bw.Flush()
}
