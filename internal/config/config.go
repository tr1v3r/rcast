package config

import (
	"os"
	"path/filepath"
	"strconv"
)

const (
	DefaultPort         = 8200
	DefaultUUIDPath     = ".local/rcast/dmr_uuid.txt"
	DefaultSettingsPath = ".local/rcast/settings.json"
)

type Config struct {
	UUIDPath               string
	SettingsPath           string
	AllowSessionPreempt    bool
	LinkSystemOutputVolume bool
	HTTPPort               int
	AdvertiseIP            string
	IINAFullscreen         bool
	DebugLog               bool
}

func Load() Config {
	home, _ := os.UserHomeDir()
	settingsPath := envVar("DMR_SETTINGS_PATH", filepath.Join(home, DefaultSettingsPath))
	cfg := Config{
		UUIDPath:            envVar("DMR_UUID_PATH", filepath.Join(home, DefaultUUIDPath)),
		SettingsPath:        settingsPath,
		AllowSessionPreempt: true,
		HTTPPort:            envVar("DMR_HTTP_PORT", DefaultPort),
		AdvertiseIP:         envVar("DMR_ADVERTISE_IP", ""),
	}

	applyPersisted(&cfg, loadPersisted(settingsPath))

	// Environment variables remain the highest-priority configuration layer.
	cfg.AllowSessionPreempt = envVar("DMR_ALLOW_PREEMPT", cfg.AllowSessionPreempt)
	cfg.LinkSystemOutputVolume = envVar("DMR_LINK_SYSTEM_VOLUME", cfg.LinkSystemOutputVolume)
	cfg.IINAFullscreen = envVar("DMR_IINA_FULLSCREEN", cfg.IINAFullscreen)
	cfg.DebugLog = envVar("DMR_DEBUG_LOG", cfg.DebugLog)

	// Validate configuration
	cfg.validate()

	return cfg
}

func envVar[T ~string | ~bool | ~int](key string, def T) T {
	v := os.Getenv(key)
	if v == "" {
		return def
	}

	switch any(def).(type) {
	case string:
		return any(v).(T)
	case bool:
		if b, err := strconv.ParseBool(v); err == nil {
			return any(b).(T)
		}
	case int:
		if i, err := strconv.Atoi(v); err == nil {
			return any(i).(T)
		}
	case int64:
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return any(i).(T)
		}
	case float64:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return any(f).(T)
		}
	}
	return def
}

// validate performs validation on configuration values
func (c *Config) validate() {
	// Validate HTTP port range
	if c.HTTPPort < 1 || c.HTTPPort > 65535 {
		c.HTTPPort = DefaultPort
	}

}
