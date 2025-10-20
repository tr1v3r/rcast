package ssdp

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/tr1v3r/rcast/internal/upnp"
)

const ssdpAddr = "239.255.255.250:1900"

func Announce(ctx context.Context, baseURL, deviceUUID, serverName string) {
	addr, _ := net.ResolveUDPAddr("udp4", ssdpAddr)
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()

	usns := []struct{ st, usn string }{
		{upnp.DeviceType, deviceUUID + "::" + upnp.DeviceType},
		{upnp.AVTransportType, deviceUUID + "::" + upnp.AVTransportType},
		{upnp.RenderingType, deviceUUID + "::" + upnp.RenderingType},
		{"upnp:rootdevice", deviceUUID + "::upnp:rootdevice"},
		{deviceUUID, deviceUUID},
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		for _, x := range usns {
			msg := fmt.Sprintf(
				"NOTIFY * HTTP/1.1\r\nHOST: %s\r\nCACHE-CONTROL: max-age=1800\r\nLOCATION: %s/device.xml\r\nNT: %s\r\nNTS: ssdp:alive\r\nSERVER: %s\r\nUSN: %s\r\nBOOTID.UPNP.ORG: 1\r\nCONFIGID.UPNP.ORG: 1\r\n\r\n",
				ssdpAddr, baseURL, x.st, serverName, x.usn)
			_, _ = conn.Write([]byte(msg))
		}
		select {
		case <-ctx.Done():
			for _, x := range usns {
				msg := fmt.Sprintf(
					"NOTIFY * HTTP/1.1\r\nHOST: %s\r\nNT: %s\r\nNTS: ssdp:byebye\r\nUSN: %s\r\n\r\n",
					ssdpAddr, x.st, x.usn)
				_, _ = conn.Write([]byte(msg))
			}
			return
		case <-ticker.C:
		}
	}
}

func SearchResponder(ctx context.Context, baseURL, deviceUUID, serverName string) {
	addr, _ := net.ResolveUDPAddr("udp4", ssdpAddr)
	l, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		return
	}
	defer l.Close()
	_ = l.SetReadBuffer(65536)
	buf := make([]byte, 8192)

	for {
		l.SetDeadline(time.Now().Add(2 * time.Second))
		n, src, err := l.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			continue
		}
		text := string(buf[:n])
		if !strings.HasPrefix(text, "M-SEARCH * HTTP/1.1") {
			continue
		}
		if !strings.Contains(strings.ToUpper(text), "MAN: \"SSDP:DISCOVER\"") {
			continue
		}
		st := headerValue(text, "ST")
		if st == "" {
			continue
		}
		valid := st == "ssdp:all" || st == "upnp:rootdevice" || st == upnp.DeviceType || st == upnp.AVTransportType || st == upnp.RenderingType || st == deviceUUID
		if !valid {
			continue
		}
		usn := deviceUUID
		if st != deviceUUID && st != "ssdp:all" {
			usn = deviceUUID + "::" + st
		}
		maxAge := 1800
		resp := fmt.Sprintf(
			"HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=%d\r\nDATE: %s\r\nEXT:\r\nLOCATION: %s/device.xml\r\nSERVER: %s\r\nST: %s\r\nUSN: %s\r\nBOOTID.UPNP.ORG: 1\r\nCONFIGID.UPNP.ORG: 1\r\n\r\n",
			maxAge, time.Now().UTC().Format(time.RFC1123), baseURL, serverName, st, usn)
		_, _ = l.WriteToUDP([]byte(resp), src)
	}
}

func headerValue(raw, key string) string {
	lines := strings.Split(raw, "\r\n")
	key = strings.ToUpper(key)
	for _, ln := range lines {
		if i := strings.IndexByte(ln, ':'); i > 0 {
			k := strings.ToUpper(strings.TrimSpace(ln[:i]))
			if k == key {
				return strings.TrimSpace(ln[i+1:])
			}
		}
	}
	return ""
}
