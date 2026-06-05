// Package admin serves the embedded web dashboard and its JSON/SSE API. It is
// the GUI for psxdh: a live capture log, per-title session progress, library
// state, and a connectivity panel (DNS resolver health + CDN reachability).
//
// Binding and auth: the dashboard is meant to be opened from a phone on the
// LAN, so it binds beyond loopback by default. Any non-loopback bind requires
// a shared token (auto-generated when unset and printed in the startup
// banner). The token is accepted via the X-Psxdh-Token header or a ?token=
// query parameter so a plain phone URL works.
package admin

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/cluster"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/doctor"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/export"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/handoff"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/jobs"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/lifecycle"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/mdns"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/netinfo"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/netresolve"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/persist"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/session"
)

//go:embed web
var webFS embed.FS

// Deps bundles the collaborators the dashboard reads from.
type Deps struct {
	Config        *config.Config
	ConfigPath    string // path to config.yaml; "" means none (PUT /api/config is read-only)
	Token         string // resolved token; empty means no auth (loopback only)
	Version       string
	Bus           capture.Bus
	Sessions      *session.Store
	Index         *library.Index
	DNSHealth     *netresolve.HealthResolver // optional
	Aria2         *handoff.Aria2Client       // optional
	Cluster       *cluster.Manager           // optional (master only)
	Prober        cluster.Prober             // optional (master only; used by /api/jobs/import)
	OnJobsChanged func()                     // optional debounced state-save trigger
	Logger        *slog.Logger
}

// Server is the admin HTTP server.
type Server struct {
	deps  Deps
	mux   *http.ServeMux
	httpd *http.Server
}

// New builds the server and registers routes.
func New(d Deps) (*Server, error) {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Server{deps: d, mux: http.NewServeMux()}

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	s.mux.Handle("/", http.FileServer(http.FS(sub)))
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/events", s.handleEvents)
	s.mux.HandleFunc("/api/sessions", s.handleSessions)
	s.mux.HandleFunc("/api/library", s.handleLibrary)
	s.mux.HandleFunc("/api/doctor", s.handleDoctor)
	s.mux.HandleFunc("/api/dns", s.handleDNS)
	s.mux.HandleFunc("/api/handoff/aria2", s.handleAria2)
	s.mux.HandleFunc("/api/cluster/progress", s.handleClusterProgress)
	s.mux.HandleFunc("/api/cluster/nodes", s.handleClusterNodes)
	s.mux.HandleFunc("/api/cluster/discovered", s.handleClusterDiscovered)
	s.mux.HandleFunc("/api/config", s.handleConfig)
	s.mux.HandleFunc("/api/export", s.handleExport)
	s.mux.HandleFunc("/api/jobs/import", s.handleJobsImport)
	return s, nil
}

// Handler returns the token-guarded handler (useful for httptest).
func (s *Server) Handler() http.Handler { return s.auth(s.mux) }

// ListenAndServe binds Config.Admin.Listen until ctx is canceled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	s.httpd = &http.Server{
		Addr:              s.deps.Config.Admin.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		s.deps.Logger.Info("admin dashboard listening", "addr", s.deps.Config.Admin.Listen)
		err := s.httpd.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		lifecycle.ShutdownHTTP(s.deps.Logger, "admin", s.httpd)
		return nil
	case err := <-errCh:
		return err
	}
}

// auth enforces the shared token on every request when one is configured.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Token == "" {
			next.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("X-Psxdh-Token")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.deps.Token)) != 1 {
			http.Error(w, "unauthorised: missing or wrong token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	files, basenames := 0, 0
	if s.deps.Index != nil {
		files, basenames = s.deps.Index.Stats()
	}
	lanIPs, _ := netinfo.IPv4Addrs()
	writeJSON(w, map[string]any{
		"version":         s.deps.Version,
		"proxy_listen":    s.deps.Config.Proxy.Listen,
		"proxy_port":      netinfo.PortOf(s.deps.Config.Proxy.Listen),
		"lan_ips":         lanIPs,
		"library_dir":     s.deps.Config.Library.Dir,
		"files":           files,
		"basenames":       basenames,
		"dropped_events":  s.deps.Bus.Dropped(),
		"aria2_enabled":   s.deps.Aria2 != nil,
		"dns_health":      s.deps.DNSHealth != nil,
		"cluster_enabled": s.deps.Cluster != nil,
		"config_editable": s.deps.ConfigPath != "",
		"config_path":     s.deps.ConfigPath,
		"state_path":      s.deps.Config.Jobs.StatePath,
	})
}

// wireEvent is the privacy-reduced capture event sent over SSE (no headers).
type wireEvent struct {
	Time   string `json:"time"`
	Method string `json:"method"`
	URL    string `json:"url"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Part   int    `json:"part"`
	Client string `json:"client"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsubscribe := s.deps.Bus.Subscribe()
	defer unsubscribe()

	// Establish the stream immediately so the client's request returns and the
	// EventSource fires `open` before any capture event arrives.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			we := wireEvent{
				Time:   ev.Time.Format(time.RFC3339),
				Method: ev.Method,
				Kind:   string(ev.Kind),
				Title:  ev.Hint.TitleHint,
				Part:   ev.Hint.PartIndex,
				Client: ev.ClientAddr,
			}
			if ev.URL != nil {
				we.URL = ev.URL.String()
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return
			}
			if err := enc.Encode(we); err != nil { // Encode appends a newline
				return
			}
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleSessions(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Sessions == nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, s.deps.Sessions.Snapshot())
}

func (s *Server) handleLibrary(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Index == nil {
		writeJSON(w, map[string]any{})
		return
	}
	writeJSON(w, s.deps.Index.All())
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	rep := doctor.Check(r.Context(), s.deps.Config.Network, doctor.CheckOptions{
		HandshakeTimeout: 5 * time.Second,
	})
	writeJSON(w, rep)
}

func (s *Server) handleDNS(w http.ResponseWriter, _ *http.Request) {
	if s.deps.DNSHealth == nil {
		writeJSON(w, []netresolve.ResolverStat{})
		return
	}
	writeJSON(w, s.deps.DNSHealth.Snapshot())
}

func (s *Server) handleAria2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.Aria2 == nil {
		http.Error(w, "aria2 handoff not enabled", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.URL == "" {
		http.Error(w, "bad request: expected {\"url\":\"…\"}", http.StatusBadRequest)
		return
	}
	gid, err := s.deps.Aria2.AddURI(r.Context(), body.URL, s.deps.Config.Library.Dir)
	if err != nil {
		s.deps.Logger.Warn("aria2 handoff failed", "url", body.URL, "err", err)
		http.Error(w, "aria2 error: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"gid": gid})
}

func (s *Server) handleClusterProgress(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Cluster == nil {
		http.Error(w, "cluster not enabled (master only)", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.deps.Cluster.Snapshot())
}

// handleClusterNodes lists (GET), adds (POST {base_url}), or removes (DELETE
// ?id=) cluster nodes.
func (s *Server) handleClusterNodes(w http.ResponseWriter, r *http.Request) {
	if s.deps.Cluster == nil {
		http.Error(w, "cluster not enabled (master only)", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.deps.Cluster.Nodes())
	case http.MethodPost:
		var body struct {
			BaseURL string `json:"base_url"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.BaseURL == "" {
			http.Error(w, "bad request: expected {\"base_url\":\"http://host:port\"}", http.StatusBadRequest)
			return
		}
		id, err := s.deps.Cluster.AddNode(r.Context(), body.BaseURL, "manual")
		if err != nil {
			// Node recorded but unreachable; report the issue, not a hard failure.
			writeJSON(w, map[string]any{"id": id, "online": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"id": id, "online": true})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id query parameter required", http.StatusBadRequest)
			return
		}
		s.deps.Cluster.RemoveNode(id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleClusterDiscovered returns psxdh node agents found on the LAN via mDNS.
func (s *Server) handleClusterDiscovered(w http.ResponseWriter, r *http.Request) {
	if s.deps.Cluster == nil {
		http.Error(w, "cluster not enabled (master only)", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	found, err := mdns.Browse(ctx, mdns.NodeServiceType)
	if err != nil {
		http.Error(w, "mdns browse failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	type discovered struct {
		Instance string `json:"instance"`
		BaseURL  string `json:"base_url"`
	}
	out := make([]discovered, 0, len(found))
	for _, sv := range found {
		out = append(out, discovered{Instance: sv.Instance, BaseURL: sv.BaseURL()})
	}
	writeJSON(w, out)
}

// handleConfig serves the running config and persists edits to ConfigPath
// after validating them. The structured dashboard form uses JSON on the wire;
// the YAML path is preserved so existing tools and `curl` recipes keep
// working. Most changes take effect on restart.
//
//	GET  /api/config              → application/x-yaml (default)
//	GET  /api/config?format=json  → application/json
//	GET  /api/config (Accept:application/json) → application/json
//	PUT  /api/config              → body is application/x-yaml (default) or application/json
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if wantsJSON(r) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.deps.Config)
			return
		}
		data, err := s.deps.Config.Marshal()
		if err != nil {
			http.Error(w, "marshal config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write(data)
	case http.MethodPut:
		if s.deps.ConfigPath == "" {
			http.Error(w, "no config file in use (started with defaults); set --config to enable editing", http.StatusConflict)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}

		var yamlBytes []byte
		if isJSONContent(r) {
			parsed, err := config.ParseJSONAndValidate(body)
			if err != nil {
				http.Error(w, "invalid config: "+err.Error(), http.StatusBadRequest)
				return
			}
			yamlBytes, err = parsed.Marshal()
			if err != nil {
				http.Error(w, "marshal config: "+err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			if _, err := config.ParseAndValidate(body); err != nil {
				http.Error(w, "invalid config: "+err.Error(), http.StatusBadRequest)
				return
			}
			yamlBytes = body
		}
		if err := os.MkdirAll(filepath.Dir(s.deps.ConfigPath), 0o755); err != nil {
			http.Error(w, "create config dir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(s.deps.ConfigPath, yamlBytes, 0o644); err != nil {
			http.Error(w, "write config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.deps.Logger.Info("config updated via dashboard", "path", s.deps.ConfigPath)
		writeJSON(w, map[string]string{"status": "saved", "note": "restart psxdh to apply"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// maxImportBytes caps the JSONL upload size. 4 MiB is enough for ~30k
// captured events, far more than a single PS5 game produces.
const maxImportBytes = 4 << 20

// handleJobsImport accepts a capture.jsonl upload (multipart) or a JSON path
// pointer and feeds the events through jobs.ImportFromEvents. The dashboard's
// "Import capture" button posts a multipart form; tooling on the master can
// also POST JSON {"path":"…"} when the file is already on disk. Returns the
// jobs.ImportResult so the caller can show "imported X parts".
//
//	POST /api/jobs/import?enumerate=true|false
func (s *Server) handleJobsImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.Sessions == nil {
		http.Error(w, "sessions store unavailable", http.StatusServiceUnavailable)
		return
	}

	enumerate := s.deps.Config.Jobs.ImportEnumerate
	if v := r.URL.Query().Get("enumerate"); v != "" {
		enumerate = strings.EqualFold(v, "true") || v == "1"
	}

	events, err := s.readImportEvents(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(events) == 0 {
		http.Error(w, "capture file contained no events", http.StatusBadRequest)
		return
	}

	opts := jobs.ImportOptions{
		Sessions:  s.deps.Sessions,
		Cluster:   s.deps.Cluster,
		Prober:    s.deps.Prober,
		Enumerate: enumerate && s.deps.Prober != nil,
		Logger:    s.deps.Logger,
	}
	res, err := jobs.ImportFromEvents(r.Context(), events, opts)
	if err != nil {
		s.deps.Logger.Warn("admin: import failed", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.deps.Logger.Info("admin: capture imported", "titles", res.Titles, "parts", res.Parts, "submitted", res.Submitted)
	if s.deps.OnJobsChanged != nil {
		s.deps.OnJobsChanged()
	}
	writeJSON(w, res)
}

// readImportEvents reads the capture log from the request, supporting both
// a multipart upload (form field "capture") and a JSON body of the form
// {"path":"/abs/path.jsonl"}. The path form is gated to avoid arbitrary file
// reads off the master's filesystem.
func (s *Server) readImportEvents(r *http.Request) ([]capture.Event, error) {
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxImportBytes); err != nil {
			return nil, err
		}
		fh, _, err := r.FormFile("capture")
		if err != nil {
			return nil, err
		}
		defer fh.Close()
		tmp, err := os.CreateTemp("", "psxdh-import-*.jsonl")
		if err != nil {
			return nil, err
		}
		defer func() {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}()
		if _, err := io.Copy(tmp, http.MaxBytesReader(nil, fh, maxImportBytes)); err != nil {
			return nil, err
		}
		if err := tmp.Sync(); err != nil {
			return nil, err
		}
		return persist.ReadAll(tmp.Name())
	}

	// JSON path pointer for in-process imports on the master.
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(&body); err != nil {
		return nil, err
	}
	if body.Path == "" {
		return nil, errAcceptedFormats
	}
	if err := validateImportPath(body.Path); err != nil {
		return nil, err
	}
	return persist.ReadAll(body.Path)
}

var errAcceptedFormats = httpErr("provide multipart upload (field 'capture') or JSON {\"path\":\"…\"}")

func httpErr(msg string) error { return errString(msg) }

type errString string

func (e errString) Error() string { return string(e) }

// validateImportPath rejects anything that isn't an absolute path under the
// user's home directory. This is a coarse but effective guard against the
// admin endpoint being abused to read arbitrary files off the host.
func validateImportPath(p string) error {
	if !filepath.IsAbs(p) {
		return httpErr("path must be absolute")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return httpErr("server cannot resolve home directory")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return httpErr("invalid path: " + err.Error())
	}
	if !strings.HasPrefix(abs, home+string(filepath.Separator)) && abs != home {
		return httpErr("path must live under " + home)
	}
	return nil
}

// handleExport streams a downloader-ready URL list (txt or aria2) for the
// running session store. It is the live counterpart of `psxdh export`: the
// dashboard hands the file straight to aria2/IDM/FDM without writing JSONL
// to disk first. Pass ?title=<name> to limit the export to one game.
//
//	GET /api/export?format=aria2|txt&title=<optional>
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.Sessions == nil {
		http.Error(w, "no sessions available", http.StatusServiceUnavailable)
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "aria2"
	}
	if format != "aria2" && format != "txt" {
		http.Error(w, "format must be aria2 or txt", http.StatusBadRequest)
		return
	}
	title := r.URL.Query().Get("title")
	urls := pushableURLs(s.deps.Sessions.Snapshot(), title)
	if len(urls) == 0 {
		http.Error(w, "no pushable URLs found", http.StatusNotFound)
		return
	}

	filename := exportFilename(title, format)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)

	libDir := ""
	if s.deps.Config != nil {
		libDir = s.deps.Config.Library.Dir
	}
	var err error
	if format == "aria2" {
		err = export.WriteAria2(w, urls, libDir)
	} else {
		err = export.WriteTxt(w, urls)
	}
	if err != nil {
		s.deps.Logger.Warn("export write failed", "err", err)
	}
}

// pushableURLs flattens session.Snapshot into the URL list export consumers
// expect, dropping non-pushable kinds (manifests, CRC, ignore) and applying
// an optional title filter. Sessions are already sorted by part index.
func pushableURLs(sessions []session.Session, titleFilter string) []string {
	var out []string
	for _, sess := range sessions {
		if titleFilter != "" && sess.Title != titleFilter {
			continue
		}
		for _, p := range sess.Parts {
			if !match.IsPushableKind(match.Kind(p.Kind)) {
				continue
			}
			if p.URL != "" {
				out = append(out, p.URL)
			}
		}
	}
	return out
}

func exportFilename(title, format string) string {
	ext := ".aria2.txt"
	if format == "txt" {
		ext = ".urls.txt"
	}
	if title == "" {
		return "psxdh-capture" + ext
	}
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ' ' || r == '"' {
			return '-'
		}
		return r
	}, title)
	return "psxdh-" + safe + ext
}

// wantsJSON reports whether the GET caller asked for JSON via the explicit
// `?format=json` query or an `Accept: application/json` header. The structured
// dashboard editor uses this; the legacy YAML response is preserved as the
// default so `curl /api/config` keeps emitting YAML.
func wantsJSON(r *http.Request) bool {
	if strings.EqualFold(r.URL.Query().Get("format"), "json") {
		return true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json")
}

// isJSONContent reports whether the PUT body is JSON (Content-Type:
// application/json). Anything else is treated as YAML.
func isJSONContent(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json")
}
