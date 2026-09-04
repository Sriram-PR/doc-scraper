// Package version holds the single release version shared by the CLI and the
// MCP server. It is a var, not a const, so release builds can override it via
// -ldflags "-X .../pkg/version.Version=...". The default must still be bumped
// each release for go-install builds, which get no ldflags.
package version

var Version = "2.8.2"
