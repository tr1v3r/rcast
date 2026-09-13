// Package gui provides the macOS menu bar front end for RCast.
//
// The package is split into three layers:
//
//   - menu.go (this file): a pure, testable menu model derived from a player
//     state snapshot and the GUI settings.
//   - gui.go: the portable controller that owns the server runtime, mirrors
//     the UPnP control semantics for menu actions, applies setting toggles
//     and drives a platform view.
//   - gui_darwin.go / gui_stub.go: the platform views. The darwin build
//     renders the model with fyne.io/systray; every other build (including
//     CGO_ENABLED=0) returns ErrUnsupported so headless usage is unaffected.
package gui

import (
	"strings"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/state"
)

// DeviceName is the friendly renderer name shown in the menu.
const DeviceName = "RCast"

// maxTitleRunes bounds the media title row; longer titles are truncated so
// the dropdown stays readable.
const maxTitleRunes = 48

// Action identifies an invokable menu command.
type Action string

const (
	// ActionPlayPause pauses when playing and (re)starts playback otherwise.
	ActionPlayPause Action = "play_pause"
	ActionStop      Action = "stop"
	ActionMute      Action = "mute"
	ActionQuit      Action = "quit"
)

// Toggle identifies a switchable settings row.
type Toggle string

const (
	ToggleFullscreen   Toggle = "fullscreen"
	ToggleLinkVolume   Toggle = "link_system_volume"
	ToggleAllowPreempt Toggle = "allow_preempt"
	ToggleDebug        Toggle = "debug_log"
)

// Settings are the toggleable preferences surfaced by the GUI. They overlay
// the base config loaded at startup; persistence is delegated through Deps.
type Settings struct {
	IINAFullscreen   bool
	LinkSystemVolume bool
	AllowPreempt     bool
	Debug            bool
}

// settingsFromConfig derives the initial toggle state from the loaded config
// (settings.json merged over env, see internal/config) and the CLI debug
// flag.
func settingsFromConfig(cfg config.Config, debug bool) Settings {
	return Settings{
		IINAFullscreen:   cfg.IINAFullscreen,
		LinkSystemVolume: cfg.LinkSystemOutputVolume,
		AllowPreempt:     cfg.AllowSessionPreempt,
		Debug:            cfg.DebugLog || debug,
	}
}

// Model is a complete, renderable snapshot of the menu bar dropdown.
type Model struct {
	// Device + human transport word, e.g. "RCast — Playing".
	Device    string
	Transport string
	// Title is the truncated media title; empty when nothing is loaded.
	Title string
	// VolumePct is the player volume in percent.
	VolumePct int
	// Controller is the session owner (client IP); empty without a session.
	Controller string
	// PlayPause is the transport control row.
	PlayPause PlayPauseRow
	// CanStop reports whether a stop would do anything.
	CanStop bool
	// Muted is the mute checkbox state.
	Muted bool
	// Toggles are the settings submenu rows, in display order.
	Toggles []ToggleRow
	// About is the version row text, e.g. "About RCast v1.2.3".
	About string
}

// PlayPauseRow is the pause/resume/play control derived from transport state.
type PlayPauseRow struct {
	Title   string
	Enabled bool
}

// ToggleRow is one checkable settings row.
type ToggleRow struct {
	ID      Toggle
	Title   string
	Checked bool
	// Restart marks toggles the running server can not adopt in place; the
	// controller restarts the server (between tracks) to apply them.
	Restart bool
}

// BuildModel derives the menu contents from a player state snapshot, the
// current settings and the injected version string.
func BuildModel(snap state.Snapshot, settings Settings, version string) Model {
	m := Model{
		Device:     DeviceName,
		Transport:  TransportWord(snap.TransportState),
		Title:      TruncateTitle(snap.Title),
		VolumePct:  snap.Volume,
		Controller: snap.SessionOwner,
		PlayPause:  playPauseRow(snap),
		CanStop:    snap.TransportState != "STOPPED" && snap.TransportState != "",
		Muted:      snap.Mute,
		Toggles: []ToggleRow{
			{ID: ToggleFullscreen, Title: "Open IINA in Fullscreen", Checked: settings.IINAFullscreen},
			{ID: ToggleLinkVolume, Title: "Link System Volume", Checked: settings.LinkSystemVolume, Restart: true},
			{ID: ToggleAllowPreempt, Title: "Allow Session Preemption", Checked: settings.AllowPreempt, Restart: true},
			{ID: ToggleDebug, Title: "Debug Logging", Checked: settings.Debug},
		},
		About: "About " + DeviceName + " v" + version,
	}
	return m
}

// TransportWord maps a UPnP transport state to a menu-friendly word.
func TransportWord(transport string) string {
	switch transport {
	case "PLAYING":
		return "Playing"
	case "PAUSED_PLAYBACK":
		return "Paused"
	case "TRANSITIONING":
		return "Loading"
	case "STOPPED", "":
		return "Idle"
	default:
		return transport
	}
}

// TruncateTitle shortens media titles to maxTitleRunes, appending an ellipsis.
func TruncateTitle(title string) string {
	title = strings.TrimSpace(title)
	runes := []rune(title)
	if len(runes) <= maxTitleRunes {
		return title
	}
	return string(runes[:maxTitleRunes-1]) + "…"
}

func playPauseRow(snap state.Snapshot) PlayPauseRow {
	switch snap.TransportState {
	case "PLAYING":
		return PlayPauseRow{Title: "Pause", Enabled: true}
	case "PAUSED_PLAYBACK":
		return PlayPauseRow{Title: "Resume", Enabled: true}
	case "TRANSITIONING":
		return PlayPauseRow{Title: "Pause", Enabled: false}
	case "STOPPED", "":
		// Restarting requires the last known URI.
		if snap.TransportURI == "" {
			return PlayPauseRow{Title: "Play", Enabled: false}
		}
		return PlayPauseRow{Title: "Play", Enabled: true}
	default:
		return PlayPauseRow{Title: "Play", Enabled: false}
	}
}
