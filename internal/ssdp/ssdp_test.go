package ssdp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tr1v3r/rcast/internal/upnp"
)

// --- Existing pure-function tests (unchanged) ---

func TestResponseTargets(t *testing.T) {
	const id = "uuid:test"
	all := responseTargets("ssdp:all", id)
	if len(all) != 6 {
		t.Fatalf("ssdp:all targets = %d, want 6", len(all))
	}
	cm := responseTargets(upnp.ConnectionManagerType, id)
	if len(cm) != 1 || cm[0].usn != id+"::"+upnp.ConnectionManagerType {
		t.Fatalf("connection manager response = %#v", cm)
	}
	if got := responseTargets("urn:unsupported", id); got != nil {
		t.Fatalf("unsupported target = %#v, want nil", got)
	}
}

func TestAdvertisedLocalAddr(t *testing.T) {
	addr := advertisedLocalAddr("http://192.0.2.10:8200")
	if addr == nil || addr.IP.String() != "192.0.2.10" {
		t.Fatalf("addr=%v", addr)
	}
	if addr := advertisedLocalAddr("not a URL"); addr != nil {
		t.Fatalf("invalid URL addr=%v, want nil", addr)
	}
}

// --- Deterministic fake UDP conn ---

type udpWrite struct {
	data string
	addr *net.UDPAddr
}

type readResult struct {
	data []byte
	src  *net.UDPAddr
	err  error
}

type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string { return "fake timeout" }

func (fakeTimeoutErr) Timeout() bool { return true }

func (fakeTimeoutErr) Temporary() bool { return true }

type fakeUDPConn struct {
	mu       sync.Mutex
	writes   []string
	toUDP    []udpWrite
	readCh   chan readResult
	writeErr error
}

func newFakeUDPConn() *fakeUDPConn {
	return &fakeUDPConn{readCh: make(chan readResult, 16)}
}

func (f *fakeUDPConn) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	f.writes = append(f.writes, string(b))
	return len(b), nil
}

func (f *fakeUDPConn) WriteToUDP(b []byte, a *net.UDPAddr) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	f.toUDP = append(f.toUDP, udpWrite{string(b), a})
	return len(b), nil
}

func (f *fakeUDPConn) ReadFromUDP(b []byte) (int, *net.UDPAddr, error) {
	r, ok := <-f.readCh
	if !ok {
		return 0, nil, fakeTimeoutErr{}
	}
	copy(b, r.data)
	return len(r.data), r.src, r.err
}

func (f *fakeUDPConn) SetDeadline(time.Time) error { return nil }

func (f *fakeUDPConn) SetReadBuffer(int) error { return nil }

func (f *fakeUDPConn) Close() error { return nil }

// snapshot returns copies safe for assertions.
func (f *fakeUDPConn) snapshot() (writes []string, toUDP []udpWrite) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writes = append([]string(nil), f.writes...)
	toUDP = append([]udpWrite(nil), f.toUDP...)
	return
}

// waitForWrites polls (2ms tick, 500ms cap) until at least `want` entries are
// present in the slice named by kind ("writes" or "toUDP").
func (f *fakeUDPConn) waitForWrites(t *testing.T, want int, kind string) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		var got int
		switch kind {
		case "writes":
			got = len(f.writes)
		case "toUDP":
			got = len(f.toUDP)
		default:
			f.mu.Unlock()
			t.Fatalf("unknown kind %q", kind)
		}
		f.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.mu.Lock()
	var got int
	switch kind {
	case "writes":
		got = len(f.writes)
	case "toUDP":
		got = len(f.toUDP)
	}
	f.mu.Unlock()
	t.Fatalf("waitForWrites(kind=%s): got %d, want >= %d", kind, got, want)
}

// --- Pure-function tests ---

func TestAliveTargets(t *testing.T) {
	const id = "uuid:abc"
	got := aliveTargets(id)
	want := []aliveTarget{
		{upnp.DeviceType, id + "::" + upnp.DeviceType},
		{upnp.AVTransportType, id + "::" + upnp.AVTransportType},
		{upnp.RenderingType, id + "::" + upnp.RenderingType},
		{upnp.ConnectionManagerType, id + "::" + upnp.ConnectionManagerType},
		{"upnp:rootdevice", id + "::upnp:rootdevice"},
		{id, id},
	}
	if len(got) != len(want) {
		t.Fatalf("aliveTargets len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("aliveTargets[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestBuildAliveMessage(t *testing.T) {
	const base = "http://192.0.2.5:8200"
	const server = "rcast/1.0 macOS/14"
	for _, x := range aliveTargets("uuid:z") {
		msg := buildAliveMessage(ssdpAddr, base, server, x.st, x.usn)
		for _, want := range []string{
			"NOTIFY * HTTP/1.1",
			"HOST: " + ssdpAddr,
			"CACHE-CONTROL: max-age=1800",
			"LOCATION: " + base + "/device.xml",
			"NT: " + x.st,
			"NTS: ssdp:alive",
			"SERVER: " + server,
			"USN: " + x.usn,
			"BOOTID.UPNP.ORG: 1",
			"CONFIGID.UPNP.ORG: 1",
			"\r\n\r\n",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("alive(%s) missing %q\nmsg:\n%s", x.st, want, msg)
			}
		}
	}
}

func TestBuildByebyeMessage(t *testing.T) {
	const st, usn = "upnp:rootdevice", "uuid:x::upnp:rootdevice"
	msg := buildByebyeMessage(ssdpAddr, st, usn)
	for _, want := range []string{
		"NOTIFY * HTTP/1.1",
		"HOST: " + ssdpAddr,
		"NT: " + st,
		"NTS: ssdp:byebye",
		"USN: " + usn,
		"\r\n\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("byebye missing %q\nmsg:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "LOCATION") {
		t.Errorf("byebye must not contain LOCATION: %s", msg)
	}
	if strings.Contains(msg, "CACHE-CONTROL") {
		t.Errorf("byebye must not contain CACHE-CONTROL: %s", msg)
	}
}

func TestBuildSearchResponse(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	target := responseTarget{st: "ssdp:all", usn: "uuid:r"}
	msg := buildSearchResponse("http://192.0.2.1:8200", "rcast/1.0", target, now)
	for _, want := range []string{
		"HTTP/1.1 200 OK",
		"CACHE-CONTROL: max-age=1800",
		"DATE: " + now.Format(http.TimeFormat),
		"EXT:",
		"LOCATION: http://192.0.2.1:8200/device.xml",
		"SERVER: rcast/1.0",
		"ST: ssdp:all",
		"USN: uuid:r",
		"BOOTID.UPNP.ORG: 1",
		"CONFIGID.UPNP.ORG: 1",
		"\r\n\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("search response missing %q\nmsg:\n%s", want, msg)
		}
	}
}

func TestHeaderValue(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		key  string
		want string
	}{
		{"case-insensitive key", "M-SEARCH * HTTP/1.1\r\nST: ssdp:all\r\n", "st", "ssdp:all"},
		{"surrounding whitespace", "X:  hello  \r\n", "X", "hello"},
		{"missing key", "FOO: bar\r\n", "BAZ", ""},
		{"multi-line first wins", "ST: a\r\nST: b\r\n", "ST", "a"},
		{"empty value", "ST:\r\n", "ST", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := headerValue(c.raw, c.key); got != c.want {
				t.Errorf("headerValue(%q,%q) = %q, want %q", c.raw, c.key, got, c.want)
			}
		})
	}
}

func TestParseMSearch(t *testing.T) {
	const id = "uuid:dev"
	mkPacket := func(st, mx string) string {
		mxLine := ""
		if mx != "" {
			mxLine = "MX: " + mx + "\r\n"
		}
		return fmt.Sprintf(
			"M-SEARCH * HTTP/1.1\r\nHOST: %s\r\nMAN: \"ssdp:discover\"\r\nST: %s\r\n%s\r\n",
			ssdpAddr, st, mxLine)
	}
	cases := []struct {
		name   string
		raw    string
		ok     bool
		wantST string
		wantMX int
	}{
		{"ssdp:all default mx", mkPacket("ssdp:all", ""), true, "ssdp:all", 1},
		{"rootdevice", mkPacket("upnp:rootdevice", "2"), true, "upnp:rootdevice", 2},
		{"device type", mkPacket(upnp.DeviceType, "3"), true, upnp.DeviceType, 3},
		{"avtransport", mkPacket(upnp.AVTransportType, ""), true, upnp.AVTransportType, 1},
		{"rendering", mkPacket(upnp.RenderingType, ""), true, upnp.RenderingType, 1},
		{"connection mgr", mkPacket(upnp.ConnectionManagerType, ""), true, upnp.ConnectionManagerType, 1},
		{"device uuid", mkPacket(id, ""), true, id, 1},
		{"mx clamp low", mkPacket("ssdp:all", "0"), true, "ssdp:all", 1},
		{"mx clamp high", mkPacket("ssdp:all", "9"), true, "ssdp:all", 5},
		{"mx non-numeric default", mkPacket("ssdp:all", "abc"), true, "ssdp:all", 1},
		{"not M-SEARCH", "POST * HTTP/1.1\r\nST: ssdp:all\r\n\r\n", false, "", 0},
		{"missing MAN", "M-SEARCH * HTTP/1.1\r\nST: ssdp:all\r\n\r\n", false, "", 0},
		{"wrong MAN", "M-SEARCH * HTTP/1.1\r\nMAN: \"SSDP:OTHER\"\r\nST: ssdp:all\r\n\r\n", false, "", 0},
		{"missing ST", "M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\n\r\n", false, "", 0},
		{"unsupported ST", mkPacket("urn:bogus:thing", ""), false, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, mx, ok := parseMSearch(c.raw, id)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (st=%q mx=%d)", ok, c.ok, st, mx)
			}
			if ok && st != c.wantST {
				t.Errorf("st = %q, want %q", st, c.wantST)
			}
			if ok && mx != c.wantMX {
				t.Errorf("mx = %d, want %d", mx, c.wantMX)
			}
		})
	}
}

// --- Loop tests ---

// withFakeDial swaps dialAnnounce + announceInterval and restores them on cleanup.
func withFakeDial(t *testing.T, conn packetConn, interval time.Duration) {
	t.Helper()
	origDial, origInterval := dialAnnounce, announceInterval
	dialAnnounce = func(*net.UDPAddr) (packetConn, error) { return conn, nil }
	announceInterval = interval
	t.Cleanup(func() {
		dialAnnounce = origDial
		announceInterval = origInterval
	})
}

func withFakeListen(t *testing.T, conn *fakeUDPConn) {
	t.Helper()
	orig := listenMulticast
	listenMulticast = func() (packetConn, error) { return conn, nil }
	t.Cleanup(func() { listenMulticast = orig })
}

func withRandomDelay(t *testing.T, fn func(mx int) time.Duration) {
	t.Helper()
	orig := randomDelay
	randomDelay = fn
	t.Cleanup(func() { randomDelay = orig })
}

func TestAnnounceHappy(t *testing.T) {
	conn := newFakeUDPConn()
	// Use a long interval so the alive-burst ticker never fires between the
	// initial 6 writes and cancel(); otherwise an extra batch would make the
	// final "exactly 12" assertion racy under load.
	withFakeDial(t, conn, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Announce(ctx, "http://192.0.2.1:8200", "uuid:happy", "rcast/1.0")
		close(done)
	}()

	// Initial burst: 6 alive messages, one per ST.
	conn.waitForWrites(t, 6, "writes")
	writes, _ := conn.snapshot()
	aliveNTs := collectNTs(t, writes, "ssdp:alive")
	if len(aliveNTs) != 6 {
		t.Fatalf("alive NT count = %d, want 6: %v", len(aliveNTs), aliveNTs)
	}
	wantAlive := map[string]bool{
		upnp.DeviceType: true, upnp.AVTransportType: true, upnp.RenderingType: true,
		upnp.ConnectionManagerType: true, "upnp:rootdevice": true, "uuid:happy": true,
	}
	for nt := range aliveNTs {
		if !wantAlive[nt] {
			t.Errorf("unexpected alive NT %q", nt)
		}
	}

	// Cancel -> 6 byebye messages, then the goroutine returns.
	cancel()
	conn.waitForWrites(t, 12, "writes")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Announce did not return after cancel")
	}

	writes, _ = conn.snapshot()
	if len(writes) != 12 {
		t.Fatalf("total writes = %d, want 12", len(writes))
	}
	byebyeNTs := collectNTs(t, writes[6:], "ssdp:byebye")
	if len(byebyeNTs) != 6 {
		t.Fatalf("byebye NT count = %d, want 6: %v", len(byebyeNTs), byebyeNTs)
	}
	for nt := range byebyeNTs {
		if !wantAlive[nt] {
			t.Errorf("unexpected byebye NT %q", nt)
		}
	}
}

func collectNTs(t *testing.T, msgs []string, nts string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range msgs {
		if !strings.Contains(m, "NTS: "+nts) {
			continue
		}
		nt := headerValue(m, "NT")
		if nt == "" {
			t.Errorf("message missing NT: %s", m)
			continue
		}
		out[nt] = true
	}
	return out
}

// errorCountingConn records every Write attempt (in `attempts`) but always
// returns the configured error, so the production loop's "log and continue"
// path can be exercised deterministically.
type errorCountingConn struct {
	*fakeUDPConn
	attempts int
}

func (e *errorCountingConn) Write(b []byte) (int, error) {
	e.mu.Lock()
	e.attempts++
	e.mu.Unlock()
	return 0, e.writeErr
}

func TestAnnounceWriteErrorContinues(t *testing.T) {
	base := newFakeUDPConn()
	base.writeErr = errors.New("boom")
	conn := &errorCountingConn{fakeUDPConn: base}
	withFakeDial(t, conn, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Announce(ctx, "http://192.0.2.1:8200", "uuid:werr", "rcast/1.0")
		close(done)
	}()

	// All 6 alive writes are attempted even though each errors.
	waitForAttempts(t, conn, 6)

	cancel()
	// 6 more byebye attempts land before the goroutine returns.
	waitForAttempts(t, conn, 12)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Announce did not return after cancel (write-error path)")
	}
}

func waitForAttempts(t *testing.T, c *errorCountingConn, want int) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := c.attempts
		c.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	c.mu.Lock()
	got := c.attempts
	c.mu.Unlock()
	t.Fatalf("waitForAttempts: got %d, want >= %d", got, want)
}

func TestAnnounceDialFails(t *testing.T) {
	orig := dialAnnounce
	dialAnnounce = func(*net.UDPAddr) (packetConn, error) { return nil, errors.New("nope") }
	t.Cleanup(func() { dialAnnounce = orig })

	done := make(chan struct{})
	go func() {
		Announce(context.Background(), "http://192.0.2.1:8200", "uuid:df", "rcast/1.0")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Announce should return immediately when dial fails")
	}
}

func TestSearchResponderHappyAll(t *testing.T) {
	conn := newFakeUDPConn()
	withFakeListen(t, conn)
	withRandomDelay(t, func(int) time.Duration { return 0 })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SearchResponder(ctx, "http://192.0.2.1:8200", "uuid:happy", "rcast/1.0")
		close(done)
	}()

	src := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1900}
	conn.readCh <- readResult{
		data: []byte("M-SEARCH * HTTP/1.1\r\nHOST: " + ssdpAddr +
			"\r\nMAN: \"ssdp:discover\"\r\nST: ssdp:all\r\nMX: 1\r\n\r\n"),
		src: src,
	}
	// ssdp:all produces 6 responses.
	conn.waitForWrites(t, 6, "toUDP")

	// Drive a timeout to verify the loop survives, then cancel.
	conn.readCh <- readResult{err: fakeTimeoutErr{}}
	cancel()
	close(conn.readCh)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("SearchResponder did not return after cancel")
	}

	_, toUDP := conn.snapshot()
	if len(toUDP) != 6 {
		t.Fatalf("responses = %d, want 6", len(toUDP))
	}
	seenST := map[string]bool{}
	for _, w := range toUDP {
		if w.addr.String() != src.String() {
			t.Errorf("response addr = %s, want %s", w.addr, src)
		}
		if !strings.Contains(w.data, "HTTP/1.1 200 OK") {
			t.Errorf("response not 200 OK: %s", w.data)
		}
		seenST[headerValue(w.data, "ST")] = true
	}
	if len(seenST) != 6 {
		t.Errorf("distinct response STs = %d, want 6: %v", len(seenST), seenST)
	}
}

func TestSearchResponderSingleST(t *testing.T) {
	conn := newFakeUDPConn()
	withFakeListen(t, conn)
	withRandomDelay(t, func(int) time.Duration { return 0 })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SearchResponder(ctx, "http://192.0.2.1:8200", "uuid:single", "rcast/1.0")
		close(done)
	}()

	conn.readCh <- readResult{
		data: []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: " +
			upnp.AVTransportType + "\r\nMX: 1\r\n\r\n"),
		src: &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 1900},
	}
	conn.waitForWrites(t, 1, "toUDP")

	cancel()
	close(conn.readCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("SearchResponder did not return")
	}

	_, toUDP := conn.snapshot()
	if len(toUDP) != 1 {
		t.Fatalf("responses = %d, want 1", len(toUDP))
	}
	if st := headerValue(toUDP[0].data, "ST"); st != upnp.AVTransportType {
		t.Errorf("response ST = %q, want %q", st, upnp.AVTransportType)
	}
}

func TestSearchResponderIgnoresBadPackets(t *testing.T) {
	conn := newFakeUDPConn()
	withFakeListen(t, conn)
	withRandomDelay(t, func(int) time.Duration { return 0 })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SearchResponder(ctx, "http://192.0.2.1:8200", "uuid:ign", "rcast/1.0")
		close(done)
	}()

	src := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 3), Port: 1900}
	// (a) not M-SEARCH, (b) missing MAN, (c) unsupported ST — none should respond.
	bad := []string{
		"POST * HTTP/1.1\r\nST: ssdp:all\r\n\r\n",
		"M-SEARCH * HTTP/1.1\r\nST: ssdp:all\r\n\r\n",
		"M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: urn:bogus\r\n\r\n",
	}
	for _, p := range bad {
		conn.readCh <- readResult{data: []byte(p), src: src}
		// Give the loop a moment to process; assert no response appears.
		time.Sleep(20 * time.Millisecond)
		if _, toUDP := conn.snapshot(); len(toUDP) != 0 {
			t.Fatalf("bad packet %q produced a response: %v", p, toUDP)
		}
	}
	// A valid packet should now respond.
	conn.readCh <- readResult{
		data: []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: ssdp:all\r\nMX: 1\r\n\r\n"),
		src:  src,
	}
	conn.waitForWrites(t, 6, "toUDP")

	cancel()
	close(conn.readCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("SearchResponder did not return")
	}
}

func TestSearchResponderReadTimeoutContinues(t *testing.T) {
	conn := newFakeUDPConn()
	withFakeListen(t, conn)
	withRandomDelay(t, func(int) time.Duration { return 0 })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SearchResponder(ctx, "http://192.0.2.1:8200", "uuid:to", "rcast/1.0")
		close(done)
	}()

	// Timeout read first (ctx not done, so loop continues).
	conn.readCh <- readResult{err: fakeTimeoutErr{}}
	// Then a valid packet responds.
	conn.readCh <- readResult{
		data: []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: ssdp:all\r\nMX: 1\r\n\r\n"),
		src:  &net.UDPAddr{IP: net.IPv4(10, 0, 0, 4), Port: 1900},
	}
	conn.waitForWrites(t, 6, "toUDP")

	cancel()
	close(conn.readCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("SearchResponder did not return")
	}
}

func TestSearchResponderNonTimeoutReadErrorContinues(t *testing.T) {
	conn := newFakeUDPConn()
	withFakeListen(t, conn)
	withRandomDelay(t, func(int) time.Duration { return 0 })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SearchResponder(ctx, "http://192.0.2.1:8200", "uuid:nte", "rcast/1.0")
		close(done)
	}()

	// Generic (non-timeout) read error: loop should continue (no response, no exit).
	conn.readCh <- readResult{err: errors.New("transient")}
	// Then a valid packet responds, proving the loop survived.
	conn.readCh <- readResult{
		data: []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: ssdp:all\r\nMX: 1\r\n\r\n"),
		src:  &net.UDPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 1900},
	}
	conn.waitForWrites(t, 6, "toUDP")

	cancel()
	close(conn.readCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("SearchResponder did not return")
	}
}

// TestSearchResponderCapDrop verifies the responder-saturation drop branch
// deterministically. responderCap is 1; the injected randomDelay signals once
// the single responder has taken the slot (so extras are guaranteed to find it
// full) and parks on a release channel; onDroppedSearch confirms each extra was
// dropped before release, so none can be processed afterwards. No fixed sleeps.
func TestSearchResponderCapDrop(t *testing.T) {
	conn := newFakeUDPConn()
	withFakeListen(t, conn)

	origCap := responderCap
	responderCap = 1
	t.Cleanup(func() { responderCap = origCap })

	parked := make(chan struct{}, 1)
	release := make(chan struct{})
	withRandomDelay(t, func(int) time.Duration {
		select { // signal: a responder holds the slot and is now parking
		case parked <- struct{}{}:
		default:
		}
		<-release
		return 0
	})

	origDrop := onDroppedSearch
	drops := make(chan struct{}, 8)
	onDroppedSearch = func() {
		select {
		case drops <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { onDroppedSearch = origDrop })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SearchResponder(ctx, "http://192.0.2.1:8200", "uuid:cap", "rcast/1.0")
		close(done)
	}()

	src := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 6), Port: 1900}
	pkt := []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: ssdp:all\r\nMX: 1\r\n\r\n")
	// First packet grabs the only slot; wait until its responder has parked.
	conn.readCh <- readResult{data: pkt, src: src}
	waitSignal(t, parked, "first responder to park")

	// Three more packets: the slot is full, so each is dropped. Wait for all
	// three drops so none remain queued to be (wrongly) served after release.
	for i := 0; i < 3; i++ {
		conn.readCh <- readResult{data: pkt, src: src}
	}
	for i := 0; i < 3; i++ {
		waitSignal(t, drops, "drop")
	}

	if _, toUDP := conn.snapshot(); len(toUDP) != 0 {
		t.Fatalf("expected 0 responses while responder parked, got %d", len(toUDP))
	}

	// Release the parked responder: exactly one batch of 6 responses lands.
	close(release)
	conn.waitForWrites(t, 6, "toUDP")
	if _, toUDP := conn.snapshot(); len(toUDP) != 6 {
		t.Fatalf("expected exactly 6 responses after release, got %d (drop branch not honored)", len(toUDP))
	}

	cancel()
	close(conn.readCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("SearchResponder did not return")
	}
}

// waitSignal waits for one signal on ch, failing the test after a generous
// timeout. Used instead of fixed sleeps so loop tests stay deterministic under
// load.
func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
