package main

// End-to-end tests. These build the real psxdh binary, start it as a separate
// process, and drive it the way a console and a user would:
//
//	console ──HTTP proxy──► psxdh ──upstream proxy──► fake Sony CDN
//	browser ──────────────► psxdh dashboard
//	"FDM"   ──writes files─► library folder
//
// The fake CDN is reached through psxdh's upstream_proxy setting, so real
// PlayStation host names are used without touching the network.

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "psxdh-e2e")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "psxdh")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	build := exec.Command("go", "build", "-ldflags", "-X main.version=e2e", "-o", binPath, ".")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic("build psxdh: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

const (
	gameURL  = "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234_00/app/pkg/1/f_abc/EP0000-PPSA01234_00-GAME000000000000_0.pkg?sig=abc"
	gameName = "EP0000-PPSA01234_00-GAME000000000000_0.pkg"
	part1URL = "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234_00/app/pkg/1/f_abc/EP0000-PPSA01234_00-GAME000000000000_1.pkg"
)

type cdn struct {
	*httptest.Server
	hits  atomic.Int64
	body  []byte
	parts int // numbered parts _0.._parts-1 exist; others are 404
}

// partBody returns part i: the shared body trimmed so each part has its own size.
func (c *cdn) partBody(i int) []byte { return c.body[:len(c.body)-i*10] }

func newCDN(t *testing.T, size int) *cdn { return newGameCDN(t, size, 1) }

func newGameCDN(t *testing.T, size, parts int) *cdn {
	c := &cdn{body: make([]byte, size), parts: parts}
	for i := range c.body {
		c.body[i] = byte(i*31 + 7)
	}
	partRe := regexp.MustCompile(`_(\d+)\.pkg$`)
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits.Add(1)
		if m := partRe.FindStringSubmatch(r.URL.Path); m != nil {
			var i int
			fmt.Sscan(m[1], &i)
			if i >= c.parts {
				http.NotFound(w, r)
				return
			}
			http.ServeContent(w, r, "x.pkg", time.Time{}, bytes.NewReader(c.partBody(i)))
			return
		}
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(c.Close)
	return c
}

type instance struct {
	cmd     *exec.Cmd
	port    string
	home    string
	libDir  string
	console *http.Client // a client configured like the console: psxdh as proxy
	exited  chan error
	stdout  *lockedBuf
}

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) WriteString(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.WriteString(s)
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func start(t *testing.T, upstream string, extra ...string) *instance {
	t.Helper()
	return startIn(t, t.TempDir(), filepath.Join(t.TempDir(), "library"), upstream, extra...)
}

// startIn runs psxdh with a given home (where config.yaml and links.json
// live) and library folder, so a test can restart it on the same state.
func startIn(t *testing.T, home, libDir, upstream string, extra ...string) *instance {
	t.Helper()
	cfg := filepath.Join(home, "config.yaml")
	conf := "log:\n  level: debug\n"
	if upstream != "" {
		conf += "network:\n  upstream_proxy:\n    enabled: true\n    url: " + upstream + "\n"
	}
	if err := os.WriteFile(cfg, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--listen", "127.0.0.1:0", "--library", libDir, "--config", cfg, "--no-browser"}, extra...)
	cmd := exec.Command(binPath, args...)
	cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := new(lockedBuf)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	inst := &instance{cmd: cmd, home: home, libDir: libDir, exited: make(chan error, 1), stdout: new(lockedBuf)}

	portRe := regexp.MustCompile(`Console proxy\s+\S+\s+port (\d+)`)
	found := make(chan string, 1)
	go func() {
		var port string
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			inst.stdout.WriteString(sc.Text() + "\n")
			if m := portRe.FindStringSubmatch(sc.Text()); m != nil {
				port = m[1]
			}
			if strings.Contains(sc.Text(), "Press Ctrl+C") {
				found <- port
			}
		}
	}()
	go func() { inst.exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if t.Failed() {
			t.Logf("psxdh stderr:\n%s", stderr.String())
		}
	})
	select {
	case inst.port = <-found:
	case err := <-inst.exited:
		t.Fatalf("psxdh exited early: %v\n%s", err, stderr.String())
	case <-time.After(15 * time.Second):
		t.Fatal("psxdh did not print its banner")
	}
	pu, _ := url.Parse("http://127.0.0.1:" + inst.port)
	inst.console = &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test TLS server
	}}
	return inst
}

func (in *instance) base() string { return "http://127.0.0.1:" + in.port }

type fileState struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	Size      int64  `json:"size"`
	LocalSize int64  `json:"localSize"`
	SentBytes int64  `json:"sentBytes"`
	Part      int    `json:"part"`
}

type apiState struct {
	Version    string `json:"version"`
	Port       string `json:"port"`
	LibraryDir string `json:"libraryDir"`
	Console    struct {
		Connected bool   `json:"connected"`
		Addr      string `json:"addr"`
	} `json:"console"`
	Titles []struct {
		ID      string      `json:"id"`
		Finding bool        `json:"finding"`
		Update  bool        `json:"update"`
		Files   []fileState `json:"files"`
	} `json:"titles"`
	Library []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	} `json:"library"`
}

func (in *instance) state(t *testing.T) apiState {
	t.Helper()
	resp, err := http.Get(in.base() + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st apiState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func (in *instance) file(t *testing.T, name string) (fileState, bool) {
	for _, ti := range in.state(t).Titles {
		for _, f := range ti.Files {
			if f.Name == name {
				return f, true
			}
		}
	}
	return fileState{}, false
}

func (in *instance) waitStatus(t *testing.T, name, want string) fileState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last fileState
	for time.Now().Before(deadline) {
		if f, ok := in.file(t, name); ok {
			last = f
			if f.Status == want {
				return f
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: status %q, want %q", name, last.Status, want)
	return last
}

func (in *instance) consoleGet(t *testing.T, raw, rng string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, raw, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := in.console.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// TestFullWorkflow walks the whole user journey.
func TestFullWorkflow(t *testing.T) {
	sony := newCDN(t, 3<<20) // 3 MiB "game part"
	in := start(t, sony.URL)

	// The banner tells the user what to do.
	for _, want := range []string{"psxdh e2e is running", "Dashboard", "Library folder", "Upstream proxy"} {
		if !strings.Contains(in.stdout.String(), want) {
			t.Errorf("banner missing %q:\n%s", want, in.stdout.String())
		}
	}

	// The dashboard opens on the same port, before any console has connected.
	resp, err := http.Get(in.base() + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(page, []byte("<title>psxdh</title>")) {
		t.Fatalf("dashboard: %d", resp.StatusCode)
	}
	st := in.state(t)
	if st.Version != "e2e" || st.Port != in.port || st.Console.Connected || len(st.Titles) != 0 {
		t.Fatalf("initial state: %+v", st)
	}
	if fi, err := os.Stat(in.libDir); err != nil || !fi.IsDir() {
		t.Fatal("library folder should be created on start")
	}

	// 1. The console starts the download through the proxy (slow path).
	r, body := in.consoleGet(t, gameURL, "bytes=0-65535")
	if r.StatusCode != 206 || !bytes.Equal(body, sony.body[:65536]) {
		t.Fatalf("forward: %d", r.StatusCode)
	}
	st = in.state(t)
	if !st.Console.Connected || st.Console.Addr != "127.0.0.1" {
		t.Errorf("console not shown as connected: %+v", st.Console)
	}
	if len(st.Titles) != 1 || st.Titles[0].ID != "PPSA01234" {
		t.Fatalf("titles: %+v", st.Titles)
	}
	f := in.waitStatus(t, gameName, "from-sony")
	if f.Size != int64(len(sony.body)) || f.URL != gameURL || f.Part != 0 {
		t.Errorf("captured file: %+v", f)
	}

	// The user copies the links (the .txt the dashboard offers).
	resp, err = http.Get(in.base() + "/api/urls.txt?need=1")
	if err != nil {
		t.Fatal(err)
	}
	txt, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.TrimSpace(string(txt)) != gameURL {
		t.Fatalf("urls.txt = %q", txt)
	}

	// 2. "FDM" downloads into the library under a temporary name...
	tmp := filepath.Join(in.libDir, gameName+".fdmdownload")
	if err := os.WriteFile(tmp, sony.body[:1<<20], 0o644); err != nil {
		t.Fatal(err)
	}
	f = in.waitStatus(t, gameName, "downloading")
	if f.LocalSize != 1<<20 {
		t.Errorf("local size = %d", f.LocalSize)
	}
	// ...and the console keeps going via Sony meanwhile: never the partial file.
	hitsBefore := sony.hits.Load()
	_, body = in.consoleGet(t, gameURL, "bytes=65536-131071")
	if !bytes.Equal(body, sony.body[65536:131072]) || sony.hits.Load() != hitsBefore+1 {
		t.Fatal("partial download must not be served")
	}

	// 3. FDM finishes and renames into place.
	if err := os.WriteFile(tmp, sony.body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(in.libDir, gameName)); err != nil {
		t.Fatal(err)
	}
	in.waitStatus(t, gameName, "ready")
	resp, _ = http.Get(in.base() + "/api/urls.txt?need=1")
	txt, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(bytes.TrimSpace(txt)) != 0 {
		t.Errorf("nothing left to download, got %q", txt)
	}

	// 4. The console resumes with a Range request; psxdh serves from disk.
	hitsBefore = sony.hits.Load()
	r, body = in.consoleGet(t, gameURL, "bytes=131072-")
	if r.StatusCode != 206 || !bytes.Equal(body, sony.body[131072:]) {
		t.Fatalf("library serve: %d, %d bytes", r.StatusCode, len(body))
	}
	if sony.hits.Load() != hitsBefore {
		t.Fatal("served from library must not contact Sony")
	}
	f = in.waitStatus(t, gameName, "sending")
	if f.SentBytes != int64(len(sony.body)-131072) {
		t.Errorf("sent bytes = %d", f.SentBytes)
	}

	// A second part shows up as a new row under the same title.
	in.consoleGet(t, part1URL, "bytes=0-0")
	st = in.state(t)
	if len(st.Titles) != 1 || len(st.Titles[0].Files) != 2 {
		t.Fatalf("expected 2 parts under one title: %+v", st.Titles)
	}

	// Clearing forgets links but leaves the library alone.
	resp, err = http.Post(in.base()+"/api/clear", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	st = in.state(t)
	if len(st.Titles) != 0 || len(st.Library) != 1 || st.Library[0].State != "ready" {
		t.Fatalf("after clear: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(in.libDir, gameName)); err != nil {
		t.Fatal("clear must not delete files")
	}
}

func TestWrongSizeFileIsNeverSentToConsole(t *testing.T) {
	sony := newCDN(t, 1<<20)
	in := start(t, sony.URL)
	in.consoleGet(t, gameURL, "bytes=0-0")
	if err := os.WriteFile(filepath.Join(in.libDir, gameName), sony.body[:1000], 0o644); err != nil {
		t.Fatal(err)
	}
	in.waitStatus(t, gameName, "wrong-size")
	_, body := in.consoleGet(t, gameURL, "bytes=2000-2999")
	if !bytes.Equal(body, sony.body[2000:3000]) {
		t.Fatal("console must get correct bytes from Sony, not the truncated file")
	}
}

func TestLibraryFileFromBeforeStartIsServed(t *testing.T) {
	// A file already sitting in the library when psxdh starts is served
	// straight away, with no captured URL needed.
	sony := newCDN(t, 4096)
	in := start(t, sony.URL)
	p := filepath.Join(in.libDir, "sub", gameName)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, sony.body, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(p, old, old)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body := in.consoleGet(t, gameURL, "bytes=0-99")
		if bytes.Equal(body, sony.body[:100]) && sony.hits.Load() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("library file was not served (sony hits %d)", sony.hits.Load())
		}
		sony.hits.Store(0)
		time.Sleep(200 * time.Millisecond)
	}
}

func TestHTTPSIsTunnelledUntouched(t *testing.T) {
	in := start(t, "")
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "psn-secure")
	}))
	defer tlsSrv.Close()
	r, body := in.consoleGet(t, tlsSrv.URL+"/login", "")
	if r.StatusCode != 200 || string(body) != "psn-secure" {
		t.Fatalf("CONNECT: %d %q", r.StatusCode, body)
	}
	if !in.state(t).Console.Connected {
		t.Error("HTTPS traffic should mark the console as connected")
	}
}

func TestDirectForwardWithoutUpstreamProxy(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s", r.URL.Query().Get("q"))
	}))
	defer origin.Close()
	in := start(t, "")
	_, body := in.consoleGet(t, origin.URL+"/x?q=psn", "")
	if string(body) != "hello psn" {
		t.Fatalf("got %q", body)
	}
}

func TestPortInUseHasFriendlyError(t *testing.T) {
	first := start(t, "")
	out, err := exec.Command(binPath, "--listen", "127.0.0.1:"+first.port, "--library", t.TempDir(), "--no-browser").CombinedOutput()
	if err == nil {
		t.Fatal("second instance on the same port should fail")
	}
	if !strings.Contains(string(out), "already in use") {
		t.Fatalf("unhelpful error: %s", out)
	}
}

func TestLegacyProxySubcommandAndVersion(t *testing.T) {
	out, err := exec.Command(binPath, "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "e2e" {
		t.Fatalf("version: %q %v", out, err)
	}
	out, err = exec.Command(binPath, "proxy", "-h").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--library") && !strings.Contains(string(out), "-library") {
		t.Fatalf("proxy -h: %v %s", err, out)
	}
	out, err = exec.Command(binPath, "--bogus").CombinedOutput()
	if err == nil {
		t.Fatalf("unknown flag should fail: %s", out)
	}
}

func TestGracefulShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGINT on Windows")
	}
	in := start(t, "")
	if err := in.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-in.exited:
		if err != nil {
			t.Fatalf("exit after Ctrl+C: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("psxdh did not stop on Ctrl+C")
	}
}

// TestConsoleOffWorkflow is the flow for big games: the console asks for one
// part, psxdh finds every part, the user switches the console off and
// downloads on the PC (psxdh may even restart), then the console comes back
// and installs everything from the PC.
func TestConsoleOffWorkflow(t *testing.T) {
	sony := newGameCDN(t, 4096, 4) // parts _0.._3, each a different size
	in := start(t, sony.URL)

	// Like the PS5 asking for part 51 first: the console asks for part 2 only.
	part := func(i int) string { return strings.Replace(gameURL, "_0.pkg", fmt.Sprintf("_%d.pkg", i), 1) }
	name := func(i int) string { return strings.Replace(gameName, "_0.pkg", fmt.Sprintf("_%d.pkg", i), 1) }
	in.consoleGet(t, part(2), "bytes=0-0")

	deadline := time.Now().Add(10 * time.Second)
	for {
		st := in.state(t)
		if len(st.Titles) == 1 && len(st.Titles[0].Files) == 4 && !st.Titles[0].Finding {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("parts not found: %+v", st.Titles)
		}
		time.Sleep(100 * time.Millisecond)
	}
	for i := 0; i < 4; i++ {
		f, _ := in.file(t, name(i))
		if f.Size != int64(len(sony.partBody(i))) || f.Part != i || !strings.HasSuffix(f.URL, "?sig=abc") {
			t.Errorf("part %d: %+v", i, f)
		}
	}
	resp, _ := http.Get(in.base() + "/api/urls.txt?need=1")
	txt, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if n := len(strings.Fields(string(txt))); n != 4 {
		t.Fatalf("Copy links should give all 4 parts, got %d:\n%s", n, txt)
	}

	// Console off; psxdh restarts. The links must survive.
	_ = in.cmd.Process.Signal(os.Interrupt)
	select {
	case <-in.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("psxdh did not stop")
	}
	if _, err := os.Stat(filepath.Join(in.home, "links.json")); err != nil {
		t.Fatalf("links were not saved: %v", err)
	}
	in2 := startIn(t, in.home, in.libDir, sony.URL)
	st := in2.state(t)
	if len(st.Titles) != 1 || len(st.Titles[0].Files) != 4 {
		t.Fatalf("links lost after restart: %+v", st.Titles)
	}
	if st.Console.Connected {
		t.Error("restored state must not claim the console is connected")
	}

	// FDM downloads every part into the library.
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(in2.libDir, name(i)), sony.partBody(i), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		in2.waitStatus(t, name(i), "ready")
	}

	// The console comes back and installs every part from the PC.
	hits := sony.hits.Load()
	for i := 0; i < 4; i++ {
		r, body := in2.consoleGet(t, part(i), "")
		if r.StatusCode != 200 || !bytes.Equal(body, sony.partBody(i)) {
			t.Fatalf("part %d from library: %d, %d bytes", i, r.StatusCode, len(body))
		}
	}
	if sony.hits.Load() != hits {
		t.Fatalf("Sony contacted %d times while installing from the PC", sony.hits.Load()-hits)
	}
	for i := 0; i < 4; i++ {
		in2.waitStatus(t, name(i), "sending")
	}
}

// TestGameVsUpdate: a PS5 starts a game by reading the tail of its last
// part. That must be treated as a game with every part listed. When the user
// marks a package as an update, its links are withdrawn and no more parts are
// searched for.
func TestGameVsUpdate(t *testing.T) {
	sony := newGameCDN(t, 4<<20, 3) // 3 parts of ~4 MB
	in := start(t, sony.URL)
	last := strings.Replace(gameURL, "_0.pkg", "_2.pkg", 1)

	// Like the PS5 with GTA: read 100 bytes near the end of the last part.
	end := len(sony.partBody(2))
	r, _ := in.consoleGet(t, last, fmt.Sprintf("bytes=%d-%d", end-100, end-1))
	if r.StatusCode != 206 {
		t.Fatalf("forward: %d", r.StatusCode)
	}
	urlsNeeded := func() int {
		resp, _ := http.Get(in.base() + "/api/urls.txt?need=1")
		txt, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return len(strings.Fields(string(txt)))
	}
	deadline := time.Now().Add(10 * time.Second)
	for urlsNeeded() != 3 {
		if time.Now().After(deadline) {
			t.Fatalf("a game read from its tail should list all 3 parts: %+v", in.state(t).Titles)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st := in.state(t); st.Titles[0].Update {
		t.Fatal("must start as a game")
	}

	// The user marks it as an update.
	resp, err := http.Post(in.base()+"/api/mode", "application/json",
		strings.NewReader(`{"package":"EP0000-PPSA01234_00-GAME000000000000","mode":"console"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("mode: %d", resp.StatusCode)
	}
	if st := in.state(t); !st.Titles[0].Update {
		t.Fatal("update card expected")
	}
	if n := urlsNeeded(); n != 0 {
		t.Fatalf("an update must not offer links, got %d", n)
	}

	// Back to a game.
	resp, _ = http.Post(in.base()+"/api/mode", "application/json",
		strings.NewReader(`{"package":"EP0000-PPSA01234_00-GAME000000000000","mode":"pc"}`))
	resp.Body.Close()
	if n := urlsNeeded(); n != 3 {
		t.Fatalf("links should be back, got %d", n)
	}
}

// TestClearWhileSearching: pressing Clear while psxdh is still asking Sony
// about parts must leave the list empty.
func TestClearWhileSearching(t *testing.T) {
	slow := newGameCDN(t, 4096, 40)
	inner := slow.Config.Handler
	slow.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond) // a slow connection to Sony
		inner.ServeHTTP(w, r)
	})
	in := start(t, slow.URL)
	in.consoleGet(t, gameURL, "bytes=0-0")
	time.Sleep(300 * time.Millisecond) // search under way

	resp, err := http.Post(in.base()+"/api/clear", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	time.Sleep(2 * time.Second) // let the old search keep answering
	if st := in.state(t); len(st.Titles) != 0 {
		n := 0
		for _, ti := range st.Titles {
			n += len(ti.Files)
		}
		t.Fatalf("Clear did not stick: %d files came back", n)
	}
}
