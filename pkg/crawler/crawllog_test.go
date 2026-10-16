package crawler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

// crawlLog collects a test crawl's debug log and prints it only if the test fails.
type crawlLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newCrawlLog(t *testing.T) *crawlLog {
	t.Helper()
	l := &crawlLog{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("crawler log:\n%s", l.String())
		}
	})
	return l
}

func (l *crawlLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *crawlLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *crawlLog) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// crawlTimeout leaves room for one request to time out on every attempt, so a
// single stalled connection on a slow runner is retried instead of failing the test.
func crawlTimeout(appCfg *config.AppConfig) time.Duration {
	return time.Duration(appCfg.MaxRetries+1)*appCfg.HTTPClientSettings.Timeout + 20*time.Second
}

// deadlineContext bounds a test crawl by crawlTimeout. When the deadline fires it
// records every goroutine's stack, taken before cancellation unwinds a stuck crawl.
func (l *crawlLog) deadlineContext(t *testing.T, appCfg *config.AppConfig) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), crawlTimeout(appCfg))
	dumped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(dumped)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			_, _ = fmt.Fprintf(l, "crawl deadline (%v) exceeded, goroutines:\n", crawlTimeout(appCfg))
			_ = pprof.Lookup("goroutine").WriteTo(l, 2)
		}
	})
	t.Cleanup(func() {
		if !stop() {
			<-dumped
		}
		cancel()
	})
	return ctx, cancel
}
