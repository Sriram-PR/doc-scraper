package fetch

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
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
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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

			require.NoError(t, err)
			require.NotNil(t, resp)
			defer resp.Body.Close()

			assert.Equal(t, tt.statusCode, resp.StatusCode)
			assert.Equal(t, int32(1), attempts.Load())
		})
	}
}

func TestFetchWithRetry_ServerError_RetrySuccess(t *testing.T) {
	// 500 → 500 → 200 (succeeds on 3rd attempt)
	server, attempts := mockServer(t, []int{500, 500, 200})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(3), attempts.Load())
}

func TestFetchWithRetry_ServerError_AllRetriesFail(t *testing.T) {
	// 500 × 4 (initial + 3 retries = 4 attempts)
	server, attempts := mockServer(t, []int{500, 500, 500, 500})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	require.Error(t, err)
	if resp != nil {
		resp.Body.Close()
	}
	assert.Nil(t, resp)
	require.ErrorIs(t, err, utils.ErrRetryFailed)
	require.ErrorIs(t, err, utils.ErrServerHTTPError)
	assert.Equal(t, int32(4), attempts.Load())
}

func TestFetchWithRetry_RateLimit_RetrySuccess(t *testing.T) {
	// 429 → 200 (succeeds on 2nd attempt)
	server, attempts := mockServer(t, []int{429, 200})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(2), attempts.Load())
}

func TestFetchWithRetry_RateLimit_AllRetriesFail(t *testing.T) {
	// 429 × 4 (all retries exhausted)
	server, attempts := mockServer(t, []int{429, 429, 429, 429})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	require.Error(t, err)
	if resp != nil {
		resp.Body.Close()
	}
	assert.Nil(t, resp)
	require.ErrorIs(t, err, utils.ErrRetryFailed)
	assert.Equal(t, int32(4), attempts.Load())
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
			require.ErrorIs(t, err, utils.ErrClientHTTPError)
			require.NotNil(t, resp)
			defer resp.Body.Close()

			assert.Equal(t, tt.statusCode, resp.StatusCode)
			// No retry for 4xx (except 429)
			assert.Equal(t, int32(1), attempts.Load())
		})
	}
}

func TestFetchWithRetry_OtherStatus(t *testing.T) {
	// Test 3xx and other non-2xx statuses (assuming redirects are disabled)
	server, attempts := mockServer(t, []int{301})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	require.ErrorIs(t, err, utils.ErrOtherHTTPError)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	assert.Equal(t, int32(1), attempts.Load())
}

func TestFetchWithRetry_ContextCancelled_BeforeAttempt(t *testing.T) {
	server, attempts := mockServer(t, []int{200})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	// Cancel context before calling FetchWithRetry
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, err := fetcher.FetchWithRetry(req, ctx)

	require.ErrorIs(t, err, context.Canceled)
	if resp != nil {
		resp.Body.Close()
	}
	assert.Nil(t, resp)
	assert.Equal(t, int32(0), attempts.Load())
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

	require.Error(t, err)
	if resp != nil {
		resp.Body.Close()
	}
	assert.Nil(t, resp)
	// Should have made exactly 1 attempt before timeout during backoff
	assert.Equal(t, int32(1), attemptCount.Load())
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

	require.Error(t, err)
	if resp != nil {
		resp.Body.Close()
	}
	assert.Nil(t, resp)
	// Context timeout should be detected
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestFetchWithRetry_NetworkError_RetrySuccess(t *testing.T) {
	attemptCount := &atomic.Int32{}

	// Handler that fails first request, succeeds on second
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attemptCount.Add(1)
		if attempt == 1 {
			// Close connection to simulate network error
			hj, ok := w.(http.Hijacker)
			if !assert.True(t, ok, "server doesn't support hijacking") {
				return
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

	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(2), attemptCount.Load())
}

func TestFetchWithRetry_MixedErrors(t *testing.T) {
	// 500 → 429 → 500 → 200 (mixed retryable errors, then success)
	server, attempts := mockServer(t, []int{500, 429, 500, 200})

	fetcher := NewFetcher(testClient(), testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(4), attempts.Load())
}

func TestFetchWithRetry_ZeroRetries(t *testing.T) {
	// With maxRetries=0, only initial attempt should be made
	server, attempts := mockServer(t, []int{500})

	fetcher := NewFetcher(testClient(), testConfig(0), testLogger())
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())

	require.Error(t, err)
	if resp != nil {
		resp.Body.Close()
	}
	require.ErrorIs(t, err, utils.ErrRetryFailed)
	assert.Equal(t, int32(1), attempts.Load())
}

type errTransport struct {
	calls atomic.Int32
	err   error
}

func (e *errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	e.calls.Add(1)
	return nil, e.err
}

func TestFetchWithRetry_DNSNotFound_FailsFast(t *testing.T) {
	tr := &errTransport{err: &net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}}
	fetcher := NewFetcher(&http.Client{Transport: tr}, testConfig(3), testLogger())
	req, _ := http.NewRequest(http.MethodGet, "http://nope.invalid/", nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())
	if resp != nil {
		resp.Body.Close()
	}
	var dnsErr *net.DNSError
	require.ErrorAs(t, err, &dnsErr)
	assert.True(t, dnsErr.IsNotFound)
	assert.Equal(t, int32(1), tr.calls.Load())
}

func TestFetchWithRetry_DNSTemporary_Retries(t *testing.T) {
	tr := &errTransport{err: &net.DNSError{Err: "server misbehaving", Name: "flaky.test", IsTemporary: true}}
	fetcher := NewFetcher(&http.Client{Transport: tr}, testConfig(2), testLogger())
	req, _ := http.NewRequest(http.MethodGet, "http://flaky.test/", nil)

	resp, err := fetcher.FetchWithRetry(req, context.Background())
	if resp != nil {
		resp.Body.Close()
	}
	require.ErrorIs(t, err, utils.ErrRetryFailed)
	assert.Equal(t, int32(3), tr.calls.Load())
}
