package utils

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
)

// --- Sentinel Errors for Categorization ---
var (
	ErrRetryFailed     = errors.New("request failed after all retries") // Wraps the last underlying error
	ErrClientHTTPError = errors.New("client HTTP error (4xx)")          // Wraps original error/status
	ErrServerHTTPError = errors.New("server HTTP error (5xx)")          // Wraps original error/status
	ErrOtherHTTPError  = errors.New("other HTTP error (non-2xx)")       // Wraps original error/status
	ErrRobotsDisallowed   = errors.New("disallowed by robots.txt")
	ErrScopeViolation     = errors.New("URL out of scope (domain/prefix/pattern)")
	ErrMaxDepthExceeded   = errors.New("maximum crawl depth exceeded")
	ErrContentSelector    = errors.New("content selector not found")
	ErrParsing            = errors.New("parsing error")    // Wraps specific parsing error (HTML, URL, JSON, XML)
	ErrFilesystem         = errors.New("filesystem error") // Wraps os errors
	ErrDatabase           = errors.New("database error")   // Wraps badger errors
	ErrSemaphoreTimeout   = errors.New("timeout acquiring semaphore")
	ErrRequestCreation    = errors.New("failed to create HTTP request")
	ErrResponseBodyRead   = errors.New("failed to read response body")
	ErrMarkdownConversion = errors.New("failed to convert HTML to markdown")
	ErrConfigValidation   = errors.New("configuration validation error")
)

// CategorizeError maps an error to a predefined category string for logging/metrics.
func CategorizeError(err error) string {
	if err == nil {
		return "None"
	}

	// Check against sentinel errors first
	switch {
	case errors.Is(err, ErrRetryFailed):
		underlying := errors.Unwrap(err)
		if underlying != nil {
			if errors.Is(underlying, ErrServerHTTPError) {
				return "RetryFailed_HTTPServer"
			}
			if errors.Is(underlying, ErrClientHTTPError) {
				return "RetryFailed_HTTPClient"
			}

			// Check for common network error substrings if wrapped error isn't a known sentinel
			errMsg := underlying.Error()
			if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "Timeout") || strings.Contains(errMsg, "deadline exceeded") {
				return "RetryFailed_NetworkTimeout"
			}
			if strings.Contains(errMsg, "connection refused") {
				return "RetryFailed_ConnectionRefused"
			}
			if strings.Contains(errMsg, "no such host") {
				return "RetryFailed_DNSLookup"
			}
			var netErr net.Error
			if errors.As(underlying, &netErr) {
				if netErr.Timeout() {
					return "RetryFailed_NetworkTimeout"
				}
			}
			return "RetryFailed_NetworkOther" // Catch-all for other network errors after retry
		}
		return "RetryFailed_Unknown" // Retry failed, but couldn't identify underlying cause
	case errors.Is(err, ErrClientHTTPError):
		// Could try to extract exact 4xx code if needed, but category is often enough
		errMsg := err.Error()
		if strings.Contains(errMsg, " 404 ") {
			return "HTTP_404"
		}
		if strings.Contains(errMsg, " 403 ") {
			return "HTTP_403"
		}
		if strings.Contains(errMsg, " 401 ") {
			return "HTTP_401"
		}
		if strings.Contains(errMsg, " 429 ") {
			return "HTTP_429"
		}
		return "HTTP_4xx" // Generic 4xx
	case errors.Is(err, ErrServerHTTPError):
		// Should only see this wrapped by ErrRetryFailed usually, but handle directly too
		return "HTTP_5xx"
	case errors.Is(err, ErrOtherHTTPError):
		return "HTTP_OtherStatus"
	case errors.Is(err, ErrRobotsDisallowed):
		return "Policy_Robots"
	case errors.Is(err, ErrScopeViolation):
		return "Policy_Scope"
	case errors.Is(err, ErrMaxDepthExceeded):
		return "Policy_MaxDepth"
	case errors.Is(err, ErrContentSelector):
		return "Content_SelectorNotFound"
	case errors.Is(err, ErrParsing):
		// Could check wrapped error for URL vs HTML vs JSON vs XML parsing if needed
		errMsg := err.Error()
		if strings.Contains(errMsg, "URL") {
			return "Content_ParsingURL"
		}
		if strings.Contains(errMsg, "HTML") {
			return "Content_ParsingHTML"
		}
		if strings.Contains(errMsg, "JSON") {
			return "Content_ParsingJSON"
		}
		if strings.Contains(errMsg, "XML") {
			return "Content_ParsingXML"
		}
		return "Content_ParsingOther"
	case errors.Is(err, ErrMarkdownConversion):
		return "Content_Markdown"
	case errors.Is(err, ErrFilesystem):
		if errors.Is(err, os.ErrPermission) {
			return "Filesystem_Permission"
		}
		if errors.Is(err, os.ErrNotExist) {
			return "Filesystem_NotExist"
		}
		if errors.Is(err, os.ErrExist) {
			return "Filesystem_Exist"
		}
		// Add checks for disk full? requires syscall or specific error strings/numbers per OS
		return "Filesystem_Other"
	case errors.Is(err, ErrDatabase):
		// Could check for specific Badger errors if necessary
		return "Database_Other"
	case errors.Is(err, ErrSemaphoreTimeout):
		return "Resource_SemaphoreTimeout"
	case errors.Is(err, ErrRequestCreation):
		return "Internal_RequestCreation"
	case errors.Is(err, ErrResponseBodyRead):
		return "Network_BodyRead"
	case errors.Is(err, ErrConfigValidation):
		return "Config_Validation"
	}

	// --- Fallback checks for common underlying error types/strings ---

	// Context errors
	if errors.Is(err, context.Canceled) {
		return "System_ContextCanceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// Check if it was semaphore timeout wrapped in context error
		if strings.Contains(err.Error(), "semaphore") {
			return "Resource_SemaphoreTimeout"
		}
		return "System_ContextDeadlineExceeded"
	}

	// Network errors (if not wrapped by custom sentinels)
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "Network_Timeout"
		}
		// Other net.Error checks
	}
	errMsg := err.Error()
	// Use lowercase for reliable substring checks
	lowerErrMsg := strings.ToLower(errMsg)
	if strings.Contains(lowerErrMsg, "timeout") {
		return "Network_TimeoutGeneric"
	}
	if strings.Contains(lowerErrMsg, "connection refused") {
		return "Network_ConnectionRefused"
	}
	if strings.Contains(lowerErrMsg, "no such host") {
		return "Network_DNSLookup"
	}
	if strings.Contains(lowerErrMsg, "tls") || strings.Contains(lowerErrMsg, "certificate") {
		return "Network_TLS"
	}
	if strings.Contains(lowerErrMsg, "reset by peer") {
		return "Network_ConnectionReset"
	}
	if strings.Contains(lowerErrMsg, "broken pipe") {
		return "Network_BrokenPipe"
	}

	return "Unknown"
}
