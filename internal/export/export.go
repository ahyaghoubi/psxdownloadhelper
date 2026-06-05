// Package export writes captured URL lists in formats that external
// downloaders (FDM, aria2, IDM, etc.) can consume. WriteTxt emits the plain
// one-URL-per-line format; WriteAria2 emits the aria2 input-file format
// (`aria2c -i list.txt`). See docs/roadmap.md and docs/network-resilience.md.
package export

import (
	"bufio"
	"errors"
	"io"
	"net/url"
	"path"
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

// WriteAria2 writes urls in aria2's input-file format. Each URL is followed by
// an indented `out=<basename>` (and, when dir is non-empty, `dir=<dir>`) so the
// downloaded file lands in the library with its original filename — which is
// exactly what the proxy's basename resolver expects.
//
// Load it with: aria2c --input-file=list.txt
func WriteAria2(w io.Writer, urls []string, dir string) error {
	if w == nil {
		return errors.New("export: nil writer")
	}
	bw := bufio.NewWriter(w)
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, err := bw.WriteString(u + "\n"); err != nil {
			return err
		}
		if name := basename(u); name != "" {
			if _, err := bw.WriteString("  out=" + name + "\n"); err != nil {
				return err
			}
		}
		if dir != "" {
			if _, err := bw.WriteString("  dir=" + dir + "\n"); err != nil {
				return err
			}
		}
	}
	return bw.Flush()
}

// basename extracts the trailing filename of a URL path, ignoring the query
// string. Returns "" when the path has no usable filename.
func basename(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" {
		return ""
	}
	name := path.Base(p)
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	return name
}
