// Package netinfo reports local network addresses for console setup hints.
package netinfo

import (
	"net"
	"sort"
)

// Addr is one non-loopback IPv4 address on an up interface.
type Addr struct {
	Interface string `json:"interface"`
	IP        string `json:"ip"`
}

// IPv4Addrs returns all non-loopback IPv4 addresses on interfaces that are up,
// sorted by interface name then IP.
func IPv4Addrs() ([]Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []Addr
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipnet.IP.To4()
			if v4 == nil {
				continue
			}
			out = append(out, Addr{Interface: iface.Name, IP: v4.String()})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Interface != out[j].Interface {
			return out[i].Interface < out[j].Interface
		}
		return out[i].IP < out[j].IP
	})
	return out, nil
}

// PrimaryIPv4 returns the first IPv4 from IPv4Addrs, or "" when none exist.
func PrimaryIPv4() string {
	addrs, err := IPv4Addrs()
	if err != nil || len(addrs) == 0 {
		return ""
	}
	return addrs[0].IP
}

// PortOf returns the port half of a host:port listen address, or the input
// unchanged when it cannot be parsed.
func PortOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return port
}
