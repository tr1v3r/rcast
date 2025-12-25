package player

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIINAPlayer_Integration(t *testing.T) {
	// This integration test requires IINA to be installed and a video file.

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("Could not get user home dir")
	}

	testFile := filepath.Join(home, "Downloads/1.webm")
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skipf("Test file %s not found, skipping integration test", testFile)
	}

	p := NewIINAPlayer(false)
	ctx := context.Background()

	t.Logf("Playing %s", testFile)
	if err := p.Play(ctx, testFile, 100); err != nil {
		t.Fatalf("Play failed: %v", err)
	}
	defer func() {
		t.Log("Stopping...")
		_ = p.Stop(ctx)
	}()

	time.Sleep(2 * time.Second)

	t.Log("Pausing...")
	if err := p.Pause(ctx); err != nil {
		t.Errorf("Pause failed: %v", err)
	}

	time.Sleep(1 * time.Second)

	t.Log("Resuming...")
	if err := p.Resume(ctx); err != nil {
		t.Errorf("Resume failed: %v", err)
	}

	time.Sleep(1 * time.Second)

	t.Log("Setting volume to 50...")
	if err := p.SetVolume(ctx, 50); err != nil {
		t.Errorf("SetVolume failed: %v", err)
	}

	time.Sleep(1 * time.Second)

	t.Log("Seeking to 30s...")
	if err := p.Seek(ctx, 30); err != nil {
		t.Errorf("Seek failed: %v", err)
	}

	time.Sleep(5 * time.Second)
}
