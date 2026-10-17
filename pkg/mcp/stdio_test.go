package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

func newStdioTestServer(t *testing.T) *Server {
	t.Helper()
	appCfg := &config.AppConfig{
		OutputBaseDir:       t.TempDir(),
		StateDir:            t.TempDir(),
		JSONLOutputFilename: "pages.jsonl",
		HTTPClientSettings: config.HTTPClientConfig{
			Timeout:              10 * time.Second,
			AllowPrivateNetworks: true,
		},
		Sites: map[string]*config.SiteConfig{"docs": {AllowedDomain: "docs.example.com"}},
	}
	s, err := NewServer(&ServerConfig{AppConfig: appCfg, Logger: silentTestLogger()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return s
}

func slowPageServer(t *testing.T, delay time.Duration) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><title>Slow</title></head><body><p>SLOW PAGE BODY</p></body></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/page.html"
}

func stdioLines(msgs ...string) io.Reader {
	return strings.NewReader(strings.Join(msgs, "\n") + "\n")
}

const stdioInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`
const stdioInitialized = `{"jsonrpc":"2.0","method":"notifications/initialized"}`

func getPageCall(id int, url string) string {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": "get_page", "arguments": map[string]any{"url": url}},
	})
	return string(b)
}

type stdioResponse struct {
	ID     json.RawMessage `json:"id"`
	Result *struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func responsesByID(t *testing.T, out []byte) map[string]stdioResponse {
	t.Helper()
	got := map[string]stdioResponse{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		var r stdioResponse
		require.NoError(t, json.Unmarshal(sc.Bytes(), &r), "line: %s", sc.Text())
		if len(r.ID) > 0 {
			got[string(r.ID)] = r
		}
	}
	return got
}

// A client that writes its requests and then closes stdin (a pipe, a script)
// must still get every answer: EOF ends the session, not the requests in it.
func TestServe_AnswersRequestsSentBeforeStdinCloses(t *testing.T) {
	s := newStdioTestServer(t)
	url := slowPageServer(t, 300*time.Millisecond)

	var out bytes.Buffer
	err := s.serve(context.Background(), stdioLines(stdioInitialize, stdioInitialized, getPageCall(2, url), getPageCall(3, url)), &out)
	require.NoError(t, err)

	got := responsesByID(t, out.Bytes())
	for _, id := range []string{"2", "3"} {
		r, ok := got[id]
		if !assert.True(t, ok, "no response for request %s; output:\n%s", id, out.String()) {
			continue
		}
		require.NotNil(t, r.Result, "request %s: error %+v", id, r.Error)
		require.NotEmpty(t, r.Result.Content)
		assert.False(t, r.Result.IsError, "request %s: %s", id, r.Result.Content[0].Text)
		assert.Contains(t, r.Result.Content[0].Text, "SLOW PAGE BODY", "request %s", id)
	}
}

func serveReturnsWithin(t *testing.T, s *Server, ctx context.Context, in io.Reader, limit time.Duration) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	errc := make(chan error, 1)
	go func() { errc <- s.serve(ctx, in, &out) }()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			require.NoError(t, err)
		}
	case <-time.After(limit):
		t.Fatalf("serve still running %v after stdin closed", limit)
	}
	return &out
}

// A request the client cancelled is not waited for.
func TestServe_DoesNotWaitForCancelledRequest(t *testing.T) {
	s := newStdioTestServer(t)
	url := slowPageServer(t, 30*time.Second)
	in := stdioLines(stdioInitialize, stdioInitialized, getPageCall(2, url),
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2}}`)
	serveReturnsWithin(t, s, context.Background(), in, 5*time.Second)
}

// A signal (the serve context ending) still stops the server promptly while
// requests are in flight.
func TestServe_ContextCancelEndsDrain(t *testing.T) {
	s := newStdioTestServer(t)
	url := slowPageServer(t, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	serveReturnsWithin(t, s, ctx, stdioLines(stdioInitialize, stdioInitialized, getPageCall(2, url)), 5*time.Second)
}

// subscriptions/listen stays open for the session by design, so it must not
// hold the process up after stdin closes.
func TestServe_DoesNotWaitForSubscriptionStream(t *testing.T) {
	s := newStdioTestServer(t)
	listen := `{"jsonrpc":"2.0","id":7,"method":"subscriptions/listen","params":{` +
		`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},` +
		`"notifications":{"toolsListChanged":true}}}`
	serveReturnsWithin(t, s, context.Background(), stdioLines(listen), 5*time.Second)
}

// A request that is never answered stops blocking EOF after the timeout.
func TestStdioDrain_GivesUpAfterTimeout(t *testing.T) {
	d := newStdioDrain(context.Background(), 50*time.Millisecond, silentTestLogger())
	r := d.reader(stdioLines(getPageCall(2, "http://unused.invalid/")))
	start := time.Now()
	requireReadAllWithin(t, r, 5*time.Second)
	assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond, "EOF should wait for the pending request")
}

// The serve context ending releases a held EOF, so the reader goroutine does
// not outlive shutdown by the full timeout.
func TestStdioDrain_ContextCancelReleasesEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := newStdioDrain(ctx, time.Hour, silentTestLogger())
	r := d.reader(stdioLines(getPageCall(2, "http://unused.invalid/")))
	time.AfterFunc(50*time.Millisecond, cancel)
	requireReadAllWithin(t, r, 5*time.Second)
}

func requireReadAllWithin(t *testing.T, r io.Reader, limit time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(r); done <- err }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(limit):
		t.Fatalf("EOF still held after %v", limit)
	}
}

// An answered request releases EOF immediately.
func TestStdioDrain_ResponseReleasesEOF(t *testing.T) {
	d := newStdioDrain(context.Background(), time.Minute, silentTestLogger())
	r := d.reader(stdioLines(getPageCall(2, "http://unused.invalid/")))
	w := d.writer(io.Discard)
	buf := make([]byte, 4096)
	_, err := r.Read(buf)
	require.NoError(t, err)
	_, err = io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{}}`+"\n")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := r.Read(buf); done <- err }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, io.EOF)
	case <-time.After(5 * time.Second):
		t.Fatal("EOF still held after the response was written")
	}
}
