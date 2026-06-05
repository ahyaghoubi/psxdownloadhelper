package mdns

import "testing"

func TestPortFromListen(t *testing.T) {
	cases := map[string]struct {
		port int
		ok   bool
	}{
		"0.0.0.0:8080":   {8080, true},
		"127.0.0.1:8081": {8081, true},
		"[::]:9000":      {9000, true},
		"noport":         {0, false},
		"":               {0, false},
	}
	for in, want := range cases {
		got, err := PortFromListen(in)
		if want.ok && (err != nil || got != want.port) {
			t.Errorf("PortFromListen(%q) = %d, %v; want %d", in, got, err, want.port)
		}
		if !want.ok && err == nil {
			t.Errorf("PortFromListen(%q) expected error", in)
		}
	}
}

func TestAnnounceRejectsBadPort(t *testing.T) {
	if _, err := Announce("psxdh", 0); err == nil {
		t.Error("expected error for port 0")
	}
	if _, err := Announce("psxdh", 70000); err == nil {
		t.Error("expected error for out-of-range port")
	}
}
