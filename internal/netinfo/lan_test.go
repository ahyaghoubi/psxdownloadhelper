package netinfo

import (
	"net"
	"testing"
)

func TestIPv4AddrsExcludesLoopback(t *testing.T) {
	addrs, err := IPv4Addrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		ip := net.ParseIP(a.IP)
		if ip == nil || ip.IsLoopback() {
			t.Errorf("loopback or invalid IP in list: %+v", a)
		}
		if a.Interface == "" {
			t.Errorf("empty interface name for IP %q", a.IP)
		}
	}
}

func TestIPv4AddrsSorted(t *testing.T) {
	addrs, err := IPv4Addrs()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(addrs); i++ {
		prev, cur := addrs[i-1], addrs[i]
		if prev.Interface > cur.Interface || (prev.Interface == cur.Interface && prev.IP > cur.IP) {
			t.Fatalf("not sorted at %d: %+v before %+v", i, prev, cur)
		}
	}
}

func TestPrimaryIPv4MatchesFirst(t *testing.T) {
	addrs, err := IPv4Addrs()
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) == 0 {
		t.Skip("no non-loopback IPv4 interfaces on this host")
	}
	if got := PrimaryIPv4(); got != addrs[0].IP {
		t.Errorf("PrimaryIPv4() = %q, want %q", got, addrs[0].IP)
	}
}

func TestPortOf(t *testing.T) {
	if got := PortOf("0.0.0.0:8080"); got != "8080" {
		t.Errorf("PortOf = %q, want 8080", got)
	}
	if got := PortOf("bad"); got != "bad" {
		t.Errorf("PortOf invalid = %q, want bad", got)
	}
}
