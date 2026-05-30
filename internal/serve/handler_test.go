package serve

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// makeFile creates a deterministic file with body[i] = byte(i % 256).
func makeFile(t *testing.T, dir, name string, size int) (path string, body []byte) {
	t.Helper()
	body = make([]byte, size)
	for i := range body {
		body[i] = byte(i % 256)
	}
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, body
}

func newServer(t *testing.T, path string) *httptest.Server {
	t.Helper()
	h := New(nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeFile(w, r, path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestServeFile_NoRangeReturns200(t *testing.T) {
	dir := t.TempDir()
	path, want := makeFile(t, dir, "f.bin", 1024)
	srv := newServer(t, path)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("body mismatch (%d vs %d bytes)", len(got), len(want))
	}
	if cl := resp.Header.Get("Content-Length"); cl != "1024" {
		t.Errorf("Content-Length = %q, want 1024", cl)
	}
	if ar := resp.Header.Get("Accept-Ranges"); ar != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", ar)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
}

func TestServeFile_SingleRangeReturns206(t *testing.T) {
	dir := t.TempDir()
	path, want := makeFile(t, dir, "f.bin", 4096)
	srv := newServer(t, path)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("status = %d, want 206", resp.StatusCode)
	}
	if !bytes.Equal(got, want[100:200]) {
		t.Errorf("range body mismatch")
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 100-199/4096" {
		t.Errorf("Content-Range = %q, want bytes 100-199/4096", cr)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "100" {
		t.Errorf("Content-Length = %q, want 100", cl)
	}
}

func TestServeFile_SuffixRange(t *testing.T) {
	dir := t.TempDir()
	path, want := makeFile(t, dir, "f.bin", 4096)
	srv := newServer(t, path)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Range", "bytes=-256")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("status = %d, want 206", resp.StatusCode)
	}
	if !bytes.Equal(got, want[len(want)-256:]) {
		t.Errorf("suffix range body mismatch")
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 3840-4095/4096" {
		t.Errorf("Content-Range = %q", cr)
	}
}

func TestServeFile_OpenEndedRange(t *testing.T) {
	dir := t.TempDir()
	path, want := makeFile(t, dir, "f.bin", 4096)
	srv := newServer(t, path)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Range", "bytes=3000-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("status = %d, want 206", resp.StatusCode)
	}
	if !bytes.Equal(got, want[3000:]) {
		t.Errorf("open-ended range body mismatch")
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 3000-4095/4096" {
		t.Errorf("Content-Range = %q", cr)
	}
}

func TestServeFile_RangeBeyondFileReturns416(t *testing.T) {
	dir := t.TempDir()
	path, _ := makeFile(t, dir, "f.bin", 100)
	srv := newServer(t, path)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Range", "bytes=200-300")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("status = %d, want 416", resp.StatusCode)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes */100" {
		t.Errorf("Content-Range = %q, want bytes */100", cr)
	}
}

func TestServeFile_MultipartRange(t *testing.T) {
	dir := t.TempDir()
	path, want := makeFile(t, dir, "f.bin", 4096)
	srv := newServer(t, path)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Range", "bytes=0-9,100-109,1000-1009")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("status = %d, want 206", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/byteranges") {
		t.Errorf("Content-Type = %q, want multipart/byteranges...", ct)
	}
	// Sanity check: the body should contain bytes from each requested chunk.
	got, _ := io.ReadAll(resp.Body)
	for _, chunk := range [][]byte{want[0:10], want[100:110], want[1000:1010]} {
		if !bytes.Contains(got, chunk) {
			t.Errorf("multipart body missing expected chunk %x", chunk)
		}
	}
}

func TestServeFile_HeadRequest(t *testing.T) {
	dir := t.TempDir()
	path, _ := makeFile(t, dir, "f.bin", 1024)
	srv := newServer(t, path)

	req, _ := http.NewRequest(http.MethodHead, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "1024" {
		t.Errorf("Content-Length = %q, want 1024", cl)
	}
	if len(body) != 0 {
		t.Errorf("HEAD body should be empty, got %d bytes", len(body))
	}
}

func TestServeFile_NotFound(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.bin")
	srv := newServer(t, missing)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestServeFile_Directory(t *testing.T) {
	dir := t.TempDir()
	srv := newServer(t, dir)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestServeFile_ConcurrentRangeReads(t *testing.T) {
	dir := t.TempDir()
	path, want := makeFile(t, dir, "f.bin", 64*1024)
	srv := newServer(t, path)

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := i * 1024
			end := start + 1023
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			got, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusPartialContent {
				errs <- fmt.Errorf("worker %d: status %d", i, resp.StatusCode)
				return
			}
			if !bytes.Equal(got, want[start:end+1]) {
				errs <- fmt.Errorf("worker %d: body mismatch", i)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
