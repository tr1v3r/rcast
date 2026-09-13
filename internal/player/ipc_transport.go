package player

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/tr1v3r/pkg/log"
)

// Transport-level sentinels used to classify IPC failures:
//
//   - errIPCTransport wraps dial/write/connection-loss failures: the
//     connection is dead and reuse must restart the instance (audit M4).
//   - errIPCApp wraps command-level failures mpv reported itself (e.g.
//     "property unavailable" after a stop): the connection is healthy.
var (
	errIPCTransport = errors.New("iina ipc connection failure")
	errIPCApp       = errors.New("iina ipc command error")
)

// ipcPollMax caps the exponential backoff between IPC connection attempts.
const ipcPollMax = 250 * time.Millisecond

// isIPCBusy classifies an error as "IINA alive but not answering in time"
// (audit M4): only deadlines count — anything transport-shaped means the
// connection is gone instead, and only that justifies a restart.
func isIPCBusy(err error) bool {
	return err != nil && errors.Is(err, context.DeadlineExceeded)
}

// ipcResult is the outcome of one command, delivered on a buffered channel
// by the connection's reader goroutine.
type ipcResult struct {
	data any
	err  error
}

// pendingWait couples a request's reply channel with the connection
// generation it was written on, so a dying reader goroutine of a replaced
// connection cannot fail replies that belong to the new connection.
type pendingWait struct {
	ch  chan ipcResult
	gen int
}

// send issues one JSON IPC command and waits for the matching reply.
//
// Lock discipline (audits L1/M4/M5): p.mu is held only to allocate the
// request id, marshal, connect and write — never while waiting for the reply.
// A background reader (readLoop) owns the read side of the connection, so:
//
//   - replies are matched by request id and delivered on a buffered channel;
//   - asynchronous mpv events flow to OnEvent handlers instead of being
//     discarded by an in-flight request (audit M2, player half);
//   - a reply timeout leaves the connection installed — IINA may just be
//     busy — instead of tearing it down and reconnecting (audit M4);
//   - event floods cannot extend how long send holds the lock: there is a
//     single bounded write per attempt and no per-line deadline refreshing
//     read loop at all (audit L1).
//
// Each attempt is bounded by ipcTimeout and the context deadline; a failed
// attempt is retried once on a fresh connection, mirroring the previous
// reconnect-retry behavior.
func (p *IINAPlayer) send(ctx context.Context, command []any) (any, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ch, id, err, fatal := p.beginSend(ctx, command)
		if fatal {
			// Audit M5: marshal failures are data bugs (e.g. NaN seek
			// targets). Retrying or writing a bare newline would hang the
			// caller for the full IPC budget.
			return nil, err
		}
		if err != nil {
			lastErr = err
			continue
		}
		timer := time.NewTimer(time.Until(ipcDeadline(ctx)))
		select {
		case res := <-ch:
			timer.Stop()
			if res.err != nil {
				if errors.Is(res.err, errIPCApp) {
					return nil, res.err
				}
				lastErr = res.err // transport-level: retry on a fresh connection
				continue
			}
			return res.data, nil
		case <-ctx.Done():
			timer.Stop()
			p.unregister(id)
			return nil, ctx.Err()
		case <-timer.C:
			p.unregister(id)
			// Deadline only: the connection itself may still be healthy
			// (IINA busy) — do not reset it (audit M4).
			lastErr = fmt.Errorf("waiting for iina ipc reply: %w", context.DeadlineExceeded)
		}
	}
	return nil, lastErr
}

// beginSend performs the locked half of send: allocate a request id, marshal
// (audit M5: marshal errors are fatal), ensure a live connection, register
// the reply channel and write the command. It never blocks on a reply.
func (p *IINAPlayer) beginSend(ctx context.Context, command []any) (<-chan ipcResult, int, error, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sockPath == "" {
		return nil, 0, fmt.Errorf("iina ipc socket path is empty"), false
	}

	p.requestIDCount++
	id := p.requestIDCount

	data, err := json.Marshal(MPVJSONIPCRequest{
		RequestID: id,
		Command:   command,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("marshaling iina ipc command (%v): %w", command, err), true
	}
	data = append(data, '\n')

	if p.conn == nil || p.connDead {
		p.resetConnLocked()
		conn, err := p.connect(p.sockPath)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: %v", errIPCTransport, err), false
		}
		p.installConnLocked(conn)
	}

	ch := make(chan ipcResult, 1)
	if p.pending == nil {
		p.pending = make(map[int]pendingWait)
	}
	p.pending[id] = pendingWait{ch: ch, gen: p.connGen}

	if err := p.conn.SetWriteDeadline(ipcDeadline(ctx)); err != nil {
		p.resetConnLocked()
		return nil, 0, fmt.Errorf("%w: set write deadline: %v", errIPCTransport, err), false
	}
	if _, err := p.conn.Write(data); err != nil {
		p.resetConnLocked()
		return nil, 0, fmt.Errorf("%w: writing to iina ipc socket: %v", errIPCTransport, err), false
	}
	return ch, id, nil, false
}

// installConnLocked makes conn the current IPC connection and starts its
// background reader. Caller must hold p.mu.
func (p *IINAPlayer) installConnLocked(conn net.Conn) {
	p.resetConnLocked()
	p.conn = conn
	p.connDead = false
	p.connGen++
	go p.readLoop(conn, bufio.NewReader(conn), p.connGen)
}

// resetConnLocked drops the current connection (if any) without touching the
// socket path. Closing the old connection unblocks its readLoop, which then
// fails anything still pending on that generation. Caller must hold p.mu.
func (p *IINAPlayer) resetConnLocked() {
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
	p.connDead = false
}

// readLoop owns the read side of one IPC connection for its whole lifetime:
// it dispatches asynchronous mpv events to the event queue and routes command
// replies to their waiting senders. It exits on the first terminal read error
// (EOF, closed connection), failing every request still pending on this
// connection generation. It holds no deadline: a quiet but healthy connection
// simply parks in ReadBytes; senders time out independently.
func (p *IINAPlayer) readLoop(conn net.Conn, reader *bufio.Reader, gen int) {
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			p.failConn(conn, gen, err)
			return
		}
		p.handleMessage(line)
	}
}

// handleMessage routes one decoded IPC message: events go to the event
// queue, replies go to the pending sender with the matching request id.
func (p *IINAPlayer) handleMessage(line []byte) {
	var resp MPVJSONIPCResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		log.Warn("unmarshal iina ipc message fail: %v data=%s", err, string(line))
		return
	}
	if resp.Event != "" {
		p.dispatchEvent(line)
		return
	}
	p.mu.Lock()
	w, ok := p.pending[resp.RequestID]
	delete(p.pending, resp.RequestID)
	p.mu.Unlock()
	if !ok {
		return // late reply for a request whose sender already gave up
	}
	if resp.Error != "success" {
		w.ch <- ipcResult{err: fmt.Errorf("%w: %s %s", errIPCApp, resp.Error, string(line))}
		return
	}
	w.ch <- ipcResult{data: resp.Data}
}

// failConn marks the connection dead (if still current) and fails every
// request that was written on this connection generation.
func (p *IINAPlayer) failConn(conn net.Conn, gen int, cause error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == conn {
		p.connDead = true
	}
	for id, w := range p.pending {
		if w.gen == gen {
			w.ch <- ipcResult{err: fmt.Errorf("%w: %v", errIPCTransport, cause)}
			delete(p.pending, id)
		}
	}
}

// unregister drops the pending entry for a request whose sender gave up
// (deadline reached / ctx done); a late reply is then simply discarded.
func (p *IINAPlayer) unregister(id int) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}
