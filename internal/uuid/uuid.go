package uuid

import (
	"os"
	"path/filepath"
	"strings"
)

func LoadOrCreate(path, fallback string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		s := strings.TrimSpace(string(b))
		if s != "" {
			if !strings.HasPrefix(s, "uuid:") {
				s = "uuid:" + s
			}
			return s, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fallback, err
	}
	if err := os.WriteFile(path, []byte(fallback+"\n"), 0o644); err != nil {
		return fallback, err
	}
	return fallback, nil
}
