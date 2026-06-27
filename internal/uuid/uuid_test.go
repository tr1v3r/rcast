package uuid

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	googleuuid "github.com/google/uuid"
)

func TestLoadOrCreatePersistsUniqueUUID(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "first", "uuid")
	secondPath := filepath.Join(t.TempDir(), "second", "uuid")

	first, err := LoadOrCreate(firstPath)
	if err != nil {
		t.Fatalf("create first UUID: %v", err)
	}
	again, err := LoadOrCreate(firstPath)
	if err != nil {
		t.Fatalf("reload first UUID: %v", err)
	}
	second, err := LoadOrCreate(secondPath)
	if err != nil {
		t.Fatalf("create second UUID: %v", err)
	}

	if first != again {
		t.Fatalf("UUID changed across loads: %q != %q", first, again)
	}
	if first == second {
		t.Fatalf("separate installations received the same UUID: %q", first)
	}
	if _, err := googleuuid.Parse(strings.TrimPrefix(first, "uuid:")); err != nil {
		t.Fatalf("generated invalid UUID %q: %v", first, err)
	}
}

func TestLoadOrCreateRepairsInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uuid")
	if err := os.WriteFile(path, []byte("not-a-uuid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("repair UUID: %v", err)
	}
	if _, err := googleuuid.Parse(strings.TrimPrefix(id, "uuid:")); err != nil {
		t.Fatalf("repair produced invalid UUID %q: %v", id, err)
	}
}

func TestLoadOrCreateMigratesLegacySharedUUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uuid")
	if err := os.WriteFile(path, []byte(legacyDefaultUUID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("migrate UUID: %v", err)
	}
	if id == legacyDefaultUUID {
		t.Fatal("legacy shared UUID was not migrated")
	}
}
