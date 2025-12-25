package player

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"github.com/google/uuid"
	"github.com/tr1v3r/pkg/log"
)

// https://mpv.io/manual/stable/#properties

const sockPathPrefix = "/tmp/rcast_iina-ipc-sock_"

func NewIINAPlayer() *IINAPlayer { return new(IINAPlayer) }

type IINAPlayer struct {
	mu       sync.Mutex
	conn     net.Conn
	reader   *bufio.Reader
	sockPath string

	requestIDCount int

	process *os.Process
}

func (p *IINAPlayer) Close(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var closeErr error
	if p.conn != nil {
		if err := p.conn.Close(); err != nil {
			closeErr = fmt.Errorf("closing iina ipc socket fail: %w", err)
		}
		p.conn = nil
		p.reader = nil
	}

	// Remove socket file if it exists
	if p.sockPath != "" {
		if err := os.Remove(p.sockPath); err != nil && !os.IsNotExist(err) {
			if closeErr != nil {
				return fmt.Errorf("multiple errors: %w, socket removal: %v", closeErr, err)
			}
			return fmt.Errorf("removing socket file: %w", err)
		}
		p.sockPath = ""
	}

	return closeErr
}

func (p *IINAPlayer) Play(ctx context.Context, uri string, volume int) error {
	log.CtxDebug(ctx, "IINAPlayer Play: uri=%s volume=%d", uri, volume)
	if app, cli, err := p.findIINA(); err != nil {
		return fmt.Errorf("IINA not found: %w", err)
	} else if cli != "" {
		p.sockPath = sockPathPrefix + uuid.NewString()
		// open -a IINA --args --mpv-input-ipc-server=/tmp/iina-ipc.sock --keep-running
		cmd := exec.CommandContext(ctx, cli,
			"--keep-running",
			"--mpv-input-ipc-server="+p.sockPath,
			"--mpv-volume="+strconv.Itoa(volume),
			"--mpv-keep-open=yes",
			// --mpv-title=, not work
			// --mpv-fs,
			// --mpv-start={start},
			uri)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start iina-cli: %w", err)
		}
		p.conn = nil
		p.process = cmd.Process
	} else if app != "" {
		if _, err := os.Stat(app); err != nil {
			return fmt.Errorf("IINA not found at %s: %w", app, err)
		}
		// open -a IINA --args --mpv-input-ipc-server=/tmp/iina-ipc.sock --keep-running
		cmd := exec.CommandContext(ctx, "open", "-a", app, uri)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start IINA: %w", err)
		}
	}
	return nil
}

func (p *IINAPlayer) connect(sockPath string) (net.Conn, error) {
	if sockPath == "" {
		return nil, fmt.Errorf("iina ipc socket path is empty")
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("connect to iina ipc socket fail: %w", err)
	}
	return conn, nil
}

func (p *IINAPlayer) Pause(ctx context.Context) error {
	p.requestIDCount++

	data, _ := json.Marshal(MPVJSONIPCRequest{
		RequestID: p.requestIDCount,
		Command:   []any{"set_property", "pause", true},
	})
	if err := p.writeSock(data); err != nil {
		return fmt.Errorf("calling iina pause failed: %w", err)
	}
	return nil
}

func (p *IINAPlayer) Resume(ctx context.Context) error {
	p.requestIDCount++

	data, _ := json.Marshal(MPVJSONIPCRequest{
		RequestID: p.requestIDCount,
		Command:   []any{"set_property", "pause", false},
	})
	if err := p.writeSock(data); err != nil {
		return fmt.Errorf("calling iina resume failed: %w", err)
	}
	return nil
}

func (p *IINAPlayer) Stop(ctx context.Context) error {
	var stopErr error

	// First close the connection and clean up resources
	if err := p.Close(ctx); err != nil {
		stopErr = fmt.Errorf("closing player resources: %w", err)
	}

	// Then kill the process if it exists
	if p.process != nil {
		if err := p.process.Kill(); err != nil {
			if stopErr != nil {
				return fmt.Errorf("multiple errors: %w, killing process: %v", stopErr, err)
			}
			return fmt.Errorf("killing process: %w", err)
		}
		p.process = nil
	}

	return stopErr
}

func (p *IINAPlayer) SetVolume(ctx context.Context, v int) error {
	p.requestIDCount++

	data, _ := json.Marshal(MPVJSONIPCRequest{
		RequestID: p.requestIDCount,
		Command:   []any{"set_property", "volume", v},
	})
	if err := p.writeSock(data); err != nil {
		return fmt.Errorf("calling iina set volume failed: %w", err)
	}
	return nil
}

func (p *IINAPlayer) SetMute(ctx context.Context, m bool) error {
	p.requestIDCount++

	data, _ := json.Marshal(MPVJSONIPCRequest{
		RequestID: p.requestIDCount,
		Command:   []any{"set_property", "mute", m},
	})
	if err := p.writeSock(data); err != nil {
		return fmt.Errorf("calling iina set volume failed: %w", err)
	}
	return nil
}

func (p *IINAPlayer) SetFullscreen(ctx context.Context, f bool) error {
	p.requestIDCount++

	data, _ := json.Marshal(MPVJSONIPCRequest{
		RequestID: p.requestIDCount,
		Command:   []any{"set_property", "fullscreen", f},
	})
	if err := p.writeSock(data); err != nil {
		return fmt.Errorf("calling iina set fullscreen failed: %w", err)
	}
	return nil
}

func (p *IINAPlayer) SetTitle(ctx context.Context, title string) error {
	p.requestIDCount++

	data, _ := json.Marshal(MPVJSONIPCRequest{
		RequestID: p.requestIDCount,
		Command:   []any{"set_property", "title", title},
	})
	if err := p.writeSock(data); err != nil {
		return fmt.Errorf("calling iina set fullscreen failed: %w", err)
	}
	return nil
}

func (p *IINAPlayer) Screenshot(ctx context.Context, path string) error {
	p.requestIDCount++

	data, _ := json.Marshal(MPVJSONIPCRequest{
		RequestID: p.requestIDCount,
		Command:   []any{"screenshot"},
	})
	if err := p.writeSock(data); err != nil {
		return fmt.Errorf("calling iina screenshot failed: %w", err)
	}
	return nil
}

func (p *IINAPlayer) SetSpeed(ctx context.Context, speed float64) error {
	p.requestIDCount++

	data, _ := json.Marshal(MPVJSONIPCRequest{
		RequestID: p.requestIDCount,
		Command:   []any{"set_property", "speed", speed},
	})
	if err := p.writeSock(data); err != nil {
		return fmt.Errorf("calling iina set speed failed: %w", err)
	}
	return nil
}

func (p *IINAPlayer) Seek(ctx context.Context, seconds float64) error {
	p.requestIDCount++

	data, _ := json.Marshal(MPVJSONIPCRequest{
		RequestID: p.requestIDCount,
		Command:   []any{"seek", seconds, "absolute"},
	})
	if err := p.writeSock(data); err != nil {
		return fmt.Errorf("calling iina seek failed: %w", err)
	}
	return nil
}

func (p *IINAPlayer) GetPosition(ctx context.Context) (float64, error) {
	val, err := p.getProperty("time-pos")
	if err != nil {
		return 0, err
	}
	if v, ok := val.(float64); ok {
		return v, nil
	}
	return 0, fmt.Errorf("unexpected type for time-pos: %T", val)
}

func (p *IINAPlayer) GetDuration(ctx context.Context) (float64, error) {
	val, err := p.getProperty("duration")
	if err != nil {
		return 0, err
	}
	if v, ok := val.(float64); ok {
		return v, nil
	}
	return 0, fmt.Errorf("unexpected type for duration: %T", val)
}

func (p *IINAPlayer) getProperty(name string) (any, error) {
	p.requestIDCount++
	data, _ := json.Marshal(MPVJSONIPCRequest{
		RequestID: p.requestIDCount,
		Command:   []any{"get_property", name},
	})

	return p.writeSockAndRead(data)
}

func (p *IINAPlayer) writeSock(data []byte) error {
	_, err := p.writeSockAndRead(data)
	return err
}

func (p *IINAPlayer) writeSockAndRead(data []byte) (any, error) {
	if p.sockPath == "" {
		return nil, fmt.Errorf("iina ipc socket path is empty")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	data = append(data, '\n')

	var lastErr error
	// Try up to 2 times (1 initial + 1 retry)
	for range 2 {
		if p.conn == nil {
			if conn, err := p.connect(p.sockPath); err != nil {
				lastErr = err
				continue
			} else {
				p.conn = conn
				p.reader = bufio.NewReader(conn)
			}
		}

		if _, err := p.conn.Write(data); err != nil {
			lastErr = fmt.Errorf("writing to iina ipc socket fail: %w", err)
			_ = p.conn.Close()
			p.conn = nil
			p.reader = nil
			continue
		}

		// 读取一行响应（如 {"error":"success", ...}）
		respBytes, err := p.reader.ReadBytes('\n')
		if err != nil {
			lastErr = fmt.Errorf("reading from iina ipc socket fail: %w", err)
			_ = p.conn.Close()
			p.conn = nil
			p.reader = nil
			continue
		}

		var resp MPVJSONIPCResponse
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal iina ipc response fail: %w", err)
		}
		if resp.Error != "success" {
			return nil, fmt.Errorf("iina ipc response error: %s %s", resp.Error, string(respBytes))
		}
		return resp.Data, nil
	}

	return nil, lastErr
}

func (*IINAPlayer) findIINA() (string, string, error) {
	if _, err := os.Stat("/opt/homebrew/bin/iina-cli"); err == nil {
		return "/Applications/IINA.app/Contents/MacOS/iina", "/opt/homebrew/bin/iina-cli", nil
	}
	if _, err := os.Stat("/usr/local/bin/iina-cli"); err == nil {
		return "/Applications/IINA.app/Contents/MacOS/iina", "/usr/local/bin/iina-cli", nil
	}

	// TODO check if IINA is installed in Applications folder

	return "/Applications/IINA.app", "/Applications/IINA.app/Contents/MacOS/iina", nil
}