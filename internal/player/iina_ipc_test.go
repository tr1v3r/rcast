package player

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeMPVServer speaks just enough of the mpv JSON IPC protocol for unit tests,
// so the IPC layer can be exercised without a real IINA install.
type fakeMPVServer struct {
	listener *net.UnixListener
	sockPath string

	mu    sync.Mutex
	noop  bool // when true, swallow commands and never reply (forces timeout)
	props map[string]any
}

func newFakeMPVServer(t *testing.T) *fakeMPVServer {
	t.Helper()
	// Keep the path short: macOS sun_path is ~104 bytes, and t.TempDir() is
	// already close to that. Reuse the production prefix under /tmp.
	sockPath := sockPathPrefix + "test-" + uuid.NewString()
	_ = os.Remove(sockPath) // defensive: clear any stale socket
	addr, err := net.ResolveUnixAddr("unix", sockPath)
	if err != nil {
		t.Fatalf("resolve unix addr: %v", err)
	}
	l, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	s := &fakeMPVServer{listener: l, sockPath: sockPath, props: map[string]any{}}
	go s.serve()
	return s
}

func (s *fakeMPVServer) serve() {
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeMPVServer) handle(conn *net.UnixConn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var req MPVJSONIPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		s.mu.Lock()
		noop := s.noop
		s.mu.Unlock()
		if noop {
			continue // never reply → client must time out
		}

		// Emit a spurious event before the reply to exercise the client's
		// event-skipping logic in its response-matching loop.
		if _, err := conn.Write([]byte(`{"event":"playback-restart"}` + "\n")); err != nil {
			return
		}

		resp := MPVJSONIPCResponse{RequestID: req.RequestID, Error: "success"}
		if len(req.Command) > 0 {
			switch req.Command[0] {
			case "get_property":
				if len(req.Command) >= 2 {
					if name, ok := req.Command[1].(string); ok {
						s.mu.Lock()
						value, exists := s.props[name]
						s.mu.Unlock()
						if exists {
							resp.Data = value
						} else {
							resp.Error = "property unavailable"
						}
					}
				}
			case "loadfile":
				if len(req.Command) >= 2 {
					if uri, ok := req.Command[1].(string); ok {
						s.mu.Lock()
						s.props["path"] = uri
						s.mu.Unlock()
					}
				}
			case "stop":
				s.mu.Lock()
				delete(s.props, "path")
				s.mu.Unlock()
			}
		}
		out, _ := json.Marshal(resp)
		if _, err := conn.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

func (s *fakeMPVServer) setProp(name string, val any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.props[name] = val
}

func (s *fakeMPVServer) setNoop(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noop = v
}

func (s *fakeMPVServer) close() { _ = s.listener.Close() }

// playerOnSocket returns an IINAPlayer wired straight to the fake server's
// socket, bypassing the real IINA launch entirely.
func playerOnSocket(t *testing.T, s *fakeMPVServer) *IINAPlayer {
	t.Helper()
	p := NewIINAPlayer(false)
	p.sockPath = s.sockPath
	return p
}

func TestIINAPlayer_CommandAndGetProperty(t *testing.T) {
	s := newFakeMPVServer(t)
	defer s.close()
	p := playerOnSocket(t, s)
	ctx := context.Background()

	if err := p.SetVolume(ctx, 80); err != nil {
		t.Fatalf("SetVolume: %v", err)
	}
	if err := p.Pause(ctx); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	s.setProp("time-pos", 12.5)
	pos, err := p.GetPosition(ctx)
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	if pos != 12.5 {
		t.Fatalf("GetPosition = %v, want 12.5", pos)
	}
}

func TestIINAPlayer_StopPlaybackKeepsIPCReusable(t *testing.T) {
	s := newFakeMPVServer(t)
	defer s.close()
	p := playerOnSocket(t, s)
	ctx := context.Background()

	if err := p.StopPlayback(ctx); err != nil {
		t.Fatalf("StopPlayback: %v", err)
	}
	if err := p.SetVolume(ctx, 40); err != nil {
		t.Fatalf("IPC unusable after StopPlayback: %v", err)
	}
}

func TestIINAPlayer_PlayLoadsNewURIAfterStopPlayback(t *testing.T) {
	s := newFakeMPVServer(t)
	defer s.close()
	s.setProp("path", "https://example.test/old.mp4")
	p := playerOnSocket(t, s)
	p.activate = func(context.Context) error { return nil }
	ctx := context.Background()

	if err := p.StopPlayback(ctx); err != nil {
		t.Fatalf("StopPlayback: %v", err)
	}
	const nextURI = "https://example.test/new.mp4"
	if err := p.Play(ctx, nextURI, 50); err != nil {
		t.Fatalf("Play new URI: %v", err)
	}
	s.mu.Lock()
	got := s.props["path"]
	s.mu.Unlock()
	if got != nextURI {
		t.Fatalf("loaded path=%v, want %s", got, nextURI)
	}
}

func TestIINAPlayer_PlayReuseActivatesWindow(t *testing.T) {
	s := newFakeMPVServer(t)
	defer s.close()
	const uri = "https://example.test/video.mp4"
	s.setProp("path", uri)
	p := playerOnSocket(t, s)
	activated := 0
	p.activate = func(context.Context) error {
		activated++
		return nil
	}

	if err := p.Play(context.Background(), uri, 50); err != nil {
		t.Fatalf("Play: %v", err)
	}
	if activated != 1 {
		t.Fatalf("activation count=%d, want 1", activated)
	}
}

// TestIINAPlayer_ConcurrentSend races many commands on a shared player. Under
// the old code this tripped the -race detector (requestIDCount was mutated
// outside the lock); the locked allocation in send() must keep it clean.
func TestIINAPlayer_ConcurrentSend(t *testing.T) {
	s := newFakeMPVServer(t)
	defer s.close()
	p := playerOnSocket(t, s)
	ctx := context.Background()

	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			switch i % 4 {
			case 0:
				errs[i] = p.SetVolume(ctx, i)
			case 1:
				errs[i] = p.Pause(ctx)
			case 2:
				errs[i] = p.Resume(ctx)
			case 3:
				errs[i] = p.SetMute(ctx, true)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
}

func TestIINAPlayer_SendTimeout(t *testing.T) {
	orig := ipcTimeout
	ipcTimeout = 100 * time.Millisecond
	t.Cleanup(func() { ipcTimeout = orig })

	s := newFakeMPVServer(t)
	defer s.close()
	s.setNoop(true) // server swallows the command and never replies
	p := playerOnSocket(t, s)

	start := time.Now()
	err := p.SetVolume(context.Background(), 50)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	// Two attempts at 100ms each → must return well inside the old "forever".
	if elapsed > 2*time.Second {
		t.Fatalf("send hung for %v (expected to time out quickly)", elapsed)
	}
}

func TestIINAPlayer_SendWithoutSocket(t *testing.T) {
	p := NewIINAPlayer(false)
	if err := p.SetVolume(context.Background(), 50); err == nil {
		t.Fatal("expected error when no socket path is set")
	}
}

func TestIINAPlayer_StopIdempotent(t *testing.T) {
	p := NewIINAPlayer(false)
	// Stop on a fresh player (no conn, no process) must not panic or deadlock.
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on fresh player: %v", err)
	}
}

func TestIINALaunchCommandForAppForcesNewInstance(t *testing.T) {
	cmd := iinaLaunchCommand(context.Background(), iinaAppBinary, []string{"--keep-running", "video.mp4"})
	want := []string{"/usr/bin/open", "-n", "-a", "IINA", "--args", "--keep-running", "video.mp4"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("args=%q, want %q", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Fatalf("arg[%d]=%q, want %q", i, cmd.Args[i], want[i])
		}
	}

	const cli = "/opt/homebrew/bin/iina-cli"
	cliCmd := iinaLaunchCommand(context.Background(), cli, []string{"video.mp4"})
	if cliCmd.Path != cli || len(cliCmd.Args) != 2 || cliCmd.Args[1] != "video.mp4" {
		t.Fatalf("CLI command=%q path=%q", cliCmd.Args, cliCmd.Path)
	}
}
