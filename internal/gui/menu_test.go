package gui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/state"
)

func TestBuildModelTransportStates(t *testing.T) {
	cases := []struct {
		name        string
		transport   string
		wantWord    string
		wantRow     PlayPauseRow
		wantCanStop bool
	}{
		{
			name:        "playing pauses",
			transport:   "PLAYING",
			wantWord:    "Playing",
			wantRow:     PlayPauseRow{Title: "Pause", Enabled: true},
			wantCanStop: true,
		},
		{
			name:        "paused resumes",
			transport:   "PAUSED_PLAYBACK",
			wantWord:    "Paused",
			wantRow:     PlayPauseRow{Title: "Resume", Enabled: true},
			wantCanStop: true,
		},
		{
			name:        "transitioning disables control",
			transport:   "TRANSITIONING",
			wantWord:    "Loading",
			wantRow:     PlayPauseRow{Title: "Pause", Enabled: false},
			wantCanStop: true,
		},
		{
			name:        "stopped without uri cannot play",
			transport:   "STOPPED",
			wantWord:    "Idle",
			wantRow:     PlayPauseRow{Title: "Play", Enabled: false},
			wantCanStop: false,
		},
		{
			name:        "empty transport is idle",
			transport:   "",
			wantWord:    "Idle",
			wantRow:     PlayPauseRow{Title: "Play", Enabled: false},
			wantCanStop: false,
		},
		{
			name:        "unknown transport falls back",
			transport:   "RECORDING",
			wantWord:    "RECORDING",
			wantRow:     PlayPauseRow{Title: "Play", Enabled: false},
			wantCanStop: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := BuildModel(state.Snapshot{TransportState: tc.transport}, Settings{}, "test")
			if m.Transport != tc.wantWord {
				t.Errorf("Transport = %q, want %q", m.Transport, tc.wantWord)
			}
			if m.PlayPause != tc.wantRow {
				t.Errorf("PlayPause = %+v, want %+v", m.PlayPause, tc.wantRow)
			}
			if m.CanStop != tc.wantCanStop {
				t.Errorf("CanStop = %v, want %v", m.CanStop, tc.wantCanStop)
			}
		})
	}
}

func TestBuildModelStoppedWithURICanReplay(t *testing.T) {
	m := BuildModel(state.Snapshot{
		TransportState: "STOPPED",
		TransportURI:   "http://example.com/video.mp4",
	}, Settings{}, "test")
	if !m.PlayPause.Enabled || m.PlayPause.Title != "Play" {
		t.Errorf("PlayPause = %+v, want enabled Play", m.PlayPause)
	}
}

func TestBuildModelStatusRows(t *testing.T) {
	m := BuildModel(state.Snapshot{
		TransportState: "PLAYING",
		Title:          "大跳水",
		Volume:         42,
		Mute:           true,
		SessionOwner:   "192.168.1.23",
		TransportURI:   "http://example.com/v.mp4",
	}, Settings{}, "1.2.3")

	if m.Device != DeviceName {
		t.Errorf("Device = %q, want %q", m.Device, DeviceName)
	}
	if m.Title != "大跳水" {
		t.Errorf("Title = %q", m.Title)
	}
	if m.VolumePct != 42 {
		t.Errorf("VolumePct = %d", m.VolumePct)
	}
	if !m.Muted {
		t.Error("Muted = false, want true")
	}
	if m.Controller != "192.168.1.23" {
		t.Errorf("Controller = %q", m.Controller)
	}
	if !strings.Contains(m.About, "1.2.3") || !strings.Contains(m.About, DeviceName) {
		t.Errorf("About = %q, want device and version", m.About)
	}

	empty := BuildModel(state.Snapshot{TransportState: "STOPPED"}, Settings{}, "dev")
	if empty.Title != "" || empty.Controller != "" {
		t.Errorf("optional rows not empty: %+v", empty)
	}
}

func TestTruncateTitle(t *testing.T) {
	short := "a short title"
	if got := TruncateTitle(short); got != short {
		t.Errorf("TruncateTitle short = %q", got)
	}
	if got := TruncateTitle("   "); got != "" {
		t.Errorf("TruncateTitle blank = %q, want empty", got)
	}

	long := strings.Repeat("标", maxTitleRunes+10)
	got := TruncateTitle(long)
	if n := len([]rune(got)); n != maxTitleRunes {
		t.Errorf("truncated length = %d, want %d", n, maxTitleRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated title missing ellipsis: %q", got)
	}

	// Multi-byte characters must survive truncation intact.
	if !utf8.ValidString(got) {
		t.Errorf("truncated title is not valid UTF-8: %q", got)
	}
}

func TestSettingsFromConfigAndApplyTo(t *testing.T) {
	cfg := config.Config{
		IINAFullscreen:         true,
		LinkSystemOutputVolume: true,
		AllowSessionPreempt:    false,
	}
	s := settingsFromConfig(cfg, true)
	if !s.IINAFullscreen || !s.LinkSystemVolume || s.AllowPreempt || !s.Debug {
		t.Fatalf("settingsFromConfig = %+v", s)
	}

	base := config.Config{HTTPPort: 8200}
	applied := Settings{IINAFullscreen: false, LinkSystemVolume: true, AllowPreempt: true}.applyTo(base)
	if applied.HTTPPort != 8200 {
		t.Errorf("applyTo changed HTTPPort: %+v", applied)
	}
	if applied.IINAFullscreen || !applied.LinkSystemOutputVolume || !applied.AllowSessionPreempt {
		t.Errorf("applyTo = %+v", applied)
	}
}

func TestBuildModelToggles(t *testing.T) {
	allOn := Settings{IINAFullscreen: true, LinkSystemVolume: true, AllowPreempt: true, Debug: true}
	m := BuildModel(state.Snapshot{}, allOn, "dev")
	if len(m.Toggles) != 4 {
		t.Fatalf("toggles = %d, want 4", len(m.Toggles))
	}
	wantIDs := []Toggle{ToggleFullscreen, ToggleLinkVolume, ToggleAllowPreempt, ToggleDebug}
	wantRestart := map[Toggle]bool{ToggleLinkVolume: true, ToggleAllowPreempt: true}
	for i, row := range m.Toggles {
		if row.ID != wantIDs[i] {
			t.Errorf("toggle[%d].ID = %s, want %s", i, row.ID, wantIDs[i])
		}
		if !row.Checked {
			t.Errorf("toggle %s not checked", row.ID)
		}
		if row.Restart != wantRestart[row.ID] {
			t.Errorf("toggle %s Restart = %v, want %v", row.ID, row.Restart, wantRestart[row.ID])
		}
	}

	allOff := BuildModel(state.Snapshot{}, Settings{}, "dev")
	for i, row := range allOff.Toggles {
		if row.Checked {
			t.Errorf("allOff toggle[%d] checked", i)
		}
		// Titles stay stable so renderers can diff by position.
		if row.Title != m.Toggles[i].Title {
			t.Errorf("toggle[%d] title changed: %q vs %q", i, row.Title, m.Toggles[i].Title)
		}
	}
}
