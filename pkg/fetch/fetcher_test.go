package fetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

// testConfig returns an AppConfig with fast retry delays for testing
func testConfig(maxRetries int) *config.AppConfig {
	return &config.AppConfig{
		MaxRetries:        maxRetries,
		InitialRetryDelay: 10 * time.Millisecond,
		MaxRetryDelay:     50 * time.Millisecond,
	}
}

// testLogger returns a logger that discards output
func testLogger() *logrus.Entry {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return logrus.NewEntry(log)
}

// testClient returns an http.Client suitable for testing
func testClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second, // Generous timeout for tests
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// mockServer creates an httptest.Server that returns status codes in sequence.
// Returns the server and an atomic counter tracking request attempts.
func mockServer(t *testing.T, statusCodes []int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	attemptCount := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(attemptCount.Add(1)) - 1
		if idx >= len(statusCodes) {
			idx = len(statusCodes) - 1 // repeat last status
		}
		w.WriteHeader(statusCodes[idx])
	}))
	t.Cleanup(server.Close)
	return server, attemptCount
}

func TestFetchWithRetry_Success(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{"200 OK", http.StatusOK},
		{"201 Created", http.StatusCreated},
		{"204 No Content", http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, attempts := mockServer(t, []int{tt.statusCode})

			fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
			req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

			resp, err := fetcher.FetchWithRetry(req, context.Background())

			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			if resp == nil {
				t.Fatal("expected response, got nil")
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.statusCode {
				t.Errorf("expected status %d, got %d", tt.statusCode, resp.StatusCode)
			}
			if attempts.Load() != 1 {
				t.Errorf("expected 1 attempt, got %d", attempts.Load())
			}
		})
	}
}

func TestFetchWithRetry_ServerError_RetrySuccess(t *testing.T) {
	// 500 → 500 → 200 (succeeds on 3rd attempt)
	server, attempts := mockServer(t, []int{500, 500, 200})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	if err != nil {
		t.Fatalf("expected no error after retry, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestFetchWithRetry_ServerError_AllRetriesFail(t *testing.T) {
	// 500 × 4 (initial + 3 retries = 4 attempts)
	server, attempts := mockServer(t, []int{500, 500, 500, 500})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	if err == nil {
		t.Fatal("expected error after all retries failed")
	}
	if resp != nil {
		resp.Body.Close()
		t.Error("expected nil response when all retries fail")
	}
	if !errors.Is(err, utils.ErrRetryFailed) {
		t.Errorf("expected ErrRetryFailed, got: %v", err)
	}
	if !errors.Is(err, utils.ErrServerHTTPError) {
		t.Errorf("expected wrapped ErrServerHTTPError, got: %v", err)
	}
	if attempts.Load() != 4 {
		t.Errorf("expected 4 attempts (initial + 3 retries), got %d", attempts.Load())
	}
}

func TestFetchWithRetry_RateLimit_RetrySuccess(t *testing.T) {
	// 429 → 200 (succeeds on 2nd attempt)
	server, attempts := mockServer(t, []int{429, 200})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	if err != nil {
		t.Fatalf("expected no error after retry, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestFetchWithRetry_RateLimit_AllRetriesFail(t *testing.T) {
	// 429 × 4 (all retries exhausted)
	server, attempts := mockServer(t, []int{429, 429, 429, 429})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	if err == nil {
		t.Fatal("expected error after all retries failed")
	}
	if resp != nil {
		resp.Body.Close()
		t.Error("expected nil response when all retries fail")
	}
	if !errors.Is(err, utils.ErrRetryFailed) {
		t.Errorf("expected ErrRetryFailed, got: %v", err)
	}
	if attempts.Load() != 4 {
		t.Errorf("expected 4 attempts, got %d", attempts.Load())
	}
}

func TestFetchWithRetry_ClientError_NoRetry(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{"404 Not Found", http.StatusNotFound},
		{"403 Forbidden", http.StatusForbidden},
		{"401 Unauthorized", http.StatusUnauthorized},
		{"400 Bad Request", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, attempts := mockServer(t, []int{tt.statusCode})

			fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
			req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

			resp, err := fetcher.FetchWithRetry(req, context.Background())

			// 4xx errors return both response AND error (caller may need response)
			if err == nil {
				t.Fatal("expected error for 4xx status")
			}
			if !errors.Is(err, utils.ErrClientHTTPError) {
				t.Errorf("expected ErrClientHTTPError, got: %v", err)
			}
			if resp == nil {
				t.Fatal("expected response for 4xx (caller may need to inspect)")
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.statusCode {
				t.Errorf("expected status %d, got %d", tt.statusCode, resp.StatusCode)
			}
			// No retry for 4xx (except 429)
			if attempts.Load() != 1 {
				t.Errorf("expected 1 attempt (no retry for 4xx), got %d", attempts.Load())
			}
		})
	}
}

func TestFetchWithRetry_OtherStatus(t *testing.T) {
	// Test 3xx and other non-2xx statuses (assuming redirects are disabled)
	server, attempts := mockServer(t, []int{301})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	if err == nil {
		t.Fatal("expected error for non-2xx status")
	}
	if !errors.Is(err, utils.ErrOtherHTTPError) {
		t.Errorf("expected ErrOtherHTTPError, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response for non-2xx")
	}
	defer resp.Body.Close()

	if attempts.Load() != 1 {
		t.Errorf("expected 1 attempt (no retry for 3xx), got %d", attempts.Load())
	}
}

func TestFetchWithRetry_ContextCancelled_BeforeAttempt(t *testing.T) {
	server, attempts := mockServer(t, []int{200})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	// Cancel context before calling FetchWithRetry
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, err := fetcher.FetchWithRetry(req, ctx)

	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
		t.Error("expected nil response for cancelled context")
	}
	if attempts.Load() != 0 {
		t.Errorf("expected 0 attempts (cancelled before first attempt), got %d", attempts.Load())
	}
}

func TestFetchWithRetry_ContextTimeout_DuringBackoff(t *testing.T) {
	// First request returns 500, triggering retry with long backoff
	// Context times out during the backoff wait
	attemptCount := &atomic.Int32{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError) // Always return 500
	}))
	t.Cleanup(server.Close)

	cfg := testConfig(3)
	cfg.InitialRetryDelay = 10 * time.Second // Very long backoff
	cfg.MaxRetryDelay = 10 * time.Second

	fetcher := NewFetcher(testClient(), cfg, testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	// Context times out after 200ms (during first backoff)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	resp, err := fetcher.FetchWithRetry(req, ctx)

	if err == nil {
		t.Fatal("expected error for timed out context")
	}
	if resp != nil {
		resp.Body.Close()
		t.Error("expected nil response")
	}
	// Should have made exactly 1 attempt before timeout during backoff
	if attemptCount.Load() != 1 {
		t.Errorf("expected 1 attempt before timeout, got %d", attemptCount.Load())
	}
}

func TestFetchWithRetry_ContextTimeout_DuringRequest(t *testing.T) {
	// Server delays response longer than context timeout
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slowServer.Close)

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, slowServer.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resp, err := fetcher.FetchWithRetry(req, ctx)

	if err == nil {
		t.Fatal("expected error for timeout")
	}
	if resp != nil {
		resp.Body.Close()
		t.Error("expected nil response")
	}
	// Context timeout should be detected
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
}

func TestFetchWithRetry_NetworkError_RetrySuccess(t *testing.T) {
	attemptCount := &atomic.Int32{}

	// Handler that fails first request, succeeds on second
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attemptCount.Add(1)
		if attempt == 1 {
			// Close connection to simulate network error
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server doesn't support hijacking")
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if attemptCount.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attemptCount.Load())
	}
}

func TestFetchWithRetry_MixedErrors(t *testing.T) {
	// 500 → 429 → 500 → 200 (mixed retryable errors, then success)
	server, attempts := mockServer(t, []int{500, 429, 500, 200})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if attempts.Load() != 4 {
		t.Errorf("expected 4 attempts, got %d", attempts.Load())
	}
}

func TestFetchWithRetry_ZeroRetries(t *testing.T) {
	// With maxRetries=0, only initial attempt should be made
	server, attempts := mockServer(t, []int{500})

	fetcher := NewFetcher(testClient(), testConfig(0), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	if err == nil {
		t.Fatal("expected error with no retries")
	}
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, utils.ErrRetryFailed) {
		t.Errorf("expected ErrRetryFailed, got: %v", err)
	}
	if attempts.Load() != 1 {
		t.Errorf("expected 1 attempt (no retries), got %d", attempts.Load())
	}
}
