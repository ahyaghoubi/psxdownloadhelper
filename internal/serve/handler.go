// Package serve streams files from the local library back to the console.
// All Range handling defers to stdlib http.ServeContent so the RFC 7233
// edge cases (suffix, open-ended, 416) are handled identically to net/http.
// See plan.md §6.2 component "serve" and the §10 testing strategy.
package serve

import (
	"log/slog"
	"net/http"
	"os"
)

// Handler streams a file from disk in response to a console request.
type Handler struct {
	logger *slog.Logger
}

// New returns a Handler. A nil logger is replaced with slog.Default.
func New(logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{logger: logger}
}

// ServeFile streams the file at path back to the client, honouring any
// Range header on r. plan.md §1.6 requires re-stat at open time so a file
// overwritten between the watcher's KindStable and the console's range
// request is detected (the size in the response reflects what's on disk
// now, not what the watcher recorded earlier).
func (h *Handler) ServeFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.logger.Warn("serve open failed", "path", path, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		h.logger.Warn("serve stat failed", "path", path, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if fi.IsDir() {
		http.Error(w, "not a regular file", http.StatusForbidden)
		return
	}

	w.Header().Set("Accept-Ranges", "bytes")
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	// http.ServeContent handles Range, If-Range, 206/416, and HEAD correctly.
	// It streams via io.Copy under the hood, so memory usage is constant
	// regardless of file size.
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}
