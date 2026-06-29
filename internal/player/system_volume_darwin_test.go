//go:build darwin

package player

import (
	"errors"
	"testing"
)

// swapOSA replaces the package-level runOSA for the duration of the test and
// restores the original (real osascript) runner on cleanup. Tests MUST call
// this so the real osascript is never executed.
func swapOSA(t *testing.T, fn func(string) error) {
	t.Helper()
	orig := runOSA
	runOSA = fn
	t.Cleanup(func() { runOSA = orig })
}

// recorder returns a runner that captures the last script and returns err.
func recorder(err error) (func(string) error, *string) {
	var got string
	return func(script string) error {
		got = script
		return err
	}, &got
}

func TestSetSystemOutputVolume_CommandConstruction(t *testing.T) {
	cases := []struct {
		name string
		v    int
		want string
	}{
		{"zero", 0, "set volume output volume 0"},
		{"mid", 50, "set volume output volume 50"},
		{"max", 100, "set volume output volume 100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, got := recorder(nil)
			swapOSA(t, run)

			if err := SetSystemOutputVolume(tc.v); err != nil {
				t.Fatalf("SetSystemOutputVolume(%d) returned unexpected error: %v", tc.v, err)
			}
			if *got != tc.want {
				t.Fatalf("SetSystemOutputVolume(%d): got script %q, want %q", tc.v, *got, tc.want)
			}
		})
	}
}

func TestSetSystemMute_CommandConstruction(t *testing.T) {
	t.Run("mute", func(t *testing.T) {
		run, got := recorder(nil)
		swapOSA(t, run)

		if err := SetSystemMute(true); err != nil {
			t.Fatalf("SetSystemMute(true) returned unexpected error: %v", err)
		}
		const want = "set volume with output muted"
		if *got != want {
			t.Fatalf("SetSystemMute(true): got script %q, want %q", *got, want)
		}
	})

	t.Run("unmute", func(t *testing.T) {
		run, got := recorder(nil)
		swapOSA(t, run)

		if err := SetSystemMute(false); err != nil {
			t.Fatalf("SetSystemMute(false) returned unexpected error: %v", err)
		}
		const want = "set volume without output muted"
		if *got != want {
			t.Fatalf("SetSystemMute(false): got script %q, want %q", *got, want)
		}
	})
}

func TestSetSystemOutputVolume_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("osascript boom")
	run, _ := recorder(sentinel)
	swapOSA(t, run)

	if err := SetSystemOutputVolume(50); !errors.Is(err, sentinel) {
		t.Fatalf("SetSystemOutputVolume error = %v, want %v", err, sentinel)
	}
}

func TestSetSystemMute_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("osascript boom")

	t.Run("mute", func(t *testing.T) {
		run, _ := recorder(sentinel)
		swapOSA(t, run)

		if err := SetSystemMute(true); !errors.Is(err, sentinel) {
			t.Fatalf("SetSystemMute(true) error = %v, want %v", err, sentinel)
		}
	})

	t.Run("unmute", func(t *testing.T) {
		run, _ := recorder(sentinel)
		swapOSA(t, run)

		if err := SetSystemMute(false); !errors.Is(err, sentinel) {
			t.Fatalf("SetSystemMute(false) error = %v, want %v", err, sentinel)
		}
	})
}
