package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvironmentAndValidation(t *testing.T) {
	t.Setenv("DMR_HTTP_PORT", "99999")
	t.Setenv("DMR_ALLOW_PREEMPT", "false")
	t.Setenv("DMR_ADVERTISE_IP", "192.0.2.5")
	t.Setenv("DMR_UUID_PATH", filepath.Join(t.TempDir(), "uuid"))

	cfg := Load()
	if cfg.HTTPPort != DefaultPort {
		t.Fatalf("invalid port=%d, want default %d", cfg.HTTPPort, DefaultPort)
	}
	if cfg.AllowSessionPreempt {
		t.Fatal("DMR_ALLOW_PREEMPT=false was ignored")
	}
	if cfg.AdvertiseIP != "192.0.2.5" {
		t.Fatalf("advertise IP=%q", cfg.AdvertiseIP)
	}
}

func TestInvalidEnvironmentFallsBack(t *testing.T) {
	t.Setenv("DMR_HTTP_PORT", "not-a-number")
	t.Setenv("DMR_LINK_SYSTEM_VOLUME", "not-a-bool")
	cfg := Load()
	if cfg.HTTPPort != DefaultPort || cfg.LinkSystemOutputVolume {
		t.Fatalf("unexpected fallback config: %+v", cfg)
	}
}

func TestLoadDefaultsWhenEnvUnset(t *testing.T) {
	// Ensure none of the env vars we test are set so defaults win.
	for _, k := range []string{
		"DMR_UUID_PATH", "DMR_ALLOW_PREEMPT", "DMR_LINK_SYSTEM_VOLUME",
		"DMR_HTTP_PORT", "DMR_ADVERTISE_IP", "DMR_IINA_FULLSCREEN",
	} {
		t.Setenv(k, "")
	}
	cfg := Load()
	if cfg.HTTPPort != DefaultPort {
		t.Errorf("HTTPPort = %d, want %d", cfg.HTTPPort, DefaultPort)
	}
	if !cfg.AllowSessionPreempt {
		t.Error("AllowSessionPreempt default = false, want true")
	}
	if cfg.LinkSystemOutputVolume {
		t.Error("LinkSystemOutputVolume default = true, want false")
	}
	if cfg.IINAFullscreen {
		t.Error("IINAFullscreen default = true, want false")
	}
	if cfg.AdvertiseIP != "" {
		t.Errorf("AdvertiseIP = %q, want empty", cfg.AdvertiseIP)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, DefaultUUIDPath)
	if cfg.UUIDPath != want {
		t.Errorf("UUIDPath = %q, want %q", cfg.UUIDPath, want)
	}
	if !strings.HasSuffix(cfg.UUIDPath, DefaultUUIDPath) {
		t.Errorf("UUIDPath %q should end with %q", cfg.UUIDPath, DefaultUUIDPath)
	}
}

func TestCustomPortRoundTrip(t *testing.T) {
	t.Setenv("DMR_HTTP_PORT", "9000")
	cfg := Load()
	if cfg.HTTPPort != 9000 {
		t.Fatalf("HTTPPort = %d, want 9000", cfg.HTTPPort)
	}
}

func TestBoolVariants(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"1", true},
		{"false", false},
		{"FALSE", false},
		{"0", false},
		{"yes", false}, // not a valid bool → falls back to default
	}
	for _, c := range cases {
		t.Run("AllowPreempt_"+c.env, func(t *testing.T) {
			t.Setenv("DMR_ALLOW_PREEMPT", c.env)
			// Default for AllowSessionPreempt is true; for non-bool input we
			// expect the default to come back.
			got := Load().AllowSessionPreempt
			if c.env == "yes" {
				if !got {
					t.Fatalf("invalid bool %q → got false, want default true", c.env)
				}
				return
			}
			if got != c.want {
				t.Fatalf("DMR_ALLOW_PREEMPT=%q → %v, want %v", c.env, got, c.want)
			}
		})
	}

	for _, c := range cases {
		t.Run("LinkSystemVolume_"+c.env, func(t *testing.T) {
			t.Setenv("DMR_LINK_SYSTEM_VOLUME", c.env)
			// Default for LinkSystemOutputVolume is false; invalid → false.
			got := Load().LinkSystemOutputVolume
			if c.env == "yes" {
				if got {
					t.Fatalf("invalid bool %q → got true, want default false", c.env)
				}
				return
			}
			if got != c.want {
				t.Fatalf("DMR_LINK_SYSTEM_VOLUME=%q → %v, want %v", c.env, got, c.want)
			}
		})
	}
}

func TestInvalidPortBoundariesFallBackToDefault(t *testing.T) {
	cases := []string{"0", "-1", "65536", "99999", "abc"}
	for _, p := range cases {
		t.Run("port_"+p, func(t *testing.T) {
			t.Setenv("DMR_HTTP_PORT", p)
			if got := Load().HTTPPort; got != DefaultPort {
				t.Fatalf("port %q → %d, want default %d", p, got, DefaultPort)
			}
		})
	}
}

func TestValidPortBoundariesKept(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"1", 1},
		{"65535", 65535},
	}
	for _, c := range cases {
		t.Run("port_"+c.env, func(t *testing.T) {
			t.Setenv("DMR_HTTP_PORT", c.env)
			if got := Load().HTTPPort; got != c.want {
				t.Fatalf("port %s → %d, want %d", c.env, got, c.want)
			}
		})
	}
}

func TestEmptyStringFallsBackToDefault(t *testing.T) {
	// An env var set to "" must fall back to the default, not parse to empty/zero.
	t.Setenv("DMR_HTTP_PORT", "")
	t.Setenv("DMR_ADVERTISE_IP", "")
	t.Setenv("DMR_ALLOW_PREEMPT", "")
	cfg := Load()
	if cfg.HTTPPort != DefaultPort {
		t.Errorf("empty HTTPPort = %d, want default %d", cfg.HTTPPort, DefaultPort)
	}
	if cfg.AdvertiseIP != "" {
		t.Errorf("empty AdvertiseIP = %q, want \"\"", cfg.AdvertiseIP)
	}
	if !cfg.AllowSessionPreempt {
		t.Error("empty AllowSessionPreempt = false, want default true")
	}
}

func TestUUIDPathCustom(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "my-uuid")
	t.Setenv("DMR_UUID_PATH", custom)
	if got := Load().UUIDPath; got != custom {
		t.Fatalf("UUIDPath = %q, want %q", got, custom)
	}
}

func TestIINAFullscreenEnv(t *testing.T) {
	t.Setenv("DMR_IINA_FULLSCREEN", "true")
	if !Load().IINAFullscreen {
		t.Fatal("IINAFullscreen = false, want true")
	}
	t.Setenv("DMR_IINA_FULLSCREEN", "0")
	if Load().IINAFullscreen {
		t.Fatal("IINAFullscreen = true, want false")
	}
}

// NOTE: envVar's generic constraint is `~string | ~bool | ~int`, which excludes
// int64 and float64 — so the int64/float64 type-switch branches inside envVar
// are currently unreachable from generic instantiation. That is a latent dead-
// code issue in config.go (not exercised here because the constraint forbids
// those types), flagged but not fixed per the no-production-change rule for
// this package.
