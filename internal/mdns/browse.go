package mdns

import (
	"context"
	"fmt"
	"net"

	"github.com/grandcat/zeroconf"
)

// NodeServiceType is the DNS-SD service psxdh cluster slave agents announce
// under, so a master can discover them on the LAN. See ADR 0005.
const NodeServiceType = "_psxdh-node._tcp"

// AnnounceService registers instanceName under serviceType on port with the
// given TXT records. Generalises Announce for the cluster node service.
func AnnounceService(instanceName, serviceType string, port int, txt []string) (*Announcer, error) {
	if instanceName == "" {
		instanceName = "psxdh"
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("mdns: invalid port %d", port)
	}
	server, err := zeroconf.Register(instanceName, serviceType, "local.", port, txt, nil)
	if err != nil {
		return nil, fmt.Errorf("mdns: register %q (%s): %w", instanceName, serviceType, err)
	}
	return &Announcer{server: server}, nil
}

// Service is a discovered mDNS instance.
type Service struct {
	Instance string
	Host     string // first IPv4 address
	Port     int
}

// BaseURL returns an http:// base URL for the service, or "" if no address.
func (s Service) BaseURL() string {
	if s.Host == "" {
		return ""
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(s.Host, fmt.Sprintf("%d", s.Port)))
}

// Browse discovers instances of serviceType on the LAN until ctx is done,
// returning the unique set found. Intended to be called with a short-timeout
// context (e.g. 2s) from the dashboard's "discover nodes" action.
func Browse(ctx context.Context, serviceType string) ([]Service, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("mdns: new resolver: %w", err)
	}
	entries := make(chan *zeroconf.ServiceEntry, 16)
	if err := resolver.Browse(ctx, serviceType, "local.", entries); err != nil {
		return nil, fmt.Errorf("mdns: browse %s: %w", serviceType, err)
	}

	seen := make(map[string]Service)
	for {
		select {
		case <-ctx.Done():
			out := make([]Service, 0, len(seen))
			for _, s := range seen {
				out = append(out, s)
			}
			return out, nil
		case e, ok := <-entries:
			if !ok {
				out := make([]Service, 0, len(seen))
				for _, s := range seen {
					out = append(out, s)
				}
				return out, nil
			}
			host := ""
			if len(e.AddrIPv4) > 0 {
				host = e.AddrIPv4[0].String()
			}
			seen[e.Instance] = Service{Instance: e.Instance, Host: host, Port: e.Port}
		}
	}
}
