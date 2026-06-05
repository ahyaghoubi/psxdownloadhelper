package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/cluster"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
)

func serverWithCluster(t *testing.T, mgr *cluster.Manager, cfgPath string) *httptest.Server {
	t.Helper()
	cfg := config.Default()
	s, err := New(Deps{Config: cfg, ConfigPath: cfgPath, Bus: capture.NewBus(8), Cluster: mgr, Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustDo(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestClusterProgressDisabled(t *testing.T) {
	srv := serverWithCluster(t, nil, "")
	resp := mustGet(t, srv.URL+"/api/cluster/progress")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("cluster disabled: got %d, want 503", resp.StatusCode)
	}
}

func TestClusterNodesAddRemove(t *testing.T) {
	mgr := cluster.NewManager(cluster.Deps{LibDir: t.TempDir()})
	srv := serverWithCluster(t, mgr, "")

	// Add an (unreachable) node — recorded, reported offline.
	resp, err := http.Post(srv.URL+"/api/cluster/nodes", "application/json", strings.NewReader(`{"base_url":"http://127.0.0.1:1"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add node: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"id"`) {
		t.Errorf("expected an id in response, got %s", body)
	}

	// List shows one node.
	lresp := mustGet(t, srv.URL+"/api/cluster/nodes")
	lbody, _ := io.ReadAll(lresp.Body)
	lresp.Body.Close()
	if !strings.Contains(string(lbody), "node-1") {
		t.Errorf("expected node-1 in list, got %s", lbody)
	}

	// Remove it.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/cluster/nodes?id=node-1", nil)
	dresp := mustDo(t, req)
	dresp.Body.Close()
	if dresp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: got %d, want 204", dresp.StatusCode)
	}
}

func TestConfigGetPut(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("log:\n  level: info\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := serverWithCluster(t, nil, cfgPath)

	// GET returns YAML.
	resp := mustGet(t, srv.URL+"/api/config")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "proxy:") {
		t.Errorf("config YAML missing proxy section: %s", body)
	}

	// GET JSON returns full config for the dashboard form.
	jresp := mustGet(t, srv.URL+"/api/config?format=json")
	jbody, _ := io.ReadAll(jresp.Body)
	jresp.Body.Close()
	if jresp.StatusCode != http.StatusOK {
		t.Fatalf("GET JSON config: got %d", jresp.StatusCode)
	}
	if ct := jresp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("GET JSON content-type = %q, want application/json", ct)
	}
	js := string(jbody)
	if !strings.Contains(js, `"proxy"`) || !strings.Contains(js, "allow_http_fallback") {
		t.Errorf("JSON config missing expected keys: %s", js)
	}

	// PUT a valid edit persists.
	good := "proxy:\n  listen: \"0.0.0.0:9999\"\nlibrary:\n  dir: \"~/x\"\n"
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(good))
	preq := mustDo(t, req)
	preq.Body.Close()
	if preq.StatusCode != http.StatusOK {
		t.Fatalf("PUT valid config: got %d", preq.StatusCode)
	}
	saved, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(saved), "9999") {
		t.Errorf("config not persisted: %s", saved)
	}

	// PUT JSON persists as YAML.
	jsonBody := `{"proxy":{"listen":"0.0.0.0:7777"}}`
	jreq, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(jsonBody))
	jreq.Header.Set("Content-Type", "application/json")
	jput := mustDo(t, jreq)
	jput.Body.Close()
	if jput.StatusCode != http.StatusOK {
		t.Fatalf("PUT JSON config: got %d", jput.StatusCode)
	}
	saved, _ = os.ReadFile(cfgPath)
	if !strings.Contains(string(saved), "7777") {
		t.Errorf("JSON config not persisted as YAML: %s", saved)
	}

	// PUT an invalid edit is rejected (and does not overwrite).
	bad := "proxy:\n  listen: \"not-a-host-port\"\n"
	breq, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(bad))
	bresp := mustDo(t, breq)
	bresp.Body.Close()
	if bresp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT invalid config: got %d, want 400", bresp.StatusCode)
	}
}

func TestConfigPutReadOnlyWithoutPath(t *testing.T) {
	srv := serverWithCluster(t, nil, "")
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader("log:\n  level: info\n"))
	resp := mustDo(t, req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("PUT without config path: got %d, want 409", resp.StatusCode)
	}
}
