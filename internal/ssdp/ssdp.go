package ssdp

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/upnp"
)

const ssdpAddr = "239.255.255.250:1900"

func Announce(ctx context.Context, baseURL, deviceUUID, serverName string) {
	addr, _ := net.ResolveUDPAddr("udp4", ssdpAddr)
	conn, err := net.DialUDP("udp4", advertisedLocalAddr(baseURL), addr)
	if err != nil {
		log.CtxError(ctx, "SSDP announce socket: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()

	usns := []struct{ st, usn string }{
		{upnp.DeviceType, deviceUUID + "::" + upnp.DeviceType},
		{upnp.AVTransportType, deviceUUID + "::" + upnp.AVTransportType},
		{upnp.RenderingType, deviceUUID + "::" + upnp.RenderingType},
		{upnp.ConnectionManagerType, deviceUUID + "::" + upnp.ConnectionManagerType},
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
			if _, err := conn.Write([]byte(msg)); err != nil {
				// Log write errors but continue with other announcements
				continue
			}
		}
		select {
		case <-ctx.Done():
			for _, x := range usns {
				msg := fmt.Sprintf(
					"NOTIFY * HTTP/1.1\r\nHOST: %s\r\nNT: %s\r\nNTS: ssdp:byebye\r\nUSN: %s\r\n\r\n",
					ssdpAddr, x.st, x.usn)
				if _, err := conn.Write([]byte(msg)); err != nil {
					// Log write errors but continue with other byebye messages
					continue
				}
			}
			return
		case <-ticker.C:
		}
	}
}

func advertisedLocalAddr(baseURL string) *net.UDPAddr {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || ip.To4() == nil {
		return nil
	}
	return &net.UDPAddr{IP: ip.To4()}
}

func SearchResponder(ctx context.Context, baseURL, deviceUUID, serverName string) {
	addr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		log.CtxError(ctx, "resolve SSDP address: %v", err)
		return
	}
	l, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		log.CtxError(ctx, "listen SSDP multicast: %v", err)
		return
	}
	defer func() { _ = l.Close() }()
	if err := l.SetReadBuffer(65536); err != nil {
		log.CtxWarn(ctx, "set SSDP read buffer: %v", err)
	}
	buf := make([]byte, 8192)
	responders := make(chan struct{}, 32)

	for {
		if err := l.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			log.CtxError(ctx, "set SSDP read deadline: %v", err)
			return
		}
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
		valid := st == "ssdp:all" || st == "upnp:rootdevice" || st == upnp.DeviceType || st == upnp.AVTransportType || st == upnp.RenderingType || st == upnp.ConnectionManagerType || st == deviceUUID
		if !valid {
			continue
		}
		mx := 1
		if parsed, err := strconv.Atoi(headerValue(text, "MX")); err == nil {
			mx = min(max(parsed, 1), 5)
		}
		select {
		case responders <- struct{}{}:
			srcCopy := *src
			go func() {
				defer func() { <-responders }()
				delay := time.Duration(rand.Int63n(int64(time.Duration(mx) * time.Second)))
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				for _, target := range responseTargets(st, deviceUUID) {
					resp := fmt.Sprintf(
						"HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nDATE: %s\r\nEXT:\r\nLOCATION: %s/device.xml\r\nSERVER: %s\r\nST: %s\r\nUSN: %s\r\nBOOTID.UPNP.ORG: 1\r\nCONFIGID.UPNP.ORG: 1\r\n\r\n",
						time.Now().UTC().Format(http.TimeFormat), baseURL, serverName, target.st, target.usn)
					if _, err := l.WriteToUDP([]byte(resp), &srcCopy); err != nil && ctx.Err() == nil {
						log.CtxWarn(ctx, "write SSDP response: %v", err)
					}
				}
			}()
		default:
			log.CtxWarn(ctx, "dropping SSDP search response: responder limit reached")
		}
	}
}

type responseTarget struct {
	st  string
	usn string
}

func responseTargets(requested, deviceUUID string) []responseTarget {
	all := []responseTarget{
		{"upnp:rootdevice", deviceUUID + "::upnp:rootdevice"},
		{deviceUUID, deviceUUID},
		{upnp.DeviceType, deviceUUID + "::" + upnp.DeviceType},
		{upnp.AVTransportType, deviceUUID + "::" + upnp.AVTransportType},
		{upnp.RenderingType, deviceUUID + "::" + upnp.RenderingType},
		{upnp.ConnectionManagerType, deviceUUID + "::" + upnp.ConnectionManagerType},
	}
	if requested == "ssdp:all" {
		return all
	}
	for _, target := range all {
		if target.st == requested {
			return []responseTarget{target}
		}
	}
	return nil
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
