package utils

import (
	"errors"
)

var (
	ErrRetryFailed      = errors.New("request failed after all retries") // Wraps the last underlying error
	ErrClientHTTPError  = errors.New("client HTTP error (4xx)")          // Wraps original error/status
	ErrServerHTTPError  = errors.New("server HTTP error (5xx)")          // Wraps original error/status
	ErrOtherHTTPError   = errors.New("other HTTP error (non-2xx)")       // Wraps original error/status
	ErrRobotsDisallowed = errors.New("disallowed by robots.txt")
	ErrScopeViolation   = errors.New("URL out of scope (domain/prefix/pattern)")
	ErrMaxDepthExceeded = errors.New("maximum crawl depth exceeded")
	ErrContentSelector  = errors.New("content selector not found")
	ErrParsing          = errors.New("parsing error")    // Wraps specific parsing error (HTML, URL, JSON, XML)
	ErrFilesystem       = errors.New("filesystem error") // Wraps os errors
	ErrDatabase         = errors.New("database error")   // Wraps badger errors
	ErrSemaphoreTimeout = errors.New("timeout acquiring semaphore")
	ErrRequestCreation  = errors.New("failed to create HTTP request")
	ErrBlockedAddress   = errors.New("blocked address (SSRF guard)") // Permanent; never retried
	ErrResponseBodyRead = errors.New("failed to read response body")
	ErrConfigValidation = errors.New("configuration validation error")
	ErrNonHTMLContent   = errors.New("non-HTML content type")
)
