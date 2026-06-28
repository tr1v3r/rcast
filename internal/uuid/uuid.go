package uuid

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	googleuuid "github.com/google/uuid"
)

const legacyDefaultUUID = "uuid:0199ffd9-6856-74cc-a2f2-4c74af0161b1"

func LoadOrCreate(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if id, ok := normalize(b); ok {
			if id != legacyDefaultUUID {
				return id, nil
			}
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading UUID file: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("creating UUID directory: %w", err)
	}

	id := "uuid:" + googleuuid.NewString()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dmr_uuid-*")
	if err != nil {
		return "", fmt.Errorf("creating temporary UUID file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("setting UUID file permissions: %w", err)
	}
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("writing UUID file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("syncing UUID file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("closing UUID file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("installing UUID file: %w", err)
	}
	return id, nil
}

func normalize(b []byte) (string, bool) {
	s := strings.TrimSpace(string(b))
	s = strings.TrimPrefix(s, "uuid:")
	id, err := googleuuid.Parse(s)
	if err != nil {
		return "", false
	}
	return "uuid:" + id.String(), true
}
