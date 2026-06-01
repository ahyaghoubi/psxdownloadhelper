package netresolve

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeUDPDNS starts a goroutine that answers UDP DNS queries with the
// configured A record. AAAA queries get a successful but empty reply.
func fakeUDPDNS(t *testing.T, a [4]byte) (addr string, stop func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1500)
		for {
			_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, peer, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var msg dnsmessage.Message
			if err := msg.Unpack(buf[:n]); err != nil {
				continue
			}
			reply := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: msg.ID, Response: true, RCode: dnsmessage.RCodeSuccess},
				Questions: msg.Questions,
			}
			for _, q := range msg.Questions {
				if q.Type == dnsmessage.TypeA {
					reply.Answers = append(reply.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: q.Class, TTL: 30},
						Body:   &dnsmessage.AResource{A: a},
					})
				}
			}
			out, err := reply.Pack()
			if err != nil {
				continue
			}
			_, _ = pc.WriteTo(out, peer)
		}
	}()
	return pc.LocalAddr().String(), func() {
		_ = pc.Close()
		<-done
	}
}

func TestUDPResolverHappyPath(t *testing.T) {
	addr, stop := fakeUDPDNS(t, [4]byte{8, 8, 4, 4})
	defer stop()

	u := NewUDP(addr)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, ttl, err := u.LookupHost(ctx, "example.com")
	if err != nil {
		t.Fatalf("LookupHost: %v", err)
	}
	if len(ips) != 1 || ips[0] != "8.8.4.4" {
		t.Errorf("ips = %v", ips)
	}
	if ttl != 30*time.Second {
		t.Errorf("ttl = %v, want 30s", ttl)
	}
}

func TestUDPResolverIPLiteralPassesThrough(t *testing.T) {
	u := NewUDP("127.0.0.1:53")
	ips, _, err := u.LookupHost(context.Background(), "8.8.4.4")
	if err != nil {
		t.Fatalf("literal: %v", err)
	}
	if len(ips) != 1 || ips[0] != "8.8.4.4" {
		t.Errorf("ips = %v", ips)
	}
}

func TestUDPResolverDialFailureSurfaces(t *testing.T) {
	// Reserved discard address that should refuse / drop.
	u := NewUDP("203.0.113.1:53")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, err := u.LookupHost(ctx, "example.com")
	if err == nil {
		t.Fatal("expected error from unreachable DNS")
	}
	if !strings.Contains(err.Error(), "udp") {
		t.Errorf("err should mention udp transport: %v", err)
	}
}

func TestNewUDPAppendsDefaultPort(t *testing.T) {
	u := NewUDP("1.1.1.1")
	if u.Addr != "1.1.1.1:53" {
		t.Errorf("Addr = %q, want 1.1.1.1:53", u.Addr)
	}
	u2 := NewUDP("1.1.1.1:5353")
	if u2.Addr != "1.1.1.1:5353" {
		t.Errorf("Addr = %q, want 1.1.1.1:5353", u2.Addr)
	}
}
