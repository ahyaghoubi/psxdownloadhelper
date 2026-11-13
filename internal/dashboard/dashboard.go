// Package dashboard serves the web UI and its small JSON API.
//
//	GET  /              the single-page dashboard
//	GET  /api/state     everything the page shows, polled every second or so
//	GET  /api/urls.txt  captured PKG URLs, one per line (?need=1: only ones
//	                    not yet in the library)
//	POST /api/clear     forget captured URLs
package dashboard

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/netinfo"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/tracker"
)

//go:embed web
var webFS embed.FS

// Status is what the user should know about one captured file.
type Status string

const (
	StatusNeeded      Status = "needed"      // not in the library yet
	StatusFromSony    Status = "from-sony"   // console is downloading it from Sony right now
	StatusDownloading Status = "downloading" // your download manager is writing it
	StatusWrongSize   Status = "wrong-size"  // library copy doesn't match Sony's size
	StatusReady       Status = "ready"       // in the library, waiting for the console
	StatusSending     Status = "sending"     // console is installing it from the PC
	StatusSent        Status = "sent"        // console has received the whole file
)

// activeWindow is how recently bytes must have flowed to count as "now".
const activeWindow = 8 * time.Second

// Info is static information about this psxdh instance.
type Info struct {
	Version    string
	Port       string
	LibraryDir string
	ConfigPath string
}

// Handler serves the dashboard.
type Handler struct {
	info    Info
	lib     *library.Library
	tracker *tracker.Tracker
	mux     *http.ServeMux
	now     func() time.Time
}

// New returns the dashboard handler.
func New(info Info, lib *library.Library, t *tracker.Tracker) *Handler {
	h := &Handler{info: info, lib: lib, tracker: t, mux: http.NewServeMux(), now: time.Now}
	static, _ := fs.Sub(webFS, "web")
	h.mux.Handle("GET /", http.FileServer(http.FS(static)))
	h.mux.HandleFunc("GET /api/state", h.state)
	h.mux.HandleFunc("GET /api/urls.txt", h.urls)
	h.mux.HandleFunc("POST /api/clear", h.clear)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	h.mux.ServeHTTP(w, r)
}

// State is the /api/state payload.
type State struct {
	Version    string         `json:"version"`
	Port       string         `json:"port"`
	Addresses  []netinfo.Addr `json:"addresses"`
	LibraryDir string         `json:"libraryDir"`
	ConfigPath string         `json:"configPath"`
	Console    ConsoleState   `json:"console"`
	Titles     []Title        `json:"titles"`
	Library    []LibraryFile  `json:"library"`
	Counts     map[Status]int `json:"counts"`
	Other      int            `json:"other"` // small files (manifests, checksums) the console fetches itself
}

// ConsoleState says whether a console is using the proxy.
type ConsoleState struct {
	Connected bool   `json:"connected"`
	Addr      string `json:"addr,omitempty"`
	AgoSec    int    `json:"agoSec"`
}

// Title groups the files of one game or update.
type Title struct {
	ID      string `json:"id"`
	Finding bool   `json:"finding"` // still looking for more parts on Sony
	Files   []File `json:"files"`
}

// File is one captured PKG and its status.
type File struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Kind        string `json:"kind"`
	Part        int    `json:"part"`
	Size        int64  `json:"size"`      // full size, 0 if unknown
	LocalSize   int64  `json:"localSize"` // bytes in the library so far
	SentBytes   int64  `json:"sentBytes"` // bytes the console took from the library
	SonyBytes   int64  `json:"sonyBytes"` // bytes the console took from Sony
	Status      Status `json:"status"`
	Requested   bool   `json:"requested"` // the console asked for it (vs found by psxdh)
	LibraryPath string `json:"libraryPath,omitempty"`
}

// LibraryFile is a file in the library folder.
type LibraryFile struct {
	library.File
	Captured bool `json:"captured"`
}

// Build assembles the current state.
func (h *Handler) Build() State {
	now := h.now()
	items, console := h.tracker.Snapshot()
	addrs, _ := netinfo.IPv4Addrs()
	st := State{
		Version:    h.info.Version,
		Port:       h.info.Port,
		Addresses:  addrs,
		LibraryDir: h.lib.Root(),
		ConfigPath: h.info.ConfigPath,
		Titles:     []Title{},
		Library:    []LibraryFile{},
		Counts:     map[Status]int{},
	}
	if st.Addresses == nil {
		st.Addresses = []netinfo.Addr{}
	}
	if !console.LastSeen.IsZero() {
		ago := now.Sub(console.LastSeen)
		st.Console = ConsoleState{Connected: ago < 2*time.Minute, Addr: console.Addr, AgoSec: int(ago.Seconds())}
	}

	finding := h.tracker.Finding()
	captured := map[string]bool{}
	byTitle := map[string]int{}
	for _, it := range items {
		if !match.IsPackage(it.Kind) {
			st.Other++
			continue
		}
		captured[it.Name] = true
		f := h.file(it, now)
		st.Counts[f.Status]++
		id := it.Title
		if id == "" {
			id = "Other"
		}
		i, ok := byTitle[id]
		if !ok {
			i = len(st.Titles)
			byTitle[id] = i
			st.Titles = append(st.Titles, Title{ID: id, Finding: finding[it.Title]})
		}
		st.Titles[i].Files = append(st.Titles[i].Files, f)
	}
	for _, f := range h.lib.Files() {
		st.Library = append(st.Library, LibraryFile{File: f, Captured: captured[f.Name]})
	}
	return st
}

func (h *Handler) file(it tracker.Item, now time.Time) File {
	f := File{
		Key: it.Key, Name: it.Name, URL: it.URL, Kind: string(it.Kind), Part: it.Part,
		Size: it.Size, SentBytes: it.FromLibrary, SonyBytes: it.FromSony, Requested: it.Requested,
	}
	lf, inLibrary := h.lib.Find(it.Name)
	if inLibrary {
		f.LocalSize = lf.Size
		f.LibraryPath = lf.Rel
	}
	switch {
	case inLibrary && lf.State == library.StateWriting:
		f.Status = StatusDownloading
	case inLibrary && it.Size > 0 && lf.Size != it.Size:
		f.Status = StatusWrongSize
	case inLibrary:
		if f.Size == 0 {
			f.Size = lf.Size
		}
		// The console usually fetches the start of a file from Sony before
		// it is paused, then resumes from the PC: together they cover it.
		received := it.FromLibrary + it.FromSony
		switch {
		case it.FromLibrary > 0 && now.Sub(it.FromLibAt) < activeWindow:
			f.Status = StatusSending
		case it.FromLibrary > 0 && received >= f.Size:
			f.Status = StatusSent
		default:
			f.Status = StatusReady
		}
	case it.FromSony > 0 && now.Sub(it.FromSonyAt) < activeWindow:
		f.Status = StatusFromSony
	default:
		f.Status = StatusNeeded
	}
	return f
}

func (h *Handler) state(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.Build())
}

func (h *Handler) urls(w http.ResponseWriter, r *http.Request) {
	onlyNeeded := r.URL.Query().Get("need") == "1"
	var b strings.Builder
	for _, t := range h.Build().Titles {
		for _, f := range t.Files {
			if onlyNeeded && f.Status != StatusNeeded && f.Status != StatusFromSony && f.Status != StatusWrongSize {
				continue
			}
			b.WriteString(f.URL)
			b.WriteString("\r\n") // CRLF so Windows tools read one URL per line
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="psxdh-urls.txt"`)
	}
	_, _ = w.Write([]byte(b.String()))
}

func (h *Handler) clear(w http.ResponseWriter, _ *http.Request) {
	h.tracker.Clear()
	w.WriteHeader(http.StatusNoContent)
}
