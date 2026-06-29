//go:build darwin && integration

package player

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestIINAPlayer_Integration is a real end-to-end smoke test against an
// installed IINA. It is excluded from the default `go test ./...` run via the
// `darwin && integration` build tag; run it explicitly with:
//
//	go test -tags=integration -run TestIINAPlayer_Integration ./internal/player
//
// Set RCAST_TEST_MEDIA to a path the test should play; if empty or the file
// does not exist, the test is skipped.
func TestIINAPlayer_Integration(t *testing.T) {
	media := os.Getenv("RCAST_TEST_MEDIA")
	if media == "" {
		t.Skip("RCAST_TEST_MEDIA not set; skipping real-IINA integration test")
	}
	if _, err := os.Stat(media); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("RCAST_TEST_MEDIA=%s does not exist; skipping", media)
		}
		t.Fatalf("stat %s: %v", media, err)
	}

	p := NewIINAPlayer(false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Logf("Playing %s", media)
	if err := p.Play(ctx, media, 100); err != nil {
		t.Fatalf("Play failed: %v", err)
	}
	defer func() {
		t.Log("Stopping...")
		_ = p.Stop(context.Background())
	}()

	// Bounded poll helpers: each step must succeed within ~3s or the step is
	// reported but the test moves on so the deferred Stop always runs.
	waitFor := func(name string, fn func() error, deadline time.Time) {
		t.Helper()
		for {
			err := fn()
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("%s did not succeed before deadline: %v", name, err)
				return
			}
			select {
			case <-ctx.Done():
				t.Errorf("ctx done while waiting for %s: %v", name, ctx.Err())
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}

	t.Log("Pausing...")
	deadline := time.Now().Add(3 * time.Second)
	waitFor("Pause", func() error { return p.Pause(ctx) }, deadline)

	t.Log("Resuming...")
	deadline = time.Now().Add(3 * time.Second)
	waitFor("Resume", func() error { return p.Resume(ctx) }, deadline)

	t.Log("Setting volume to 50...")
	deadline = time.Now().Add(3 * time.Second)
	waitFor("SetVolume", func() error { return p.SetVolume(ctx, 50) }, deadline)

	t.Log("Seeking to 30s...")
	deadline = time.Now().Add(3 * time.Second)
	waitFor("Seek", func() error { return p.Seek(ctx, 30) }, deadline)

	// Confirm the position query is responsive without blocking the test long.
	posDeadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := p.GetPosition(ctx); err == nil {
			break
		}
		if time.Now().After(posDeadline) {
			t.Logf("GetPosition never succeeded within deadline (non-fatal)")
			break
		}
		select {
		case <-ctx.Done():
			t.Logf("ctx done while polling GetPosition (non-fatal): %v", ctx.Err())
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
