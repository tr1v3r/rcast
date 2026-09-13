package ssdp

// Regression tests for the SSDP resilience fixes (audit H1, M6, M7, L1, L2 in
// output/reports/t3-net-layer-audit.md and output/reports/bug-audit-summary.md).

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- hook helpers ---

// withAnnounceInterval swaps announceInterval for the test duration.
func withAnnounceInterval(t *testing.T, d time.Duration) {
	t.Helper()
	orig := announceInterval
	announceInterval = d
	t.Cleanup(func() { announceInterval = orig })
}

// withStartupRetry collapses the socket-setup retry backoff to d.
func withStartupRetry(t *testing.T, d time.Duration) {
	t.Helper()
	origBase, origMax := startupRetryBase, startupRetryMax
	startupRetryBase, startupRetryMax = d, d
	t.Cleanup(func() { startupRetryBase, startupRetryMax = origBase, origMax })
}

// withListenConn installs listenMulticast returning conn (any packetConn,
// including error-instrumenting wrappers).
func withListenConn(t *testing.T, conn packetConn) {
	t.Helper()
	orig := listenMulticast
	listenMulticast = func() (packetConn, error) { return conn, nil }
	t.Cleanup(func() { listenMulticast = orig })
}

// --- H1: persistent read errors must back off, not busy-spin ---

// errorReadConn fails every read with a persistent non-timeout error and
// counts attempts, so tests can prove the loop backs off instead of spinning.
type errorReadConn struct {
	*fakeUDPConn
	reads atomic.Int64
}

func (c *errorReadConn) ReadFromUDP([]byte) (int, *net.UDPAddr, error) {
	c.reads.Add(1)
	return 0, nil, errors.New("interface removed")
}

// closedReadConn fails every read like a socket closed underneath the loop.
type closedReadConn struct {
	*fakeUDPConn
}

func (c *closedReadConn) ReadFromUDP([]byte) (int, *net.UDPAddr, error) {
	return 0, nil, fmt.Errorf("read ssdp: %w", net.ErrClosed)
}

func TestSearchResponderPersistentReadErrorBacksOffAndCancels(t *testing.T) {
	conn := &errorReadConn{fakeUDPConn: newFakeUDPConn()}
	withListenConn(t, conn)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SearchResponder(ctx, "http://192.0.2.1:8200", "uuid:h1", "rcast/1.0")
		close(done)
	}()

	// Let the loop chew on persistent errors for 300ms. With the default
	// 100ms→1s backoff this yields a handful of reads; the pre-fix busy loop
	// was measured at ~20.5M reads/second (audit H1).
	time.Sleep(300 * time.Millisecond)
	reads := conn.reads.Load()
	if reads > 50 {
		t.Fatalf("read loop spun: %d reads in 300ms (busy-loop regression, want a handful)", reads)
	}
	if reads < 2 {
		t.Fatalf("expected the loop to keep retrying, got %d reads", reads)
	}

	// Cancelling mid-error-stream must stop the goroutine promptly.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("SearchResponder did not exit after cancel (H1 goroutine leak)")
	}
	// And it must stay stopped.
	after := conn.reads.Load()
	time.Sleep(150 * time.Millisecond)
	if got := conn.reads.Load(); got != after {
		t.Fatalf("reads continued after exit: %d -> %d", after, got)
	}
}

func TestSearchResponderReturnsOnClosedConn(t *testing.T) {
	withListenConn(t, &closedReadConn{fakeUDPConn: newFakeUDPConn()})

	done := make(chan struct{})
	go func() {
		// No cancellation at all: the closed-socket error alone must end the
		// loop instead of spinning forever on it.
		SearchResponder(context.Background(), "http://192.0.2.1:8200", "uuid:closed", "rcast/1.0")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("SearchResponder kept running after its socket was closed")
	}
}

// --- M6: socket setup retries instead of permanently giving up ---

// dialRecorder is an injectable dialAnnounce fake that records the local
// address of every attempt, fails the first failN attempts, and hands out a
// fresh fake conn per success.
type dialRecorder struct {
	mu     sync.Mutex
	locals []*net.UDPAddr
	conns  []*fakeUDPConn
	failN  int
}

func (d *dialRecorder) dial(local *net.UDPAddr) (packetConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.locals = append(d.locals, local)
	if len(d.locals) <= d.failN {
		return nil, fmt.Errorf("dial attempt %d failed", len(d.locals))
	}
	c := newFakeUDPConn()
	d.conns = append(d.conns, c)
	return c, nil
}

func (d *dialRecorder) install(t *testing.T) {
	t.Helper()
	orig := dialAnnounce
	dialAnnounce = d.dial
	t.Cleanup(func() { dialAnnounce = orig })
}

func (d *dialRecorder) dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.locals)
}

func (d *dialRecorder) conn(i int) *fakeUDPConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[i]
}

func (d *dialRecorder) local(i int) *net.UDPAddr {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.locals[i]
}

func waitForDials(t *testing.T, rec *dialRecorder, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec.dials() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("dial attempts = %d, want >= %d", rec.dials(), want)
}

func TestAnnounceRetriesDialUntilSuccess(t *testing.T) {
	withStartupRetry(t, time.Millisecond)
	withAnnounceInterval(t, time.Hour)

	rec := &dialRecorder{failN: 2}
	rec.install(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Announce(ctx, "http://192.0.2.1:8200", "uuid:m6a", "rcast/1.0")
		close(done)
	}()

	// Two failed dials, then a successful one. The alive burst proves the
	// loop eventually came up instead of giving up after the first failure.
	waitForDials(t, rec, 3)
	rec.conn(0).waitForWrites(t, 6, "writes")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Announce did not return after cancel")
	}
	if got := rec.dials(); got != 3 {
		t.Fatalf("dial attempts = %d, want exactly 3", got)
	}
}

func TestSearchResponderRetriesListenUntilSuccess(t *testing.T) {
	conn := newFakeUDPConn()
	var calls atomic.Int32
	orig := listenMulticast
	listenMulticast = func() (packetConn, error) {
		if calls.Add(1) <= 2 {
			return nil, errors.New("network not ready")
		}
		return conn, nil
	}
	t.Cleanup(func() { listenMulticast = orig })
	withStartupRetry(t, time.Millisecond)
	withRandomDelay(t, func(int) time.Duration { return 0 })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SearchResponder(ctx, "http://192.0.2.1:8200", "uuid:m6r", "rcast/1.0")
		close(done)
	}()

	waitForListenCalls(t, &calls, 3)

	// The retried listener actually serves searches end to end.
	conn.readCh <- readResult{
		data: []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: ssdp:all\r\nMX: 1\r\n\r\n"),
		src:  &net.UDPAddr{IP: net.IPv4(10, 0, 0, 9), Port: 1900},
	}
	conn.waitForWrites(t, 6, "toUDP")

	cancel()
	close(conn.readCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("SearchResponder did not return after cancel")
	}
}

func waitForListenCalls(t *testing.T, calls *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("listen attempts = %d, want >= %d", calls.Load(), want)
}

// --- M7: advertised LOCATION follows host IP changes ---

func TestAnnounceRebindsSocketOnIPChange(t *testing.T) {
	withAnnounceInterval(t, 10*time.Millisecond)

	var hostIP atomic.Value
	hostIP.Store("192.0.2.1")
	origIP := currentHostIP
	currentHostIP = func() string { return hostIP.Load().(string) }
	t.Cleanup(func() { currentHostIP = origIP })

	rec := &dialRecorder{}
	rec.install(t)

	loc := NewBaseURLSource("http://192.0.2.1:8200")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		AnnounceTracking(ctx, loc, "uuid:m7", "rcast/1.0")
		close(done)
	}()

	// First burst goes out on the original IP.
	waitForDials(t, rec, 1)
	rec.conn(0).waitForWrites(t, 6, "writes")

	// The host IP moves (192.0.2.1 -> 198.51.100.7).
	hostIP.Store("198.51.100.7")

	// Within a few announce intervals the socket is re-dialed bound to the
	// new IP and the shared URL refreshed.
	waitForDials(t, rec, 2)
	if l := rec.local(1); l == nil || l.IP.String() != "198.51.100.7" {
		t.Fatalf("rebind local addr = %v, want 198.51.100.7", l)
	}
	waitForBaseURL(t, loc, "http://198.51.100.7:8200")

	// The rebound socket announces the new LOCATION.
	rec.conn(1).waitForWrites(t, 6, "writes")
	msgs, _ := rec.conn(1).snapshot()
	if len(msgs) == 0 {
		t.Fatalf("no alive messages on rebound socket")
	}
	for _, m := range msgs {
		if !strings.Contains(m, "LOCATION: http://198.51.100.7:8200/device.xml") {
			t.Fatalf("alive message carries stale LOCATION: %s", m)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("AnnounceTracking did not return after cancel")
	}
}

func TestSearchResponderPicksUpBaseURLChange(t *testing.T) {
	conn := newFakeUDPConn()
	withFakeListen(t, conn)
	withRandomDelay(t, func(int) time.Duration { return 0 })

	loc := NewBaseURLSource("http://192.0.2.1:8200")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SearchResponderTracking(ctx, loc, "uuid:loc", "rcast/1.0")
		close(done)
	}()

	src := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 7), Port: 1900}
	pkt := []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: ssdp:all\r\nMX: 1\r\n\r\n")

	// Initial responses carry the original LOCATION.
	conn.readCh <- readResult{data: pkt, src: src}
	conn.waitForWrites(t, 6, "toUDP")
	_, toUDP := conn.snapshot()
	for _, w := range toUDP {
		if !strings.Contains(w.data, "LOCATION: http://192.0.2.1:8200/device.xml") {
			t.Fatalf("initial response carries wrong LOCATION: %s", w.data)
		}
	}

	// After a host IP move, subsequent responses must use the new LOCATION
	// instead of the one captured at startup.
	loc.Set("http://198.51.100.7:8200")
	conn.readCh <- readResult{data: pkt, src: src}
	conn.waitForWrites(t, 12, "toUDP")
	_, toUDP = conn.snapshot()
	for _, w := range toUDP[6:] {
		if !strings.Contains(w.data, "LOCATION: http://198.51.100.7:8200/device.xml") {
			t.Fatalf("post-change response carries stale LOCATION: %s", w.data)
		}
	}

	cancel()
	close(conn.readCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("SearchResponderTracking did not return after cancel")
	}
}

func waitForBaseURL(t *testing.T, loc *BaseURLSource, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if loc.Get() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("base URL = %q, want %q", loc.Get(), want)
}

func TestBaseURLSourceRefreshIP(t *testing.T) {
	orig := currentHostIP
	t.Cleanup(func() { currentHostIP = orig })

	t.Run("tracking rewrites ip preserving port", func(t *testing.T) {
		currentHostIP = func() string { return "198.51.100.7" }
		s := NewBaseURLSource("http://192.0.2.1:8200")
		if !s.refreshIP() {
			t.Fatal("refreshIP = false, want true on IP change")
		}
		if got, want := s.Get(), "http://198.51.100.7:8200"; got != want {
			t.Fatalf("Get() = %q, want %q", got, want)
		}
	})

	t.Run("same ip is a no-op", func(t *testing.T) {
		currentHostIP = func() string { return "192.0.2.1" }
		s := NewBaseURLSource("http://192.0.2.1:8200")
		if s.refreshIP() {
			t.Fatal("refreshIP = true, want false when IP unchanged")
		}
	})

	t.Run("fixed source never tracks", func(t *testing.T) {
		currentHostIP = func() string { return "198.51.100.7" }
		s := NewFixedBaseURL("http://192.0.2.1:8200")
		if s.refreshIP() {
			t.Fatal("refreshIP = true, want false for fixed source")
		}
		if got, want := s.Get(), "http://192.0.2.1:8200"; got != want {
			t.Fatalf("fixed source URL changed to %q", got)
		}
	})

	t.Run("unknown ip keeps previous", func(t *testing.T) {
		currentHostIP = func() string { return "" }
		s := NewBaseURLSource("http://192.0.2.1:8200")
		if s.refreshIP() {
			t.Fatal("refreshIP = true, want false when IP unknown")
		}
	})

	t.Run("unparsable url keeps previous", func(t *testing.T) {
		currentHostIP = func() string { return "198.51.100.7" }
		s := NewBaseURLSource("http://192.0.2.1:8200")
		s.Set("://bad")
		if s.refreshIP() {
			t.Fatal("refreshIP = true, want false on unparsable URL")
		}
		if got, want := s.Get(), "://bad"; got != want {
			t.Fatalf("URL rewritten to %q despite parse failure", got)
		}
	})
}

// --- L1: MAN header parsed as a header, not a packet-wide substring ---

func TestParseMSearchMANHeaderForms(t *testing.T) {
	const id = "uuid:man"
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"quoted with space", "M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: ssdp:all\r\n\r\n", true},
		{"quoted without space after colon", "M-SEARCH * HTTP/1.1\r\nMAN:\"ssdp:discover\"\r\nST: ssdp:all\r\n\r\n", true},
		{"unquoted value", "M-SEARCH * HTTP/1.1\r\nMAN: ssdp:discover\r\nST: ssdp:all\r\n\r\n", true},
		{"case-insensitive value", "M-SEARCH * HTTP/1.1\r\nMAN: \"SSDP:Discover\"\r\nST: ssdp:all\r\n\r\n", true},
		{"missing MAN header", "M-SEARCH * HTTP/1.1\r\nST: ssdp:all\r\n\r\n", false},
		{"wrong MAN value", "M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:fresh\"\r\nST: ssdp:all\r\n\r\n", false},
		{"magic string only in unrelated header value", "M-SEARCH * HTTP/1.1\r\nX-Custom: MAN: \"ssdp:discover\"\r\nST: ssdp:all\r\n\r\n", false},
		{"magic string only in X-MAN header", "M-SEARCH * HTTP/1.1\r\nX-MAN: \"ssdp:discover\"\r\nST: ssdp:all\r\n\r\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, ok := parseMSearch(c.raw, id)
			if ok != c.ok {
				t.Fatalf("parseMSearch ok = %v, want %v\nraw: %q", ok, c.ok, c.raw)
			}
		})
	}
}

// --- L2: multicast join eligibility covers all IPv4 interfaces ---

func TestMulticastEligible(t *testing.T) {
	cidr := func(s string) net.Addr {
		t.Helper()
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("parse cidr %s: %v", s, err)
		}
		return n
	}
	cases := []struct {
		name  string
		ifi   net.Interface
		addrs []net.Addr
		want  bool
	}{
		{"up iface with global v4", net.Interface{Name: "en0", Flags: net.FlagUp | net.FlagBroadcast}, []net.Addr{cidr("192.168.1.5/24")}, true},
		{"down iface", net.Interface{Name: "en1"}, []net.Addr{cidr("192.168.1.5/24")}, false},
		{"loopback", net.Interface{Name: "lo0", Flags: net.FlagUp | net.FlagLoopback}, []net.Addr{cidr("127.0.0.1/8")}, false},
		{"v6 only", net.Interface{Name: "en0", Flags: net.FlagUp}, []net.Addr{cidr("fe80::1/64")}, false},
		{"link-local v4 only", net.Interface{Name: "en0", Flags: net.FlagUp}, []net.Addr{cidr("169.254.1.5/16")}, false},
		{"global v4 among v6", net.Interface{Name: "en0", Flags: net.FlagUp}, []net.Addr{cidr("fe80::1/64"), cidr("10.0.0.5/24")}, true},
		{"no addrs", net.Interface{Name: "en0", Flags: net.FlagUp}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := multicastEligible(c.ifi, c.addrs); got != c.want {
				t.Fatalf("multicastEligible(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}
