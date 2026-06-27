package netutil

import (
	"fmt"
	"net"
)

func FirstUsableIPv4() (string, error) {
	// Ask the routing table which interface would carry SSDP multicast. This is
	// generally more reliable than selecting the first interface on hosts with
	// VPNs, bridges, and virtual adapters. DialUDP does not send a packet.
	if conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   net.ParseIP("239.255.255.250"),
		Port: 1900,
	}); err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && usableIPv4(addr.IP) {
			return addr.IP.To4().String(), nil
		}
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&(net.FlagUp|net.FlagLoopback) != net.FlagUp {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && usableIPv4(ipn.IP) {
				ip := ipn.IP.To4()
				return ip.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no IPv4 found")
}

func usableIPv4(ip net.IP) bool {
	return ip.To4() != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}
