// Package mdns advertises psxdh on the local network via mDNS / DNS-SD so the
// console-setup step doesn't require hunting for the PC's IP address. It is a
// thin wrapper over github.com/grandcat/zeroconf (see ADR 0004); the dependency
// is isolated here behind a small interface.
package mdns

import (
	"fmt"
	"net"

	"github.com/grandcat/zeroconf"
)

// ServiceType is the DNS-SD service type psxdh advertises under.
const ServiceType = "_http._tcp"

// Announcer is a running mDNS registration. Close withdraws it (sends the
// goodbye packets) and releases the multicast socket.
type Announcer struct {
	server *zeroconf.Server
}

// Announce registers instanceName on the LAN, pointing at port (the proxy
// port) with a TXT record describing psxdh. The returned Announcer must be
// Closed on shutdown. instanceName defaults to "psxdh" when empty.
func Announce(instanceName string, port int) (*Announcer, error) {
	if instanceName == "" {
		instanceName = "psxdh"
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("mdns: invalid port %d", port)
	}
	txt := []string{"app=psxdh", "path=/"}
	server, err := zeroconf.Register(instanceName, ServiceType, "local.", port, txt, nil)
	if err != nil {
		return nil, fmt.Errorf("mdns: register %q: %w", instanceName, err)
	}
	return &Announcer{server: server}, nil
}

// Close withdraws the advertisement.
func (a *Announcer) Close() {
	if a != nil && a.server != nil {
		a.server.Shutdown()
		a.server = nil
	}
}

// PortFromListen extracts the numeric port from a "host:port" listen address.
func PortFromListen(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("mdns: parse listen %q: %w", addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return 0, fmt.Errorf("mdns: parse port %q: %w", portStr, err)
	}
	return port, nil
}
