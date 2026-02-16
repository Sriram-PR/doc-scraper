package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

// Fetcher handles making HTTP requests with configured retry logic, using an underlying http.Client
type Fetcher struct {
	client *http.Client     // The configured HTTP client to use for requests
	cfg    *config.AppConfig // Application config, needed primarily for retry settings
	log    *logrus.Logger
}

// NewFetcher creates a new Fetcher instance
func NewFetcher(client *http.Client, cfg *config.AppConfig, log *logrus.Logger) *Fetcher {
	return &Fetcher{
		client: client,
		cfg:    cfg,
		log:    log,
	}
}

// FetchWithRetry performs an HTTP request associated with the provided context
// It implements a retry mechanism with exponential backoff and jitter for transient network errors and specific HTTP status codes (5xx, 429)
func (f *Fetcher) FetchWithRetry(req *http.Request, ctx context.Context) (*http.Response, error) {
	var lastErr error              // Stores the error from the *last* failed attempt in the loop
	var currentResp *http.Response // Stores the response from the *current* attempt (potentially failed)

	reqLog := f.log.WithField("url", req.URL.String())

	// Get retry settings from the application configuration
	maxRetries := f.cfg.MaxRetries
	initialRetryDelay := f.cfg.InitialRetryDelay
	maxRetryDelay := f.cfg.MaxRetryDelay

	// Retry loop: Try up to maxRetries+1 times (initial attempt + retries)
	for attempt := 0; attempt <= maxRetries; attempt++ {

		// --- Context Check ---
		// Check if the context has been cancelled *before* making the attempt or sleeping
		select {
		case <-ctx.Done():
			reqLog.Warnf("Context cancelled before attempt %d: %v", attempt, ctx.Err())
			// If context cancelled, wrap the last known error (if any) or return the context error
			if lastErr != nil {
				return nil, fmt.Errorf("context cancelled (%v) during retry backoff after error: %w", ctx.Err(), lastErr)
			}
			return nil, fmt.Errorf("context cancelled before first attempt: %w", ctx.Err())
		default:
			// Context is still active, proceed with the attempt
		}

		// --- Exponential Backoff Delay ---
		// Apply delay only *before* retry attempts (not before the first attempt)
		if attempt > 0 {
			// Calculate delay: initial * 2^(attempt-1), capped by maxRetryDelay
			backoff := float64(initialRetryDelay) * math.Pow(2, float64(attempt-1))
			delay := time.Duration(backoff)
			if delay <= 0 || delay > maxRetryDelay { // Handle zero/negative initial delay or cap exceeding max
				delay = maxRetryDelay
			}

			// Add jitter: +/- 10% of the calculated delay to help avoid thundering herd
			// Ensure delay calculation doesn't lead to negative division in rand.Int63n
			var jitter time.Duration
			if delay > 0 {
				jitter = time.Duration(rand.Int63n(int64(delay)/5)) - (delay / 10) // +/- 10% range is delay/5 wide centered at 0
			}
			finalDelay := delay + jitter
			if finalDelay < 0 { // Ensure final delay isn't negative
				finalDelay = 0
			}

			reqLog.WithFields(logrus.Fields{"attempt": attempt, "max_retries": maxRetries, "delay": finalDelay}).Warn("Retrying request...")

			// Wait for the calculated delay, but respect context cancellation during the wait
			select {
			case <-time.After(finalDelay):
				// Sleep completed normally
			case <-ctx.Done():
				// Context was cancelled *during* the sleep
				reqLog.Warnf("Context cancelled during retry sleep: %v", ctx.Err())
				// Return the error from the *previous* attempt, wrapped with context info
				if lastErr != nil {
					return nil, fmt.Errorf("context cancelled (%v) during retry delay after error: %w", ctx.Err(), lastErr)
				}
				return nil, fmt.Errorf("context cancelled during retry delay: %w", ctx.Err())
			}
		}

		// --- Perform HTTP Request ---
		// Attach the current context to the request for this attempt
		reqWithCtx := req.WithContext(ctx)
		// Execute the request using the underlying HTTP client
		currentResp, lastErr = f.client.Do(reqWithCtx)

		// --- Handle Network-Level Errors ---
		// Errors occurring before getting an HTTP response (DNS, TCP, TLS errors etc.)
		if lastErr != nil {
			// Check specifically for context cancellation/timeout during the HTTP call itself
			if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
				reqLog.Warnf("Context cancelled/timed out during HTTP request execution: %v", lastErr)
				// Ensure response body (if partially received) is closed
				if currentResp != nil {
					io.Copy(io.Discard, currentResp.Body)
					currentResp.Body.Close()
				}
				// Do not retry context errors. Return the context error directly
				return nil, lastErr
			}

			// Check for URL errors - these are typically not retryable
			var urlErr *url.Error
			if errors.As(lastErr, &urlErr) {
				// Could add more specific checks here if needed, e.g., urlErr.Temporary()
				// For now, most url.Error types resulting from Do() are network-related, so we retry
			}

			// Log other network errors and proceed to retry
			reqLog.WithField("attempt", attempt).Errorf("Network error: %v", lastErr)
			// Ensure body is closed if a response object exists despite the error
			if currentResp != nil {
				io.Copy(io.Discard, currentResp.Body)
				currentResp.Body.Close()
			}
			continue // Go to the next retry attempt for network errors
		}

		// --- Handle HTTP Status Codes ---
		// If lastErr is nil, we received an HTTP response - Check its status code
		statusCode := currentResp.StatusCode
		resLog := reqLog.WithFields(logrus.Fields{"status_code": statusCode, "status": currentResp.Status, "attempt": attempt})

		switch {
		case statusCode >= 200 && statusCode < 300:
			// Success (2xx)! Return the response immediately - Caller must close body
			resLog.Debug("Successfully fetched")
			return currentResp, nil

		case statusCode >= 500:
			// Server Error (5xx). These are potentially transient, so retry
			resLog.Warn("Server error, retrying...")
			// Store the error for this attempt, wrapped with a sentinel type
			lastErr = fmt.Errorf("%w: status %d %s", utils.ErrServerHTTPError, statusCode, currentResp.Status)
			// Must drain and close the body before the next retry attempt
			io.Copy(io.Discard, currentResp.Body)
			currentResp.Body.Close()
			continue // Go to the next retry attempt

		case statusCode == http.StatusTooManyRequests: // Specifically handle 429
			// Rate limited by the server; Retry according to policy
			// Future enhancement: Parse Retry-After header for smarter delay
			resLog.Warn("Received 429 Too Many Requests, retrying...")
			lastErr = fmt.Errorf("%w: status %d %s", utils.ErrClientHTTPError, statusCode, currentResp.Status) // Categorize as Client error for now
			io.Copy(io.Discard, currentResp.Body)
			currentResp.Body.Close()
			continue // Go to the next retry attempt

		case statusCode >= 400 && statusCode < 500:
			// Other Client Errors (4xx, excluding 429). These are generally not retryable (e.g., 404 Not Found, 403 Forbidden)
			resLog.Warn("Client error (4xx), not retrying")
			// Return the response object (caller might want to inspect headers/body)
			// along with a wrapped error indicating a non-retryable client error
			// *** Caller MUST close currentResp.Body in this case ***
			return currentResp, fmt.Errorf("%w: status %d %s", utils.ErrClientHTTPError, statusCode, currentResp.Status)

		default:
			// Other non-2xx statuses (e.g., 3xx if redirects were disabled, or other unexpected codes)
			// Treat these as non-retryable.
			resLog.Warnf("Non-retryable/unexpected status: %d", statusCode)
			// Return the response and a wrapped error
			// *** Caller MUST close currentResp.Body in this case ***
			return currentResp, fmt.Errorf("%w: status %d %s", utils.ErrOtherHTTPError, statusCode, currentResp.Status)
		}
	}

	// --- All Retries Failed ---
	// If the loop completes, all attempts (initial + retries) have failed
	reqLog.Errorf("All %d fetch retries failed. Last error: %v", maxRetries+1, lastErr)
	// Ensure the body of the *last* response (if any) is closed
	if currentResp != nil {
		io.Copy(io.Discard, currentResp.Body)
		currentResp.Body.Close()
	}

	// Wrap the *very last error* encountered (could be network error, 5xx, 429, or context error from last sleep)
	if lastErr != nil {
		// Check if the loop terminated because the context was cancelled during the *final* backoff sleep
		if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
			return nil, lastErr // Return the context error directly
		}
		// Otherwise, wrap the last HTTP/network error with the ErrRetryFailed sentinel
		return nil, fmt.Errorf("%w: %w", utils.ErrRetryFailed, lastErr)
	}

	// This case should be theoretically unreachable if maxRetries >= 0, but return a generic ErrRetryFailed if lastErr was somehow nil after the loop
	return nil, utils.ErrRetryFailed
}
