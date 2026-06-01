package netresolve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// UDPResolver does plain unencrypted DNS over UDP/53 against a fixed
// server address. It is the fallback used by Iran-friendly resolvers
// (Shecan, Electro, 403.online) when DoH cannot be reached.
//
// The implementation is intentionally minimal: one A query + one AAAA
// query, racing in parallel, no recursion-desired games, no EDNS0. We
// rely on the upstream resolver to do the actual recursion.
type UDPResolver struct {
	// Addr is "host:port" of the DNS server. If port is omitted, 53
	// is appended in the constructor.
	Addr string
}

// NewUDP returns a UDPResolver. If addr lacks a port, ":53" is appended.
func NewUDP(addr string) *UDPResolver {
	if !strings.Contains(addr, ":") {
		addr = addr + ":53"
	}
	return &UDPResolver{Addr: addr}
}

// LookupHost implements Resolver via UDP DNS.
func (u *UDPResolver) LookupHost(ctx context.Context, host string) ([]string, time.Duration, error) {
	if ip := parseIPLiteral(host); ip != "" {
		return []string{ip}, 0, nil
	}
	name, err := dnsmessage.NewName(dnsNameOf(host))
	if err != nil {
		return nil, 0, fmt.Errorf("netresolve/udp: invalid name %q: %w", host, err)
	}

	type res struct {
		ips []string
		ttl time.Duration
		err error
	}
	ch := make(chan res, 2)
	go func() {
		ips, ttl, err := u.query(ctx, name, dnsmessage.TypeA)
		ch <- res{ips, ttl, err}
	}()
	go func() {
		ips, ttl, err := u.query(ctx, name, dnsmessage.TypeAAAA)
		ch <- res{ips, ttl, err}
	}()

	var ips []string
	var ttl time.Duration
	var firstErr error
	for i := 0; i < 2; i++ {
		r := <-ch
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		ips = append(ips, r.ips...)
		if r.ttl > 0 && (ttl == 0 || r.ttl < ttl) {
			ttl = r.ttl
		}
	}
	if len(ips) == 0 {
		if firstErr == nil {
			firstErr = &nxDomainError{host: host}
		}
		return nil, 0, firstErr
	}
	return dedupeSort(ips), ttl, nil
}

func (u *UDPResolver) query(ctx context.Context, name dnsmessage.Name, qtype dnsmessage.Type) ([]string, time.Duration, error) {
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: uint16(time.Now().UnixNano() & 0xFFFF), RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  name,
			Type:  qtype,
			Class: dnsmessage.ClassINET,
		}},
	}
	buf, err := msg.Pack()
	if err != nil {
		return nil, 0, fmt.Errorf("netresolve/udp: pack: %w", err)
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", u.Addr)
	if err != nil {
		return nil, 0, fmt.Errorf("netresolve/udp: dial %s: %w", u.Addr, err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(boundedDeadline(ctx, 1500*time.Millisecond))

	if _, err := conn.Write(buf); err != nil {
		return nil, 0, fmt.Errorf("netresolve/udp: write: %w", err)
	}

	reply := make([]byte, 1500) // UDP DNS messages cap at 512 bytes without EDNS, 1500 is safe.
	n, err := conn.Read(reply)
	if err != nil {
		return nil, 0, fmt.Errorf("netresolve/udp: read: %w", err)
	}

	var parsed dnsmessage.Message
	if err := parsed.Unpack(reply[:n]); err != nil {
		return nil, 0, fmt.Errorf("netresolve/udp: unpack: %w", err)
	}
	if parsed.RCode == dnsmessage.RCodeNameError {
		return nil, 0, &nxDomainError{host: name.String()}
	}
	if parsed.RCode != dnsmessage.RCodeSuccess {
		return nil, 0, fmt.Errorf("netresolve/udp: server returned rcode %s", parsed.RCode)
	}

	ips, ttl := extractIPs(parsed.Answers)
	if len(ips) == 0 {
		return nil, 0, errors.New("netresolve/udp: no answer records")
	}
	return ips, ttl, nil
}

// extractIPs walks DNS resource records and returns any A / AAAA addresses
// found, plus the minimum TTL across them (cached values must expire as
// soon as the shortest-lived record does).
func extractIPs(answers []dnsmessage.Resource) ([]string, time.Duration) {
	var ips []string
	var minTTL uint32
	for _, a := range answers {
		switch r := a.Body.(type) {
		case *dnsmessage.AResource:
			ips = append(ips, net.IP(r.A[:]).String())
		case *dnsmessage.AAAAResource:
			ips = append(ips, net.IP(r.AAAA[:]).String())
		default:
			continue
		}
		if minTTL == 0 || a.Header.TTL < minTTL {
			minTTL = a.Header.TTL
		}
	}
	return ips, time.Duration(minTTL) * time.Second
}

// dnsNameOf ensures the trailing dot required by dnsmessage.NewName.
func dnsNameOf(host string) string {
	if !strings.HasSuffix(host, ".") {
		host = host + "."
	}
	return host
}
