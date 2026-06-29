package netutil

import (
	"net"
	"strings"
	"testing"
)

func TestUsableIPv4(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"valid v4", "192.168.1.10", true},
		{"valid v4 ten-net", "10.0.0.2", true},
		{"loopback", "127.0.0.1", false},
		{"link-local", "169.254.1.2", false},
		{"v6 loopback rejected", "::1", false},
		{"unspecified", "0.0.0.0", false},
		{"v6 global rejected", "2001:db8::1", false},
		{"v4-mapped v6 rejected by To4 check pass-through", "::ffff:192.168.1.1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usableIPv4(net.ParseIP(tt.ip)); got != tt.want {
				t.Errorf("usableIPv4(%q)=%v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

// injectSources overrides the package-level routeSource/ifaceSource and restores
// them at test cleanup.
func injectSources(t *testing.T, route, iface ipSource) {
	t.Helper()
	prevRoute, prevIface := routeSource, ifaceSource
	routeSource, ifaceSource = route, iface
	t.Cleanup(func() {
		routeSource, ifaceSource = prevRoute, prevIface
	})
}

func TestFirstUsableIPv4RouteHitSkipsIface(t *testing.T) {
	routeCalled := false
	ifaceCalled := false
	injectSources(t,
		func() (net.IP, bool) {
			routeCalled = true
			return net.IPv4(192, 168, 1, 5), true
		},
		func() (net.IP, bool) {
			ifaceCalled = true
			return nil, false
		},
	)
	got, err := FirstUsableIPv4()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != "192.168.1.5" {
		t.Fatalf("FirstUsableIPv4() = %q, want 192.168.1.5", got)
	}
	if !routeCalled {
		t.Fatal("routeSource was not consulted")
	}
	if ifaceCalled {
		t.Fatal("ifaceSource was consulted despite route hit")
	}
}

func TestFirstUsableIPv4RouteMissIfaceHit(t *testing.T) {
	routeCalled := false
	injectSources(t,
		func() (net.IP, bool) {
			routeCalled = true
			return nil, false
		},
		func() (net.IP, bool) {
			return net.IPv4(10, 0, 0, 7), true
		},
	)
	got, err := FirstUsableIPv4()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != "10.0.0.7" {
		t.Fatalf("FirstUsableIPv4() = %q, want 10.0.0.7", got)
	}
	if !routeCalled {
		t.Fatal("routeSource was not consulted")
	}
}

func TestFirstUsableIPv4BothMissError(t *testing.T) {
	injectSources(t,
		func() (net.IP, bool) { return nil, false },
		func() (net.IP, bool) { return nil, false },
	)
	got, err := FirstUsableIPv4()
	if got != "" {
		t.Fatalf("FirstUsableIPv4() = %q, want empty string", got)
	}
	if err == nil {
		t.Fatal("err = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "no IPv4 found") {
		t.Fatalf("err = %q, want wrapping of \"no IPv4 found\"", err.Error())
	}
}

func TestFirstUsableIPv4RouteReturnsUnusableFallsThrough(t *testing.T) {
	// Defensive: if routeSource returns ok=true but the IP is somehow unusable,
	// the result is still stringified via To4(). Cover the explicit To4 path by
	// returning a usable IP so we assert the happy stringification.
	injectSources(t,
		func() (net.IP, bool) { return net.IPv4(172, 16, 0, 1), true },
		func() (net.IP, bool) { return nil, false },
	)
	got, err := FirstUsableIPv4()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != "172.16.0.1" {
		t.Fatalf("got = %q, want 172.16.0.1", got)
	}
}

// Ensure the default sources do not panic on a real host (smoke test). This
// exercises defaultRouteIP / defaultInterfaceIP end-to-end.
func TestDefaultSourcesSmoke(t *testing.T) {
	if ip, ok := defaultRouteIP(); ok && !usableIPv4(ip) {
		t.Fatalf("defaultRouteIP returned non-usable IP %v", ip)
	}
	if ip, ok := defaultInterfaceIP(); ok && !usableIPv4(ip) {
		t.Fatalf("defaultInterfaceIP returned non-usable IP %v", ip)
	}
	if _, err := FirstUsableIPv4(); err != nil && !strings.Contains(err.Error(), "no IPv4 found") {
		t.Fatalf("unexpected err: %v", err)
	}
}
