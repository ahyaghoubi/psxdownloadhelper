package main

import (
	"os"
	"testing"
)

func TestDefaultProxyWhenNoArgs(t *testing.T) {
	orig := append([]string(nil), os.Args...)
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"psxdh"}
	if !defaultProxyWhenNoArgs() {
		t.Fatal("expected bare start")
	}
	if len(os.Args) != 2 || os.Args[1] != "proxy" {
		t.Fatalf("args = %v, want [psxdh proxy]", os.Args)
	}
}

func TestDefaultProxyWhenNoArgsSkipsWithSubcommand(t *testing.T) {
	orig := append([]string(nil), os.Args...)
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"psxdh", "version"}
	if defaultProxyWhenNoArgs() {
		t.Fatal("should not default when subcommand present")
	}
}
