package config

import (
	"os"
	"strconv"
)

const (
	DefaultUUID     = "uuid:12345678-90ab-cdef-1234-567890abcdef"
	DefaultPort     = 8200
	DefaultUUIDPath = "./dmr_uuid.txt"
)

type Config struct {
	UUIDPath               string
	AllowSessionPreempt    bool
	LinkSystemOutputVolume bool
	HTTPPort               int
}

func Load() Config {
	cfg := Config{
		UUIDPath:               envStr("DMR_UUID_PATH", DefaultUUIDPath),
		AllowSessionPreempt:    envBool("DMR_ALLOW_PREEMPT", true),
		LinkSystemOutputVolume: envBool("DMR_LINK_SYSTEM_VOLUME", false),
		HTTPPort:               envInt("DMR_HTTP_PORT", DefaultPort),
	}
	return cfg
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		i, err := strconv.Atoi(v)
		if err == nil {
			return i
		}
	}
	return def
}
