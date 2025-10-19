package netutil

import (
	"fmt"
	"net"
)

func FirstUsableIPv4() (string, error) {
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
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				ip := ipn.IP.To4()
				if !ip.IsLoopback() {
					return ip.String(), nil
				}
			}
		}
	}
	return "", fmt.Errorf("no IPv4 found")
}
