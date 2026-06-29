package netutil

import (
	"fmt"
	"net"
)

// ipSource returns a usable non-loopback/link-local IPv4, or false.
type ipSource func() (net.IP, bool)

var (
	routeSource ipSource = defaultRouteIP
	ifaceSource ipSource = defaultInterfaceIP
)

func FirstUsableIPv4() (string, error) {
	// Ask the routing table which interface would carry SSDP multicast. This is
	// generally more reliable than selecting the first interface on hosts with
	// VPNs, bridges, and virtual adapters. DialUDP does not send a packet.
	if ip, ok := routeSource(); ok {
		return ip.To4().String(), nil
	}

	if ip, ok := ifaceSource(); ok {
		return ip.To4().String(), nil
	}
	return "", fmt.Errorf("no IPv4 found")
}

// defaultRouteIP probes the routing table via a connected (but unsent) UDP
// socket and reports the local IPv4 it would use.
func defaultRouteIP() (net.IP, bool) {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   net.ParseIP("239.255.255.250"),
		Port: 1900,
	})
	if err != nil {
		return nil, false
	}
	defer func() { _ = conn.Close() }()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && usableIPv4(addr.IP) {
		return addr.IP, true
	}
	return nil, false
}

// defaultInterfaceIP enumerates local network interfaces and returns the first
// usable non-loopback IPv4 address.
func defaultInterfaceIP() (net.IP, bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, false
	}
	for _, iface := range ifaces {
		if iface.Flags&(net.FlagUp|net.FlagLoopback) != net.FlagUp {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && usableIPv4(ipn.IP) {
				return ipn.IP.To4(), true
			}
		}
	}
	return nil, false
}

func usableIPv4(ip net.IP) bool {
	return ip.To4() != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}
