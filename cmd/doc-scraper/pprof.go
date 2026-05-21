//go:build pprof

package main

import (
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/ handlers on http.DefaultServeMux

	"github.com/sirupsen/logrus"
)

// startPprof starts the pprof HTTP server if addr is non-empty. The pprof
// handlers register themselves on http.DefaultServeMux via the blank import
// above. Only compiled into the binary when built with `-tags pprof`.
func startPprof(addr string, log *logrus.Logger) {
	if addr == "" {
		return
	}
	go func() {
		log.Infof("Starting pprof server at http://%s/debug/pprof/", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Errorf("pprof server error: %v", err)
		}
	}()
}
