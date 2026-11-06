package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

// slowChainServer serves /docs/index.html linking to p1..p8, each linking to
// the next, every page taking delay to answer, so a crawl runs for seconds.
func slowChainServer(t *testing.T, delay time.Duration) *Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/docs/") {
			http.NotFound(w, r)
			return
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		n := 0
		_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/docs/p"), "%d", &n)
		next := ""
		if n < 8 {
			next = fmt.Sprintf(`<a href="/docs/p%d.html">next</a>`, n+1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<html><head><title>P%d</title></head><body><div id="c"><p>page %d</p>%s</div></body></html>`, n, n, next)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	host = host[:strings.LastIndex(host, ":")]
	appCfg := &config.AppConfig{
		NumWorkers:          2,
		MaxRequests:         4,
		MaxRequestsPerHost:  2,
		OutputBaseDir:       t.TempDir(),
		StateDir:            t.TempDir(),
		DefaultUserAgent:    "test-agent",
		MaxRetries:          1,
		JSONLOutputFilename: "pages.jsonl",
		HTTPClientSettings:  config.HTTPClientConfig{Timeout: 10 * time.Second, AllowPrivateNetworks: true},
		Sites: map[string]*config.SiteConfig{"docs": {
			StartURLs:         []string{srv.URL + "/docs/index.html"},
			AllowedDomain:     host,
			AllowedPathPrefix: "/docs",
			ContentSelector:   "#c",
		}},
	}
	s, err := NewServer(&ServerConfig{AppConfig: appCfg, Logger: silentTestLogger()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return s
}

type progressParams struct {
	ProgressToken any     `json:"progressToken"`
	Progress      float64 `json:"progress"`
	Total         float64 `json:"total"`
	Message       string  `json:"message"`
}

func progressNotifications(t *testing.T, out []byte) []progressParams {
	t.Helper()
	var got []progressParams
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		var msg struct {
			Method string         `json:"method"`
			Params progressParams `json:"params"`
		}
		require.NoError(t, json.Unmarshal(sc.Bytes(), &msg))
		if msg.Method == "notifications/progress" {
			got = append(got, msg.Params)
		}
	}
	return got
}

func waitForJobCall(jobID string, meta map[string]any) string {
	params := map[string]any{"name": "get_job_status", "arguments": map[string]any{"job_id": jobID, "wait_seconds": 30}}
	if meta != nil {
		params["_meta"] = meta
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": params})
	return string(b)
}

func startCrawl(t *testing.T, s *Server) string {
	t.Helper()
	_, got := callJSON(t, s.handleCrawlSite, map[string]any{"site_key": "docs"})
	require.NotNil(t, got)
	return got["job_id"].(string)
}

func TestGetJobStatus_SendsProgressWhileWaiting(t *testing.T) {
	s := slowChainServer(t, 300*time.Millisecond)
	jobID := startCrawl(t, s)

	var out bytes.Buffer
	in := stdioLines(stdioInitialize, stdioInitialized, waitForJobCall(jobID, map[string]any{"progressToken": "p1"}))
	require.NoError(t, s.serve(context.Background(), in, &out))

	r, ok := responsesByID(t, out.Bytes())["9"]
	require.True(t, ok, "no get_job_status response; output:\n%s", out.String())
	require.NotNil(t, r.Result)
	assert.Contains(t, r.Result.Content[0].Text, `"status":"completed"`)

	got := progressNotifications(t, out.Bytes())
	require.NotEmpty(t, got, "no progress notifications; output:\n%s", out.String())
	for i, p := range got {
		assert.Equal(t, "p1", p.ProgressToken)
		assert.Contains(t, p.Message, "pages")
		assert.Zero(t, p.Total, "a crawl's total isn't known")
		if i > 0 {
			assert.Greater(t, p.Progress, got[i-1].Progress, "progress must strictly increase")
		}
	}
}

func TestGetJobStatus_NoProgressWithoutToken(t *testing.T) {
	s := slowChainServer(t, 100*time.Millisecond)
	jobID := startCrawl(t, s)

	var out bytes.Buffer
	in := stdioLines(stdioInitialize, stdioInitialized, waitForJobCall(jobID, nil))
	require.NoError(t, s.serve(context.Background(), in, &out))

	_, ok := responsesByID(t, out.Bytes())["9"]
	require.True(t, ok, "no get_job_status response; output:\n%s", out.String())
	assert.Empty(t, progressNotifications(t, out.Bytes()))
}
