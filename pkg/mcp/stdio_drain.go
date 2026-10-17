package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// stdinEOFDrainTimeout bounds how long a closed stdin waits for requests
// already read to be answered, so a stuck handler cannot keep the process up.
const stdinEOFDrainTimeout = 60 * time.Second

// stdioDrain holds back stdin's EOF until every request read from it has been
// answered on stdout. mcp-go's stdio Listen cancels all in-flight requests as
// soon as its input ends, so a client that writes its requests and closes
// stdin would otherwise get "context canceled" for anything still running.
type stdioDrain struct {
	ctx     context.Context
	timeout time.Duration
	log     *slog.Logger

	mu      sync.Mutex
	pending map[string]struct{}
	idle    chan struct{} // closed when pending empties while EOF waits
}

func newStdioDrain(ctx context.Context, timeout time.Duration, log *slog.Logger) *stdioDrain {
	return &stdioDrain{ctx: ctx, timeout: timeout, log: log, pending: make(map[string]struct{})}
}

func (d *stdioDrain) reader(r io.Reader) io.Reader {
	return &drainReader{d: d, r: bufio.NewReader(r)}
}

func (d *stdioDrain) writer(w io.Writer) io.Writer {
	return &drainWriter{d: d, w: w}
}

type rpcEnvelope struct {
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id"`
	Params struct {
		RequestID json.RawMessage `json:"requestId"`
	} `json:"params"`
}

// rpcIDKey returns a comparable form of a JSON-RPC id, or "" for none.
func rpcIDKey(id json.RawMessage) string {
	var buf bytes.Buffer
	if json.Compact(&buf, id) != nil || buf.Len() == 0 || buf.String() == "null" {
		return ""
	}
	return buf.String()
}

func parseEnvelope(line []byte) (rpcEnvelope, bool) {
	var env rpcEnvelope
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &env) != nil {
		return env, false
	}
	return env, true
}

func (d *stdioDrain) observeInput(line []byte) {
	env, ok := parseEnvelope(line)
	if !ok {
		return
	}
	switch {
	case env.Method == "notifications/cancelled":
		d.done(rpcIDKey(env.Params.RequestID))
	// A subscription stream stays open for the whole session by design.
	case env.Method == "subscriptions/listen":
	case env.Method != "":
		if key := rpcIDKey(env.ID); key != "" {
			d.mu.Lock()
			d.pending[key] = struct{}{}
			d.mu.Unlock()
		}
	}
}

func (d *stdioDrain) observeOutput(line []byte) {
	env, ok := parseEnvelope(line)
	if ok && env.Method == "" {
		d.done(rpcIDKey(env.ID))
	}
}

func (d *stdioDrain) done(key string) {
	if key == "" {
		return
	}
	d.mu.Lock()
	delete(d.pending, key)
	if len(d.pending) == 0 && d.idle != nil {
		close(d.idle)
		d.idle = nil
	}
	d.mu.Unlock()
}

func (d *stdioDrain) waitIdle() {
	d.mu.Lock()
	if len(d.pending) == 0 {
		d.mu.Unlock()
		return
	}
	if d.idle == nil {
		d.idle = make(chan struct{})
	}
	idle := d.idle
	d.mu.Unlock()

	timer := time.NewTimer(d.timeout)
	defer timer.Stop()
	select {
	case <-idle:
	case <-d.ctx.Done():
	case <-timer.C:
		d.mu.Lock()
		n := len(d.pending)
		d.mu.Unlock()
		d.log.Warn("stdin closed; stopped waiting for in-flight requests", "pending", n, "waited", d.timeout)
	}
}

type drainReader struct {
	d   *stdioDrain
	r   *bufio.Reader
	buf []byte
	err error
}

func (r *drainReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if r.err != nil {
			if errors.Is(r.err, io.EOF) {
				r.d.waitIdle()
			}
			return 0, r.err
		}
		line, err := r.r.ReadBytes('\n')
		r.d.observeInput(line)
		r.buf, r.err = line, err
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

type drainWriter struct {
	d       *stdioDrain
	w       io.Writer
	mu      sync.Mutex
	partial []byte
}

func (w *drainWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p[:n]...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		w.d.observeOutput(w.partial[:i])
		w.partial = w.partial[i+1:]
	}
	return n, err
}
