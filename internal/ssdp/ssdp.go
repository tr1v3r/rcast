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

// packetConn is the UDP surface the SSDP loops use; *net.UDPConn implements it.
type packetConn interface {
	Write(b []byte) (int, error)
	WriteToUDP(b []byte, addr *net.UDPAddr) (int, error)
	ReadFromUDP(b []byte) (int, *net.UDPAddr, error)
	SetDeadline(t time.Time) error
	SetReadBuffer(bytes int) error
	Close() error
}

// Injectable runtime hooks. Every default reproduces today's behavior.
var (
	dialAnnounce = func(local *net.UDPAddr) (packetConn, error) {
		addr, _ := net.ResolveUDPAddr("udp4", ssdpAddr)
		return net.DialUDP("udp4", local, addr)
	}
	listenMulticast = func() (packetConn, error) {
		addr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
		if err != nil {
			return nil, err
		}
		return net.ListenMulticastUDP("udp4", nil, addr)
	}
	announceInterval   = 30 * time.Second
	searchReadDeadline = 2 * time.Second
	responderCap       = 32
	randomDelay        = func(mx int) time.Duration {
		return time.Duration(rand.Int63n(int64(time.Duration(mx) * time.Second)))
	}
	// onDroppedSearch is a test hook invoked when an M-SEARCH is dropped because
	// the responder cap is full. It is a no-op in production.
	onDroppedSearch = func() {}
)

// aliveTarget is one of the device's ST/USN pairs sent in Announce loops.
type aliveTarget struct{ st, usn string }

// aliveTargets returns the six Announce entries in the existing order
// (DeviceType, AVTransport, Rendering, ConnectionManager, rootdevice, uuid).
// The order differs from responseTargets and must not be reused.
func aliveTargets(deviceUUID string) []aliveTarget {
	return []aliveTarget{
		{upnp.DeviceType, deviceUUID + "::" + upnp.DeviceType},
		{upnp.AVTransportType, deviceUUID + "::" + upnp.AVTransportType},
		{upnp.RenderingType, deviceUUID + "::" + upnp.RenderingType},
		{upnp.ConnectionManagerType, deviceUUID + "::" + upnp.ConnectionManagerType},
		{"upnp:rootdevice", deviceUUID + "::upnp:rootdevice"},
		{deviceUUID, deviceUUID},
	}
}

// buildAliveMessage formats an ssdp:alive NOTIFY (verbatim).
func buildAliveMessage(ssdpAddr, baseURL, serverName, st, usn string) string {
	return fmt.Sprintf(
		"NOTIFY * HTTP/1.1\r\nHOST: %s\r\nCACHE-CONTROL: max-age=1800\r\nLOCATION: %s/device.xml\r\nNT: %s\r\nNTS: ssdp:alive\r\nSERVER: %s\r\nUSN: %s\r\nBOOTID.UPNP.ORG: 1\r\nCONFIGID.UPNP.ORG: 1\r\n\r\n",
		ssdpAddr, baseURL, st, serverName, usn)
}

// buildByebyeMessage formats an ssdp:byebye NOTIFY (verbatim).
func buildByebyeMessage(ssdpAddr, st, usn string) string {
	return fmt.Sprintf(
		"NOTIFY * HTTP/1.1\r\nHOST: %s\r\nNT: %s\r\nNTS: ssdp:byebye\r\nUSN: %s\r\n\r\n",
		ssdpAddr, st, usn)
}

// buildSearchResponse formats a 200 OK M-SEARCH response (verbatim), using now
// formatted as RFC1123 GMT for the DATE header.
func buildSearchResponse(baseURL, serverName string, target responseTarget, now time.Time) string {
	return fmt.Sprintf(
		"HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nDATE: %s\r\nEXT:\r\nLOCATION: %s/device.xml\r\nSERVER: %s\r\nST: %s\r\nUSN: %s\r\nBOOTID.UPNP.ORG: 1\r\nCONFIGID.UPNP.ORG: 1\r\n\r\n",
		now.Format(http.TimeFormat), baseURL, serverName, target.st, target.usn)
}

// parseMSearch validates an M-SEARCH packet and extracts the ST and clamped MX.
// Returns ok=false for any malformed or unsupported packet.
func parseMSearch(raw, deviceUUID string) (st string, mx int, ok bool) {
	if !strings.HasPrefix(raw, "M-SEARCH * HTTP/1.1") {
		return "", 0, false
	}
	if !strings.Contains(strings.ToUpper(raw), "MAN: \"SSDP:DISCOVER\"") {
		return "", 0, false
	}
	st = headerValue(raw, "ST")
	if st == "" {
		return "", 0, false
	}
	valid := st == "ssdp:all" || st == "upnp:rootdevice" || st == upnp.DeviceType ||
		st == upnp.AVTransportType || st == upnp.RenderingType ||
		st == upnp.ConnectionManagerType || st == deviceUUID
	if !valid {
		return "", 0, false
	}
	mx = 1
	if parsed, err := strconv.Atoi(headerValue(raw, "MX")); err == nil {
		mx = min(max(parsed, 1), 5)
	}
	return st, mx, true
}

func Announce(ctx context.Context, baseURL, deviceUUID, serverName string) {
	conn, err := dialAnnounce(advertisedLocalAddr(baseURL))
	if err != nil {
		log.CtxError(ctx, "SSDP announce socket: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()

	usns := aliveTargets(deviceUUID)

	ticker := time.NewTicker(announceInterval)
	defer ticker.Stop()

	for {
		for _, x := range usns {
			msg := buildAliveMessage(ssdpAddr, baseURL, serverName, x.st, x.usn)
			if _, err := conn.Write([]byte(msg)); err != nil {
				// Log write errors but continue with other announcements
				continue
			}
		}
		select {
		case <-ctx.Done():
			for _, x := range usns {
				msg := buildByebyeMessage(ssdpAddr, x.st, x.usn)
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
	conn, err := listenMulticast()
	if err != nil {
		log.CtxError(ctx, "listen SSDP multicast: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadBuffer(65536); err != nil {
		log.CtxWarn(ctx, "set SSDP read buffer: %v", err)
	}
	buf := make([]byte, 8192)
	responders := make(chan struct{}, responderCap)

	for {
		if err := conn.SetDeadline(time.Now().Add(searchReadDeadline)); err != nil {
			log.CtxError(ctx, "set SSDP read deadline: %v", err)
			return
		}
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			continue
		}
		st, mx, ok := parseMSearch(string(buf[:n]), deviceUUID)
		if !ok {
			continue
		}
		select {
		case responders <- struct{}{}:
			srcCopy := *src
			go func() {
				defer func() { <-responders }()
				timer := time.NewTimer(randomDelay(mx))
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				for _, target := range responseTargets(st, deviceUUID) {
					resp := buildSearchResponse(baseURL, serverName, target, time.Now().UTC())
					if _, err := conn.WriteToUDP([]byte(resp), &srcCopy); err != nil && ctx.Err() == nil {
						log.CtxWarn(ctx, "write SSDP response: %v", err)
					}
				}
			}()
		default:
			onDroppedSearch()
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
