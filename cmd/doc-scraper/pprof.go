//go:build pprof

package main

import (
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/ handlers on http.DefaultServeMux
	"runtime"

	"log/slog"
)

// startPprof starts the pprof HTTP server if addr is non-empty. The pprof
// handlers register themselves on http.DefaultServeMux via the blank import
// above. Only compiled into the binary when built with `-tags pprof`.
func startPprof(addr string, log *slog.Logger) {
	if addr == "" {
		return
	}
	runtime.SetBlockProfileRate(1000)
	runtime.SetMutexProfileFraction(1000)
	go func() {
		log.Info("Starting pprof server", "addr", addr, "url", "http://"+addr+"/debug/pprof/")
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Error("pprof server error", "addr", addr, "err", err)
		}
	}()
}
