package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	settingsDirMode  = 0o700
	settingsFileMode = 0o600
)

type persistedSettings struct {
	IINAFullscreen   *bool `json:"iinaFullscreen,omitempty"`
	LinkSystemVolume *bool `json:"linkSystemVolume,omitempty"`
	AllowPreempt     *bool `json:"allowPreempt,omitempty"`
	DebugLog         *bool `json:"debugLog,omitempty"`
}

type savedSettings struct {
	IINAFullscreen   bool `json:"iinaFullscreen"`
	LinkSystemVolume bool `json:"linkSystemVolume"`
	AllowPreempt     bool `json:"allowPreempt"`
	DebugLog         bool `json:"debugLog"`
}

func loadPersisted(path string) persistedSettings {
	data, err := os.ReadFile(path)
	if err != nil {
		return persistedSettings{}
	}

	var settings persistedSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return persistedSettings{}
	}
	return settings
}

func applyPersisted(cfg *Config, settings persistedSettings) {
	if settings.IINAFullscreen != nil {
		cfg.IINAFullscreen = *settings.IINAFullscreen
	}
	if settings.LinkSystemVolume != nil {
		cfg.LinkSystemOutputVolume = *settings.LinkSystemVolume
	}
	if settings.AllowPreempt != nil {
		cfg.AllowSessionPreempt = *settings.AllowPreempt
	}
	if settings.DebugLog != nil {
		cfg.DebugLog = *settings.DebugLog
	}
}

// Save atomically persists the GUI-controlled boolean settings. SettingsPath
// selects the destination; when empty, DMR_SETTINGS_PATH or the default path is
// used. The destination directory is created when necessary.
func Save(cfg Config) error {
	path := cfg.SettingsPath
	if path == "" {
		home, _ := os.UserHomeDir()
		path = envVar("DMR_SETTINGS_PATH", filepath.Join(home, DefaultSettingsPath))
	}

	data, err := json.MarshalIndent(savedSettings{
		IINAFullscreen:   cfg.IINAFullscreen,
		LinkSystemVolume: cfg.LinkSystemOutputVolume,
		AllowPreempt:     cfg.AllowSessionPreempt,
		DebugLog:         cfg.DebugLog,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, settingsDirMode); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary settings file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(settingsFileMode); err != nil {
		return fmt.Errorf("set settings permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close settings: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	committed = true
	return nil
}
