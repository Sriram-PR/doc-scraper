package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

// HTTPFetcher is the interface for performing HTTP requests with retry logic.
type HTTPFetcher interface {
	FetchWithRetry(req *http.Request, ctx context.Context) (*http.Response, error)
}

// Fetcher makes HTTP requests with exponential-backoff retry logic.
type Fetcher struct {
	client *http.Client
	cfg    *config.AppConfig
	log    *slog.Logger
}

func NewFetcher(client *http.Client, cfg *config.AppConfig, log *slog.Logger) *Fetcher {
	return &Fetcher{
		client: client,
		cfg:    cfg,
		log:    log,
	}
}

// FetchWithRetry performs the request with exponential backoff and jitter for 5xx and 429 responses.
func (f *Fetcher) FetchWithRetry(req *http.Request, ctx context.Context) (*http.Response, error) { //nolint:gocyclo // retry logic with multiple error paths
	var lastErr error
	var currentResp *http.Response

	reqLog := f.log.With("url", req.URL.String())

	maxRetries := f.cfg.MaxRetries
	initialRetryDelay := f.cfg.InitialRetryDelay
	maxRetryDelay := f.cfg.MaxRetryDelay

	for attempt := 0; attempt <= maxRetries; attempt++ {

		select {
		case <-ctx.Done():
			reqLog.Warn(fmt.Sprintf("Context cancelled before attempt %d: %v", attempt, ctx.Err()))
			if lastErr != nil {
				return nil, fmt.Errorf("context cancelled (%v) during retry backoff after error: %w", ctx.Err(), lastErr) //nolint:errorlint // ctx.Err() is diagnostic info, lastErr is the wrapped error
			}
			return nil, fmt.Errorf("context cancelled before first attempt: %w", ctx.Err())
		default:
		}

		if attempt > 0 {
			backoff := float64(initialRetryDelay) * math.Pow(2, float64(attempt-1))
			delay := time.Duration(backoff)
			if delay <= 0 || delay > maxRetryDelay {
				delay = maxRetryDelay
			}

			finalDelay := withJitter(delay)

			reqLog.Warn("Retrying request...", "attempt", attempt, "max_retries", maxRetries, "delay", finalDelay)

			select {
			case <-time.After(finalDelay):
			case <-ctx.Done():
				reqLog.Warn(fmt.Sprintf("Context cancelled during retry sleep: %v", ctx.Err()))
				if lastErr != nil {
					return nil, fmt.Errorf("context cancelled (%v) during retry delay after error: %w", ctx.Err(), lastErr) //nolint:errorlint // ctx.Err() is diagnostic info, lastErr is the wrapped error
				}
				return nil, fmt.Errorf("context cancelled during retry delay: %w", ctx.Err())
			}
		}

		reqWithCtx := req.WithContext(ctx)
		currentResp, lastErr = f.client.Do(reqWithCtx)

		if lastErr != nil {
			if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
				reqLog.Warn(fmt.Sprintf("Context cancelled/timed out during HTTP request execution: %v", lastErr))
				drainAndClose(currentResp)
				return nil, lastErr
			}

			// A blocked address (SSRF guard) can never succeed; fail fast instead
			// of burning the whole retry/backoff schedule on it.
			if errors.Is(lastErr, utils.ErrBlockedAddress) {
				reqLog.Warn(fmt.Sprintf("Address blocked by SSRF guard, not retrying: %v", lastErr))
				drainAndClose(currentResp)
				return nil, lastErr
			}

			reqLog.Error(fmt.Sprintf("Network error: %v", lastErr), "attempt", attempt)
			drainAndClose(currentResp)
			continue
		}

		statusCode := currentResp.StatusCode
		resLog := reqLog.With("status_code", statusCode, "status", currentResp.Status, "attempt", attempt)

		switch {
		case statusCode >= 200 && statusCode < 300:
			resLog.Debug("Successfully fetched")
			return currentResp, nil

		case statusCode >= 500:
			resLog.Warn("Server error, retrying...")
			lastErr = fmt.Errorf("%w: status %d %s", utils.ErrServerHTTPError, statusCode, currentResp.Status)
			drainAndClose(currentResp)
			continue

		case statusCode == http.StatusTooManyRequests:
			resLog.Warn("Received 429 Too Many Requests, retrying...")
			lastErr = fmt.Errorf("%w: status %d %s", utils.ErrClientHTTPError, statusCode, currentResp.Status)
			drainAndClose(currentResp)
			continue

		case statusCode >= 400 && statusCode < 500:
			// 4xx (except 429) are not retryable; caller must close body.
			resLog.Warn("Client error (4xx), not retrying")
			return currentResp, fmt.Errorf("%w: status %d %s", utils.ErrClientHTTPError, statusCode, currentResp.Status)

		default:
			// Non-2xx, non-retryable; caller must close body.
			resLog.Warn(fmt.Sprintf("Non-retryable/unexpected status: %d", statusCode))
			return currentResp, fmt.Errorf("%w: status %d %s", utils.ErrOtherHTTPError, statusCode, currentResp.Status)
		}
	}

	reqLog.Error(fmt.Sprintf("All %d fetch retries failed. Last error: %v", maxRetries+1, lastErr))
	drainAndClose(currentResp)

	if lastErr != nil {
		if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
			return nil, lastErr
		}
		return nil, fmt.Errorf("%w: %w", utils.ErrRetryFailed, lastErr)
	}

	return nil, utils.ErrRetryFailed
}

// drainAndClose drains and closes a response body so the connection can be
// reused. Safe to call with a nil response.
func drainAndClose(resp *http.Response) {
	if resp != nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}
