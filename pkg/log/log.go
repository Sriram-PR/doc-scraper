package log

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Format selects the slog handler used by New.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// New returns a *slog.Logger with the given level and handler format. format
// must be FormatText or FormatJSON; anything else falls back to FormatText.
func New(level slog.Level, format string, out io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch format {
	case FormatJSON:
		h = slog.NewJSONHandler(out, opts)
	default:
		h = slog.NewTextHandler(out, opts)
	}
	return slog.New(h)
}

// ParseLevel maps a logrus-style level string ("debug", "info", "warn",
// "warning", "error") to a slog.Level. Unknown levels return an error; the
// CLI subcommands warn and fall back to slog.LevelInfo (matching the
// existing logrus parse-then-fallback contract for crawl and watch).
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q", s)
	}
}
