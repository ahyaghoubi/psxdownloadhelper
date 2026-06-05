package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/cluster"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/persist"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/session"
)

func writeCaptureJSONL(t *testing.T, path string) {
	t.Helper()
	sink, err := persist.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	u, _ := url.Parse("http://cdn.example/PPSA01_0.pkg")
	ev := capture.Event{
		Time: time.Now(), Method: "GET", URL: u,
		Kind: match.KindPKGApp, Hint: match.Hint{TitleHint: "PPSA01", PartIndex: 0},
	}
	if err := sink.Write(ev); err != nil {
		t.Fatal(err)
	}
}

func TestJobsImportMultipart(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "capture.jsonl")
	writeCaptureJSONL(t, jsonl)

	cfg := config.Default()
	mgr := cluster.NewManager(cluster.Deps{LibDir: t.TempDir()})
	store := session.New(nil)
	s, err := New(Deps{
		Config: cfg, Bus: capture.NewBus(8), Sessions: store,
		Cluster: mgr, Version: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("capture", "capture.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(jsonl)
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/jobs/import?enumerate=false", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, msg)
	}
	var res map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if int(res["titles"].(float64)) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if got := store.Snapshot(); len(got) != 1 || got[0].Title != "PPSA01" {
		t.Fatalf("sessions = %+v", got)
	}
}

func TestJobsImportJSONPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".psxdh-test-import")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	jsonl := filepath.Join(dir, "capture.jsonl")
	writeCaptureJSONL(t, jsonl)

	cfg := config.Default()
	store := session.New(nil)
	s, _ := New(Deps{Config: cfg, Bus: capture.NewBus(8), Sessions: store, Version: "t"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	body := `{"path":"` + jsonl + `"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/jobs/import?enumerate=false", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestJobsImportRejectsPathOutsideHome(t *testing.T) {
	cfg := config.Default()
	store := session.New(nil)
	s, _ := New(Deps{Config: cfg, Bus: capture.NewBus(8), Sessions: store, Version: "t"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	body := `{"path":"/etc/passwd"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/jobs/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}
