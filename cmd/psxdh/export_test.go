package main

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/persist"
)

func writeFixtureJSONL(t *testing.T, path string) {
	t.Helper()
	sink, err := persist.Open(path, false)
	if err != nil {
		t.Fatalf("open sink: %v", err)
	}
	defer sink.Close()
	for _, raw := range []struct {
		url   string
		title string
		idx   int
		kind  match.Kind
	}{
		{"http://cdn.example/PPSA01_0.pkg", "PPSA01", 0, match.KindPKGApp},
		{"http://cdn.example/PPSA01_1.pkg", "PPSA01", 1, match.KindPKGApp},
		{"http://cdn.example/manifest.json", "PPSA01", -1, match.KindManifestJSON},
		{"http://cdn.example/CUSA02_0.pkg", "CUSA02", 0, match.KindPKGApp},
	} {
		u, _ := url.Parse(raw.url)
		ev := capture.Event{
			Time:   time.Now().UTC(),
			Method: "GET",
			URL:    u,
			Kind:   raw.kind,
			Hint:   match.Hint{TitleHint: raw.title, PartIndex: raw.idx},
		}
		if err := sink.Write(ev); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
}

func TestExportAria2WritesAllPushable(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "capture.jsonl")
	out := filepath.Join(dir, "out.txt")
	writeFixtureJSONL(t, jsonl)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"export",
		"--from", jsonl,
		"--format", "aria2",
		"--out", out,
		"--library-dir", "/tmp/lib",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\noutput: %s", err, buf.String())
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"http://cdn.example/CUSA02_0.pkg",
		"http://cdn.example/PPSA01_0.pkg",
		"http://cdn.example/PPSA01_1.pkg",
		"out=PPSA01_0.pkg",
		"dir=/tmp/lib",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q\noutput:\n%s", want, got)
		}
	}
	if strings.Contains(got, "manifest.json") {
		t.Errorf("export should drop non-pushable kinds:\n%s", got)
	}
}

func TestExportTitleFilterAndTxt(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "capture.jsonl")
	out := filepath.Join(dir, "out.txt")
	writeFixtureJSONL(t, jsonl)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"export",
		"--from", jsonl,
		"--format", "txt",
		"--title", "PPSA01",
		"--out", out,
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\noutput: %s", err, buf.String())
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if strings.Contains(got, "CUSA02") {
		t.Errorf("title filter should exclude CUSA02:\n%s", got)
	}
	if !strings.Contains(got, "PPSA01_0.pkg") || !strings.Contains(got, "PPSA01_1.pkg") {
		t.Errorf("missing PPSA01 parts:\n%s", got)
	}
}

func TestExportRequiresFromFlag(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"export"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected --from to be required")
	}
}
