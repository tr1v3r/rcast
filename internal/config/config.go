package config

import (
	"os"
	"strconv"
)

const (
	DefaultUUID     = "uuid:0199ffd9-6856-74cc-a2f2-4c74af0161b1"
	DefaultPort     = 8200
	DefaultUUIDPath = ".local/rcast/dmr_uuid.txt"
)

type Config struct {
	UUIDPath               string
	AllowSessionPreempt    bool
	LinkSystemOutputVolume bool
	HTTPPort               int
}

func Load() Config {
	return Config{
		UUIDPath:               envVar("DMR_UUID_PATH", os.Getenv("HOME")+"/"+DefaultUUIDPath),
		AllowSessionPreempt:    envVar("DMR_ALLOW_PREEMPT", true),
		LinkSystemOutputVolume: envVar("DMR_LINK_SYSTEM_VOLUME", false),
		HTTPPort:               envVar("DMR_HTTP_PORT", DefaultPort),
	}
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
