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

	if p.conn != nil {
		if err := p.conn.Close(); err != nil {
			return fmt.Errorf("closing iina ipc socket fail: %w", err)
		}
	}
	return os.Remove(p.sockPath)
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
			// '--mpv-start={start}',
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

func (p *IINAPlayer) Stop(ctx context.Context) error {
	_ = p.Close(ctx)
	if p.process == nil {
		return nil
	}
	return p.process.Kill()
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

func (p *IINAPlayer) writeSock(data []byte) error {
	if p.sockPath == "" {
		return fmt.Errorf("iina ipc socket path is empty")
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		if conn, err := p.connect(p.sockPath); err != nil {
			return err
		} else {
			p.conn = conn
			p.reader = bufio.NewReader(conn)
		}
	}

	data = append(data, '\n')

	if _, err := p.conn.Write(data); err != nil {
		return fmt.Errorf("writing to iina ipc socket fail: %w", err)
	}

	// 读取一行响应（如 {"error":"success", ...}）
	respBytes, err := p.reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("reading from iina ipc socket fail: %w", err)
	}

	var resp MPVJSONIPCResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return fmt.Errorf("unmarshal iina ipc response fail: %w", err)
	}
	if resp.Error != "success" {
		return fmt.Errorf("iina ipc response error: %s %s", resp.Error, string(respBytes))
	}
	return nil
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

// func findIINA() (string, string) {
// 	if _, err := os.Stat("/opt/homebrew/bin/iina-cli"); err == nil {
// 		return "/Applications/IINA.app/Contents/MacOS/iina", "/opt/homebrew/bin/iina-cli"
// 	}
// 	if _, err := os.Stat("/usr/local/bin/iina-cli"); err == nil {
// 		return "/Applications/IINA.app/Contents/MacOS/iina", "/usr/local/bin/iina-cli"
// 	}

// 	return "/Applications/IINA.app/Contents/MacOS/iina", ""
// }

// func Play(uri string) error {
// 	app, cli := findIINA()
// 	if cli != "" {
// 		return exec.Command(cli, uri).Start()
// 	}
// 	if _, err := os.Stat(app); err == nil {
// 		return exec.Command(app, "--no-stdin", uri).Start()
// 	}
// 	script := fmt.Sprintf(`tell application "IINA"
//         activate
//         open location "%s"
//     end tell`, escapeAppleScript(uri))
// 	return exec.Command("osascript", "-e", script).Start()
// }

// func Pause() error {
// 	script := `tell application "IINA" to pause`
// 	return exec.Command("osascript", "-e", script).Run()
// }

// func Stop() error {
// 	script := `tell application "IINA" to stop`
// 	return exec.Command("osascript", "-e", script).Run()
// }

// func SetVolume(v int) error {
// 	// 设置 IINA 内部音量
// 	return iinaSetVolume(v)
// }

// func SetMute(m bool) error {
// 	return iinaSetMute(m)
// }

// func iinaSetVolume(v int) error {
// 	script := fmt.Sprintf(`tell application "IINA"
//         if exists current player then
//             set volume of current player to %d
//         else
//             activate
//         end if
//     end tell`, v)
// 	return exec.Command("osascript", "-e", script).Run()
// }

// func iinaSetMute(m bool) error {
// 	val := "false"
// 	if m {
// 		val = "true"
// 	}
// 	script := fmt.Sprintf(`tell application "IINA"
//         if exists current player then
//             set mute of current player to %s
//         else
//             activate
//         end if
//     end tell`, val)
// 	return exec.Command("osascript", "-e", script).Run()
// }

// func escapeAppleScript(s string) string {
// 	r := strings.ReplaceAll(s, `\`, `\\`)
// 	r = strings.ReplaceAll(r, `"`, `\"`)
// 	return r
// }
