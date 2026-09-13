package ssdp

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/netutil"
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

// BaseURLSource is the advertised base URL shared by the SSDP loops. A
// tracking source (NewBaseURLSource) rewrites itself when the host's usable
// IPv4 changes, so the advertised LOCATION stops going stale after a network
// switch (audit M7); a fixed source (NewFixedBaseURL) pins the URL, e.g. when
// DMR_ADVERTISE_IP is configured.
type BaseURLSource struct {
	mu    sync.RWMutex
	url   string
	track bool
}

// NewBaseURLSource returns a source that tracks host IP changes.
func NewBaseURLSource(base string) *BaseURLSource {
	return &BaseURLSource{url: base, track: true}
}

// NewFixedBaseURL returns a source whose URL never changes.
func NewFixedBaseURL(base string) *BaseURLSource {
	return &BaseURLSource{url: base}
}

// Get returns the current base URL.
func (s *BaseURLSource) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.url
}

// Set replaces the base URL outright.
func (s *BaseURLSource) Set(base string) {
	s.mu.Lock()
	s.url = base
	s.mu.Unlock()
}

// refreshIP re-resolves the host IP for tracking sources and, when it
// changed, rewrites the URL preserving scheme, port and path. It reports
// whether the URL changed. An unresolvable IP leaves the source untouched.
func (s *BaseURLSource) refreshIP() bool {
	if !s.track {
		return false
	}
	ip := currentHostIP()
	if ip == "" {
		return false
	}
	parsed, err := url.Parse(s.Get())
	if err != nil {
		return false
	}
	if parsed.Hostname() == ip {
		return false
	}
	host := ip
	if port := parsed.Port(); port != "" {
		host = net.JoinHostPort(ip, port)
	}
	s.mu.Lock()
	s.url = fmt.Sprintf("%s://%s%s", parsed.Scheme, host, parsed.Path)
	s.mu.Unlock()
	return true
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
		conn, err := net.ListenMulticastUDP("udp4", nil, addr)
		if err != nil {
			return nil, err
		}
		// Joining on the default interface alone leaves other interfaces'
		// M-SEARCH unanswered on multi-homed hosts (audit L2). Extra joins
		// are best effort.
		joinAllMulticastInterfaces(conn)
		return conn, nil
	}
	announceInterval   = 30 * time.Second
	searchReadDeadline = 2 * time.Second
	responderCap       = 32
	// startupRetry* bound the exponential backoff between socket setup
	// retries. At boot (launchd/login items) the network may not be up yet;
	// giving up after a single attempt left the device undiscoverable until
	// a manual restart (audit M6).
	startupRetryBase = time.Second
	startupRetryMax  = 30 * time.Second
	// readError* bound the exponential backoff after persistent
	// non-timeout read errors, which used to busy-spin a full core in a
	// goroutine that could never be cancelled (audit H1).
	readErrorBackoff    = 100 * time.Millisecond
	readErrorBackoffMax = time.Second
	// currentHostIP reports the host's currently usable IPv4 for IP-change
	// tracking, or "" when it cannot be determined (in which case tracking
	// keeps the previous URL).
	currentHostIP = func() string {
		ip, err := netutil.FirstUsableIPv4()
		if err != nil {
			return ""
		}
		return ip
	}
	randomDelay = func(mx int) time.Duration {
		return time.Duration(rand.Int63n(int64(time.Duration(mx) * time.Second)))
	}
	// onDroppedSearch is a test hook invoked when an M-SEARCH is dropped because
	// the responder cap is full. It is a no-op in production.
	onDroppedSearch = func() {}
)

// multicastGroup is the SSDP group address used for interface joins.
var multicastGroup = &net.UDPAddr{IP: net.ParseIP("239.255.255.250"), Port: 1900}

// joinAllMulticastInterfaces joins the SSDP multicast group on every eligible
// IPv4 interface, not just the system default (audit L2). Individual join
// failures (e.g. re-joining the interface the kernel already picked for the
// default membership) are skipped.
func joinAllMulticastInterfaces(conn packetConn) {
	uc, ok := conn.(*net.UDPConn)
	if !ok {
		return
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	joined := 0
	for _, ifi := range ifaces {
		addrs, _ := ifi.Addrs()
		if !multicastEligible(ifi, addrs) {
			continue
		}
		if err := joinGroupOnInterface(uc, multicastGroup.IP, addrs); err != nil {
			continue
		}
		joined++
	}
	if joined > 0 {
		log.Info("SSDP multicast group joined on %d interface(s)", joined)
	}
}

// joinGroupOnInterface adds the interface behind addrs to the socket's SSDP
// multicast membership via the raw socket option — the equivalent of the
// JoinGroup method net.UDPConn no longer exports. Like the stdlib's own
// joinIPv4Group, mreq.Interface carries the interface's IPv4 address.
func joinGroupOnInterface(uc *net.UDPConn, group net.IP, addrs []net.Addr) error {
	v4 := group.To4()
	if v4 == nil {
		return errors.New("ssdp: group address is not IPv4")
	}
	var mreq syscall.IPMreq
	copy(mreq.Multiaddr[:], v4)
	found := false
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if a4 := ipn.IP.To4(); a4 != nil {
				copy(mreq.Interface[:], a4)
				found = true
				break
			}
		}
	}
	if !found {
		return errors.New("ssdp: interface has no IPv4 address")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptIPMreq(int(fd), syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, &mreq)
	})
	if ctrlErr != nil {
		return ctrlErr
	}
	return sockErr
}

// multicastEligible reports whether an interface should join the SSDP
// multicast group: up, non-loopback, and carrying a non-link-local IPv4
// address.
func multicastEligible(ifi net.Interface, addrs []net.Addr) bool {
	if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip := ipn.IP.To4(); ip != nil && !ip.IsLinkLocalUnicast() {
				return true
			}
		}
	}
	return false
}

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
	// Per UPnP the MAN header value is `"ssdp:discover"`; some stacks omit
	// the space after the colon or the quotes. Parse the header field itself
	// instead of substring-matching the raw packet, so the magic string
	// hiding inside an unrelated header is rejected (audit L1).
	man := strings.Trim(strings.TrimSpace(headerValue(raw, "MAN")), `"`)
	if !strings.EqualFold(man, "ssdp:discover") {
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

// Announce runs the ssdp:alive/byebye loop with a fixed advertised base URL.
func Announce(ctx context.Context, baseURL, deviceUUID, serverName string) {
	announceLoop(ctx, NewFixedBaseURL(baseURL), deviceUUID, serverName)
}

// AnnounceTracking runs the alive/byebye loop against a shared base URL. When
// the host IP changes the URL is refreshed and the announce socket rebound,
// so the advertised LOCATION never goes stale (audit M7).
func AnnounceTracking(ctx context.Context, loc *BaseURLSource, deviceUUID, serverName string) {
	announceLoop(ctx, loc, deviceUUID, serverName)
}

// dialWithRetry dials the announce socket, retrying with exponential backoff
// until it connects or ctx is done (audit M6).
func dialWithRetry(ctx context.Context, local *net.UDPAddr) (packetConn, bool) {
	backoff := startupRetryBase
	for {
		conn, err := dialAnnounce(local)
		if err == nil {
			return conn, true
		}
		log.CtxWarn(ctx, "SSDP announce socket: %v; retrying in %s", err, backoff)
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, startupRetryMax)
	}
}

// listenWithRetry listens on the SSDP multicast group, retrying with
// exponential backoff until it succeeds or ctx is done (audit M6).
func listenWithRetry(ctx context.Context) (packetConn, bool) {
	backoff := startupRetryBase
	for {
		conn, err := listenMulticast()
		if err == nil {
			return conn, true
		}
		log.CtxWarn(ctx, "listen SSDP multicast: %v; retrying in %s", err, backoff)
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, startupRetryMax)
	}
}

// sendAliveBurst writes one ssdp:alive per target and returns how many writes
// succeeded. Failures are logged — they usually mean the advertised IP went
// away and the socket needs a rebuild (audit M7: they were silently
// swallowed, behind a comment claiming otherwise).
func sendAliveBurst(ctx context.Context, conn packetConn, baseURL, serverName string, usns []aliveTarget) int {
	written := 0
	for _, x := range usns {
		msg := buildAliveMessage(ssdpAddr, baseURL, serverName, x.st, x.usn)
		if _, err := conn.Write([]byte(msg)); err != nil {
			log.CtxWarn(ctx, "write ssdp:alive (%s): %v", x.st, err)
			continue
		}
		written++
	}
	return written
}

// sendByebyeBurst writes the ssdp:byebye shutdown messages, logging failures.
func sendByebyeBurst(ctx context.Context, conn packetConn, usns []aliveTarget) {
	for _, x := range usns {
		msg := buildByebyeMessage(ssdpAddr, x.st, x.usn)
		if _, err := conn.Write([]byte(msg)); err != nil {
			log.CtxWarn(ctx, "write ssdp:byebye (%s): %v", x.st, err)
		}
	}
}

func announceLoop(ctx context.Context, loc *BaseURLSource, deviceUUID, serverName string) {
	usns := aliveTargets(deviceUUID)

	conn, ok := dialWithRetry(ctx, advertisedLocalAddr(loc.Get()))
	if !ok {
		return
	}
	defer func() { _ = conn.Close() }()

	ticker := time.NewTicker(announceInterval)
	defer ticker.Stop()

	for {
		if sendAliveBurst(ctx, conn, loc.Get(), serverName, usns) == 0 && loc.refreshIP() {
			// Every write failed and the host IP moved: rebind now instead
			// of waiting out the announce interval.
			if next, ok := rebindAnnounceConn(ctx, conn, loc); ok {
				conn = next
				continue
			}
			return
		}
		select {
		case <-ctx.Done():
			sendByebyeBurst(ctx, conn, usns)
			return
		case <-ticker.C:
			if loc.refreshIP() {
				if next, ok := rebindAnnounceConn(ctx, conn, loc); ok {
					conn = next
				} else {
					return
				}
			}
		}
	}
}

// rebindAnnounceConn closes conn and re-dials bound to the advertised IP now
// held by loc. It reports false when ctx was cancelled while retrying.
func rebindAnnounceConn(ctx context.Context, conn packetConn, loc *BaseURLSource) (packetConn, bool) {
	_ = conn.Close()
	next, ok := dialWithRetry(ctx, advertisedLocalAddr(loc.Get()))
	if !ok {
		return nil, false
	}
	log.CtxInfo(ctx, "SSDP announce socket rebound after advertised IP change; base URL now %s", loc.Get())
	return next, true
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

// SearchResponder answers M-SEARCH requests with a fixed advertised base URL.
func SearchResponder(ctx context.Context, baseURL, deviceUUID, serverName string) {
	respondLoop(ctx, NewFixedBaseURL(baseURL), deviceUUID, serverName)
}

// SearchResponderTracking answers M-SEARCH requests using the shared base
// URL, so responses follow host IP changes instead of advertising a stale
// LOCATION (audit M7).
func SearchResponderTracking(ctx context.Context, loc *BaseURLSource, deviceUUID, serverName string) {
	respondLoop(ctx, loc, deviceUUID, serverName)
}

func respondLoop(ctx context.Context, loc *BaseURLSource, deviceUUID, serverName string) {
	conn, ok := listenWithRetry(ctx)
	if !ok {
		return
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadBuffer(65536); err != nil {
		log.CtxWarn(ctx, "set SSDP read buffer: %v", err)
	}
	buf := make([]byte, 8192)
	responders := make(chan struct{}, responderCap)

	backoff := time.Duration(0)
	for {
		if err := conn.SetDeadline(time.Now().Add(searchReadDeadline)); err != nil {
			log.CtxError(ctx, "set SSDP read deadline: %v", err)
			return
		}
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				// Socket closed underneath us; nothing more to read.
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			// Persistent non-timeout read error (interface removed, network
			// stack hiccup): back off exponentially instead of busy-spinning,
			// and stay responsive to cancellation (audit H1: this branch
			// used to spin ~20M iterations/s and leaked the goroutine).
			if backoff == 0 {
				backoff = readErrorBackoff
			} else {
				backoff = min(backoff*2, readErrorBackoffMax)
			}
			log.CtxWarn(ctx, "SSDP read error: %v; backing off %s", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}
		backoff = 0
		st, mx, ok := parseMSearch(string(buf[:n]), deviceUUID)
		if !ok {
			continue
		}
		select {
		case responders <- struct{}{}:
			srcCopy := *src
			// Read per packet so LOCATION follows base URL changes.
			base := loc.Get()
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
					resp := buildSearchResponse(base, serverName, target, time.Now().UTC())
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
