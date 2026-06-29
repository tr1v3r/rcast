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
	if !strings.HasPrefix(first, "uuid:") {
		t.Fatalf("generated UUID missing prefix: %q", first)
	}

	// The persisted file on disk should match the returned id (modulo the
	// trailing newline the writer appends).
	content, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if strings.TrimSpace(string(content)) != first {
		t.Fatalf("persisted file %q != returned id %q", strings.TrimSpace(string(content)), first)
	}
}

func TestLoadOrCreateReturnsExistingValidUUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uuid")
	const existing = "uuid:11112222-3333-4444-5555-666677778888"
	if err := os.WriteFile(path, []byte(existing+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("read existing: %v", err)
	}
	if id != existing {
		t.Fatalf("existing UUID returned as %q, want %q", id, existing)
	}
	// File should not have been rewritten.
	content, _ := os.ReadFile(path)
	if strings.TrimSpace(string(content)) != existing {
		t.Fatalf("file was rewritten despite being valid: %q", string(content))
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
	if strings.HasPrefix(id, "not-a-uuid") {
		t.Fatalf("repair did not replace garbage: %q", id)
	}
	// File rewritten with the new id.
	content, _ := os.ReadFile(path)
	if strings.TrimSpace(string(content)) != id {
		t.Fatalf("file not rewritten after repair: %q", string(content))
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
	if _, err := googleuuid.Parse(strings.TrimPrefix(id, "uuid:")); err != nil {
		t.Fatalf("migrated UUID is invalid: %q (%v)", id, err)
	}
	// File on disk reflects the migrated id.
	content, _ := os.ReadFile(path)
	if strings.TrimSpace(string(content)) != id {
		t.Fatalf("legacy file not rewritten: %q", string(content))
	}
}

func TestLoadOrCreateReadErrorNotIsNotExist(t *testing.T) {
	// A path that is itself a directory makes os.ReadFile fail with EISDIR,
	// which is not an IsNotExist error — should be wrapped and returned.
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreate(subdir)
	if err == nil {
		t.Fatal("LoadOrCreate on a directory path returned nil error")
	}
	if !strings.Contains(err.Error(), "reading UUID file") {
		t.Fatalf("error not wrapped as read failure: %q", err.Error())
	}
}

func TestLoadOrCreateDirectoryCreationFailure(t *testing.T) {
	// Make a parent directory that exists but is read-only, then request a UUID
	// at parent/newsub/uuid. os.ReadFile fails with IsNotExist (newsub missing)
	// so we fall through to os.MkdirAll(parent/newsub), which fails with EACCES
	// because parent is not writable.
	//
	// Skip when running as root (root bypasses permission checks).
	if os.Geteuid() == 0 {
		t.Skip("directory creation failure test requires non-root user")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) }) // restore so TempDir cleanup works

	path := filepath.Join(parent, "newsub", "uuid")
	_, err := LoadOrCreate(path)
	if err == nil {
		t.Fatal("LoadOrCreate returned nil error for unwritable parent")
	}
	if !strings.Contains(err.Error(), "creating UUID directory") {
		t.Fatalf("error not wrapped as directory creation failure: %q", err.Error())
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantOK  bool
		wantOut string
	}{
		{"prefixed", "uuid:11112222-3333-4444-5555-666677778888", true, "uuid:11112222-3333-4444-5555-666677778888"},
		{"unprefixed", "11112222-3333-4444-5555-666677778888", true, "uuid:11112222-3333-4444-5555-666677778888"},
		{"whitespace", "  uuid:11112222-3333-4444-5555-666677778888  ", true, "uuid:11112222-3333-4444-5555-666677778888"},
		{"garbage", "garbage", false, ""},
		{"empty", "", false, ""},
		{"partial", "uuid:11112222", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := normalize([]byte(c.input))
			if ok != c.wantOK {
				t.Fatalf("normalize(%q) ok = %v, want %v", c.input, ok, c.wantOK)
			}
			if ok && got != c.wantOut {
				t.Errorf("normalize(%q) = %q, want %q", c.input, got, c.wantOut)
			}
		})
	}
}
