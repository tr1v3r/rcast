package netutil

import (
	"net"
	"testing"
)

func TestUsableIPv4(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"192.168.1.10", true},
		{"10.0.0.2", true},
		{"127.0.0.1", false},
		{"169.254.1.2", false},
		{"::1", false},
		{"0.0.0.0", false},
	}
	for _, tt := range tests {
		if got := usableIPv4(net.ParseIP(tt.ip)); got != tt.want {
			t.Errorf("usableIPv4(%q)=%v, want %v", tt.ip, got, tt.want)
		}
	}
}
