package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "rcast-config-test-*")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("DMR_SETTINGS_PATH", filepath.Join(dir, "settings.json")); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	clearBooleanEnvironment(t)
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	t.Setenv("DMR_SETTINGS_PATH", path)
	want := Config{
		SettingsPath:           path,
		IINAFullscreen:         true,
		LinkSystemOutputVolume: true,
		AllowSessionPreempt:    false,
		DebugLog:               true,
	}

	if err := Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got := Load()
	if got.SettingsPath != path {
		t.Errorf("SettingsPath = %q, want %q", got.SettingsPath, path)
	}
	if got.IINAFullscreen != want.IINAFullscreen ||
		got.LinkSystemOutputVolume != want.LinkSystemOutputVolume ||
		got.AllowSessionPreempt != want.AllowSessionPreempt ||
		got.DebugLog != want.DebugLog {
		t.Fatalf("loaded booleans = %+v, want %+v", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat(settings) error = %v", err)
	}
	if info.Mode().Perm() != settingsFileMode {
		t.Errorf("settings permissions = %o, want %o", info.Mode().Perm(), settingsFileMode)
	}
}

func TestEnvironmentOverridesPersistedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	persisted := Config{
		SettingsPath:           path,
		IINAFullscreen:         false,
		LinkSystemOutputVolume: true,
		AllowSessionPreempt:    false,
		DebugLog:               false,
	}
	if err := Save(persisted); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	t.Setenv("DMR_SETTINGS_PATH", path)
	t.Setenv("DMR_IINA_FULLSCREEN", "true")
	t.Setenv("DMR_LINK_SYSTEM_VOLUME", "false")
	t.Setenv("DMR_ALLOW_PREEMPT", "true")
	t.Setenv("DMR_DEBUG_LOG", "true")
	got := Load()
	if !got.IINAFullscreen || got.LinkSystemOutputVolume || !got.AllowSessionPreempt || !got.DebugLog {
		t.Fatalf("environment did not override settings: %+v", got)
	}
}

func TestInvalidEnvironmentFallsBackToPersistedSetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Save(Config{
		SettingsPath:           path,
		IINAFullscreen:         true,
		LinkSystemOutputVolume: true,
		AllowSessionPreempt:    false,
		DebugLog:               true,
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	t.Setenv("DMR_SETTINGS_PATH", path)
	t.Setenv("DMR_IINA_FULLSCREEN", "invalid")
	t.Setenv("DMR_LINK_SYSTEM_VOLUME", "invalid")
	t.Setenv("DMR_ALLOW_PREEMPT", "invalid")
	t.Setenv("DMR_DEBUG_LOG", "invalid")
	got := Load()
	if !got.IINAFullscreen || !got.LinkSystemOutputVolume || got.AllowSessionPreempt || !got.DebugLog {
		t.Fatalf("invalid environment did not preserve persisted values: %+v", got)
	}
}

func TestMissingOrDamagedSettingsFallBackToDefaults(t *testing.T) {
	clearBooleanEnvironment(t)
	for name, contents := range map[string]string{
		"missing": "",
		"damaged": `{"iinaFullscreen": tru`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if name == "damaged" {
				if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
					t.Fatalf("write damaged settings: %v", err)
				}
			}
			t.Setenv("DMR_SETTINGS_PATH", path)

			got := Load()
			if got.IINAFullscreen || got.LinkSystemOutputVolume || !got.AllowSessionPreempt || got.DebugLog {
				t.Fatalf("fallback config = %+v", got)
			}
		})
	}
}

func TestSaveUsesAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	allFalse := Config{SettingsPath: path}
	allTrue := Config{
		SettingsPath:           path,
		IINAFullscreen:         true,
		LinkSystemOutputVolume: true,
		AllowSessionPreempt:    true,
		DebugLog:               true,
	}
	if err := Save(allFalse); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}

	const iterations = 20
	done := make(chan struct{})
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := range iterations {
			cfg := allFalse
			if i%2 == 0 {
				cfg = allTrue
			}
			if err := Save(cfg); err != nil {
				errCh <- fmt.Errorf("Save iteration %d: %w", i, err)
				return
			}
		}
	}()

readLoop:
	for {
		select {
		case <-done:
			break readLoop
		default:
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile during Save: %v", err)
		}
		var got savedSettings
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("observed partial JSON during Save: %v\n%s", err, data)
		}
		allSet := got.IINAFullscreen && got.LinkSystemVolume && got.AllowPreempt && got.DebugLog
		allClear := !got.IINAFullscreen && !got.LinkSystemVolume && !got.AllowPreempt && !got.DebugLog
		if !allSet && !allClear {
			t.Fatalf("observed mixed settings during atomic replacement: %+v", got)
		}
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}

	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".settings-*.tmp"))
	if err != nil {
		t.Fatalf("Glob temporary settings: %v", err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary settings files left behind: %v", temps)
	}
}

func TestSaveWithEmptySettingsPathUsesEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	t.Setenv("DMR_SETTINGS_PATH", path)
	if err := Save(Config{DebugLog: true}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var got savedSettings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !got.DebugLog {
		t.Fatal("Save() ignored DMR_SETTINGS_PATH destination")
	}
}

func clearBooleanEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DMR_IINA_FULLSCREEN",
		"DMR_LINK_SYSTEM_VOLUME",
		"DMR_ALLOW_PREEMPT",
		"DMR_DEBUG_LOG",
	} {
		t.Setenv(key, "")
	}
}
