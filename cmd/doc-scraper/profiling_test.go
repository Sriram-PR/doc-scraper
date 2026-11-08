package main

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkglog "github.com/Sriram-PR/doc-scraper/v2/pkg/log"
)

// Block and mutex profiling slow every lock and channel operation, so a crawl
// without -pprof must not turn them on.
func TestCrawlWithoutPprofLeavesProfilingOff(t *testing.T) {
	t.Cleanup(func() {
		runtime.SetBlockProfileRate(0)
		runtime.SetMutexProfileFraction(0)
	})
	srv := crawlFixtureServer(t)
	cfgPath, _, _ := writeCrawlConfig(t, srv.URL, "")

	require.Equal(t, 0, executeCrawl(cfgPath, "fixture", "error", pkglog.FormatText, "", false, false, false, false))
	assert.Zero(t, runtime.SetMutexProfileFraction(0), "mutex profiling after crawl")

	require.Equal(t, 0, executeParallelCrawl(cfgPath, []string{"fixture"}, false, "error", pkglog.FormatText, "", false, false, false, false))
	assert.Zero(t, runtime.SetMutexProfileFraction(-1), "mutex profiling after crawl -sites")
}
