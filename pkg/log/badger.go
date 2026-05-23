package log

import (
	"context"
	"fmt"
	"log/slog"
)

// BadgerSlogAdapter satisfies BadgerDB's Logger interface (Errorf, Warningf,
// Infof, Debugf) by forwarding to a *slog.Logger. Each call formats the
// message via fmt.Sprintf since slog's structured API is incompatible with
// BadgerDB's Printf-style contract.
type BadgerSlogAdapter struct {
	log *slog.Logger
}

func NewBadgerSlogAdapter(log *slog.Logger) *BadgerSlogAdapter {
	return &BadgerSlogAdapter{log: log}
}

func (a *BadgerSlogAdapter) Errorf(f string, v ...interface{}) {
	a.log.Log(context.Background(), slog.LevelError, fmt.Sprintf(f, v...))
}

func (a *BadgerSlogAdapter) Warningf(f string, v ...interface{}) {
	a.log.Log(context.Background(), slog.LevelWarn, fmt.Sprintf(f, v...))
}

func (a *BadgerSlogAdapter) Infof(f string, v ...interface{}) {
	a.log.Log(context.Background(), slog.LevelInfo, fmt.Sprintf(f, v...))
}

func (a *BadgerSlogAdapter) Debugf(f string, v ...interface{}) {
	a.log.Log(context.Background(), slog.LevelDebug, fmt.Sprintf(f, v...))
}
