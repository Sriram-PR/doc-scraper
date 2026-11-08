package mcp

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// progressReporter sends notifications/progress for a tool call whose request
// carried a progressToken. It sends only when progress has grown, since the
// spec requires it to increase with every notification, and at most once per
// interval.
type progressReporter struct {
	ctx      context.Context
	token    mcp.ProgressToken
	interval time.Duration
	last     float64
	sent     time.Time
}

// newProgressReporter returns nil when the client asked for no progress; a nil
// reporter's report does nothing.
func newProgressReporter(ctx context.Context, req mcp.CallToolRequest, interval time.Duration) *progressReporter {
	if req.Params.Meta == nil || req.Params.Meta.ProgressToken == nil {
		return nil
	}
	return &progressReporter{ctx: ctx, token: req.Params.Meta.ProgressToken, interval: interval, last: -1}
}

// report sends progress; total 0 means unknown.
func (p *progressReporter) report(progress, total float64, message string) {
	if p == nil || progress <= p.last || time.Since(p.sent) < p.interval {
		return
	}
	srv := server.ServerFromContext(p.ctx)
	if srv == nil {
		return
	}
	params := map[string]any{"progressToken": p.token, "progress": progress, "message": message}
	if total > 0 {
		params["total"] = total
	}
	// A legacy client that never initialized can't receive notifications;
	// the call itself still succeeds.
	if srv.SendNotificationToClient(p.ctx, string(mcp.MethodNotificationProgress), params) == nil {
		p.last, p.sent = progress, time.Now()
	}
}
