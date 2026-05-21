//go:build !pprof

package main

import "github.com/sirupsen/logrus"

// startPprof is a no-op when the binary is built without `-tags pprof`. This
// keeps net/http/pprof and its unauthenticated handlers out of release
// builds. To enable: `go build -tags pprof ./cmd/doc-scraper`.
func startPprof(addr string, log *logrus.Logger) {
	if addr != "" {
		log.Warnf("--pprof %s requested but binary was not built with -tags pprof; ignoring", addr)
	}
}
