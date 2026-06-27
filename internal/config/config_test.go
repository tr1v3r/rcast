package config

import (
	"path/filepath"
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
