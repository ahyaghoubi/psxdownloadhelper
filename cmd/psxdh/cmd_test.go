package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "dev") {
		t.Errorf("version output = %q, want to contain 'dev'", buf.String())
	}
}

func TestRootHelpListsSubcommands(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"version", "proxy"} {
		if !strings.Contains(out, want) {
			t.Errorf("root --help missing subcommand %q\nfull output:\n%s", want, out)
		}
	}
}

func TestProxyHelpListsFlags(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"proxy", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute proxy --help: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"--config", "--listen", "--library", "--log-level"} {
		if !strings.Contains(out, want) {
			t.Errorf("proxy --help missing flag %q\nfull output:\n%s", want, out)
		}
	}
}

func TestRootIncludesDoctorAndProbe(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"doctor", "probe"} {
		if !strings.Contains(out, want) {
			t.Errorf("root --help missing subcommand %q\nfull output:\n%s", want, out)
		}
	}
}

func TestDoctorRunsWithSkipTLS(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"doctor", "--skip-tls", "--host", "localhost"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "DNS resolvers") {
		t.Errorf("doctor output missing 'DNS resolvers' header: %s", out)
	}
}

func TestProbeRejectsRelativeURL(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"probe", "/foo.pkg"})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected probe with relative URL to fail")
	}
}
