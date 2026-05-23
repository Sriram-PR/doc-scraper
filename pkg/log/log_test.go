package log

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
		err  bool
	}{
		{"debug", slog.LevelDebug, false},
		{"DEBUG", slog.LevelDebug, false},
		{"info", slog.LevelInfo, false},
		{"", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{" Error ", slog.LevelError, false},
		{"fatal", slog.LevelInfo, true},
		{"nonsense", slog.LevelInfo, true},
	}
	for _, c := range cases {
		got, err := ParseLevel(c.in)
		assert.Equal(t, c.want, got, "input=%q", c.in)
		if c.err {
			assert.Error(t, err, "input=%q", c.in)
		} else {
			assert.NoError(t, err, "input=%q", c.in)
		}
	}
}

func TestNew_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(slog.LevelInfo, FormatText, &buf)
	l.Info("hello", "n", 3)
	out := buf.String()
	assert.Contains(t, out, "msg=hello")
	assert.Contains(t, out, "n=3")
}

func TestNew_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(slog.LevelInfo, FormatJSON, &buf)
	l.Info("hello", "n", 3)
	out := buf.String()
	assert.Contains(t, out, `"msg":"hello"`)
	assert.Contains(t, out, `"n":3`)
}

func TestNew_RespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(slog.LevelWarn, FormatText, &buf)
	l.Debug("debug-suppressed")
	l.Info("info-suppressed")
	l.Warn("warn-emitted")
	out := buf.String()
	assert.NotContains(t, out, "debug-suppressed")
	assert.NotContains(t, out, "info-suppressed")
	assert.Contains(t, out, "warn-emitted")
}

func TestBadgerSlogAdapter_LogsAtCorrectLevels(t *testing.T) {
	var buf bytes.Buffer
	l := New(slog.LevelDebug, FormatText, &buf)
	a := NewBadgerSlogAdapter(l)

	a.Errorf("E %d", 1)
	a.Warningf("W %s", "x")
	a.Infof("I")
	a.Debugf("D")

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 4)
	assert.Contains(t, lines[0], "level=ERROR")
	assert.Contains(t, lines[0], "msg=\"E 1\"")
	assert.Contains(t, lines[1], "level=WARN")
	assert.Contains(t, lines[1], "msg=\"W x\"")
	assert.Contains(t, lines[2], "level=INFO")
	assert.Contains(t, lines[2], "msg=I")
	assert.Contains(t, lines[3], "level=DEBUG")
	assert.Contains(t, lines[3], "msg=D")
}
