package netresolve

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeDoH replies with A records pointing at the configured IP. The
// AAAA query gets an empty answer so the resolver still completes.
func fakeDoH(t *testing.T, aIPv4 [4]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "expected POST", http.StatusMethodNotAllowed)
			return
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/dns-message") {
			http.Error(w, "bad content-type", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("DoH server read body: %v", err)
			http.Error(w, "read", http.StatusInternalServerError)
			return
		}
		var msg dnsmessage.Message
		if err := msg.Unpack(body); err != nil {
			http.Error(w, "unpack", http.StatusBadRequest)
			return
		}
		reply := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: msg.ID, Response: true, RCode: dnsmessage.RCodeSuccess},
			Questions: msg.Questions,
		}
		for _, q := range msg.Questions {
			switch q.Type {
			case dnsmessage.TypeA:
				reply.Answers = append(reply.Answers, dnsmessage.Resource{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: q.Class, TTL: 60},
					Body:   &dnsmessage.AResource{A: aIPv4},
				})
			case dnsmessage.TypeAAAA:
				// Leave empty so the test only relies on the A path.
			}
		}
		out, err := reply.Pack()
		if err != nil {
			http.Error(w, "pack", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(out)
	}))
}

func TestDoHResolverHappyPath(t *testing.T) {
	srv := fakeDoH(t, [4]byte{1, 2, 3, 4})
	defer srv.Close()
	d := &DoHResolver{Endpoint: srv.URL, Client: &http.Client{Timeout: 2 * time.Second}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, ttl, err := d.LookupHost(ctx, "example.com")
	if err != nil {
		t.Fatalf("LookupHost: %v", err)
	}
	if len(ips) != 1 || ips[0] != "1.2.3.4" {
		t.Errorf("ips = %v", ips)
	}
	if ttl != 60*time.Second {
		t.Errorf("ttl = %v, want 60s", ttl)
	}
}

func TestDoHResolverRejectsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	d := &DoHResolver{Endpoint: srv.URL, Client: &http.Client{Timeout: time.Second}}
	_, _, err := d.LookupHost(context.Background(), "example.com")
	if err == nil {
		t.Fatal("expected error from 502 response")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error should mention 502: %v", err)
	}
}

func TestDoHResolverIPLiteralPassesThrough(t *testing.T) {
	d := &DoHResolver{Endpoint: "https://invalid.example/dns-query"}
	ips, _, err := d.LookupHost(context.Background(), "9.9.9.9")
	if err != nil {
		t.Fatalf("literal: %v", err)
	}
	if len(ips) != 1 || ips[0] != "9.9.9.9" {
		t.Errorf("ips = %v", ips)
	}
}
