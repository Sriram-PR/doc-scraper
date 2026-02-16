package utils

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// --- CategorizeError Tests ---

func TestCategorizeError_NilError(t *testing.T) {
	result := CategorizeError(nil)
	if result != "None" {
		t.Errorf("CategorizeError(nil) = %q, want %q", result, "None")
	}
}

func TestCategorizeError_SentinelErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"RobotsDisallowed", ErrRobotsDisallowed, "Policy_Robots"},
		{"ScopeViolation", ErrScopeViolation, "Policy_Scope"},
		{"MaxDepthExceeded", ErrMaxDepthExceeded, "Policy_MaxDepth"},
		{"ContentSelector", ErrContentSelector, "Content_SelectorNotFound"},
		{"MarkdownConversion", ErrMarkdownConversion, "Content_Markdown"},
		{"SemaphoreTimeout", ErrSemaphoreTimeout, "Resource_SemaphoreTimeout"},
		{"RequestCreation", ErrRequestCreation, "Internal_RequestCreation"},
		{"ResponseBodyRead", ErrResponseBodyRead, "Network_BodyRead"},
		{"ConfigValidation", ErrConfigValidation, "Config_Validation"},
		{"ServerHTTPError", ErrServerHTTPError, "HTTP_5xx"},
		{"OtherHTTPError", ErrOtherHTTPError, "HTTP_OtherStatus"},
		{"Database", ErrDatabase, "Database_Other"},
		{"Filesystem", ErrFilesystem, "Filesystem_Other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CategorizeError(tt.err)
			if result != tt.expected {
				t.Errorf("CategorizeError(%v) = %q, want %q", tt.err, result, tt.expected)
			}
		})
	}
}

func TestCategorizeError_WrappedErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "WrappedRobotsDisallowed",
			err:      fmt.Errorf("some context: %w", ErrRobotsDisallowed),
			expected: "Policy_Robots",
		},
		{
			name:     "WrappedScopeViolation",
			err:      fmt.Errorf("URL out of scope: %w", ErrScopeViolation),
			expected: "Policy_Scope",
		},
		{
			name:     "DoubleWrapped",
			err:      fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", ErrMaxDepthExceeded)),
			expected: "Policy_MaxDepth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CategorizeError(tt.err)
			if result != tt.expected {
				t.Errorf("CategorizeError(%v) = %q, want %q", tt.err, result, tt.expected)
			}
		})
	}
}

func TestCategorizeError_ClientHTTPCodes(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "404",
			err:      fmt.Errorf("HTTP status 404 : %w", ErrClientHTTPError),
			expected: "HTTP_404",
		},
		{
			name:     "403",
			err:      fmt.Errorf("HTTP status 403 : %w", ErrClientHTTPError),
			expected: "HTTP_403",
		},
		{
			name:     "401",
			err:      fmt.Errorf("HTTP status 401 : %w", ErrClientHTTPError),
			expected: "HTTP_401",
		},
		{
			name:     "429",
			err:      fmt.Errorf("HTTP status 429 : %w", ErrClientHTTPError),
			expected: "HTTP_429",
		},
		{
			name:     "Generic4xx",
			err:      fmt.Errorf("HTTP status 400: %w", ErrClientHTTPError),
			expected: "HTTP_4xx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CategorizeError(tt.err)
			if result != tt.expected {
				t.Errorf("CategorizeError(%v) = %q, want %q", tt.err, result, tt.expected)
			}
		})
	}
}

func TestCategorizeError_ParsingErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "URLParsing",
			err:      fmt.Errorf("URL parsing failed: %w", ErrParsing),
			expected: "Content_ParsingURL",
		},
		{
			name:     "HTMLParsing",
			err:      fmt.Errorf("HTML parsing failed: %w", ErrParsing),
			expected: "Content_ParsingHTML",
		},
		{
			name:     "JSONParsing",
			err:      fmt.Errorf("JSON parsing failed: %w", ErrParsing),
			expected: "Content_ParsingJSON",
		},
		{
			name:     "XMLParsing",
			err:      fmt.Errorf("XML parsing failed: %w", ErrParsing),
			expected: "Content_ParsingXML",
		},
		{
			name:     "GenericParsing",
			err:      fmt.Errorf("parsing failed: %w", ErrParsing),
			expected: "Content_ParsingOther",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CategorizeError(tt.err)
			if result != tt.expected {
				t.Errorf("CategorizeError(%v) = %q, want %q", tt.err, result, tt.expected)
			}
		})
	}
}

func TestCategorizeError_ContextErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"ContextCanceled", context.Canceled, "System_ContextCanceled"},
		{"ContextDeadlineExceeded", context.DeadlineExceeded, "System_ContextDeadlineExceeded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CategorizeError(tt.err)
			if result != tt.expected {
				t.Errorf("CategorizeError(%v) = %q, want %q", tt.err, result, tt.expected)
			}
		})
	}
}

func TestCategorizeError_NetworkStrings(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"Timeout", errors.New("connection timeout occurred"), "Network_TimeoutGeneric"},
		{"ConnectionRefused", errors.New("connection refused"), "Network_ConnectionRefused"},
		{"DNSLookup", errors.New("no such host"), "Network_DNSLookup"},
		{"TLS", errors.New("tls handshake failed"), "Network_TLS"},
		{"Certificate", errors.New("certificate verify failed"), "Network_TLS"},
		{"ConnectionReset", errors.New("reset by peer"), "Network_ConnectionReset"},
		{"BrokenPipe", errors.New("broken pipe"), "Network_BrokenPipe"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CategorizeError(tt.err)
			if result != tt.expected {
				t.Errorf("CategorizeError(%v) = %q, want %q", tt.err, result, tt.expected)
			}
		})
	}
}

func TestCategorizeError_Unknown(t *testing.T) {
	err := errors.New("some completely unknown error")
	result := CategorizeError(err)
	if result != "Unknown" {
		t.Errorf("CategorizeError(%v) = %q, want %q", err, result, "Unknown")
	}
}

// --- SanitizeFilename Tests ---

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"Simple", "hello", "hello"},
		{"WithSpaces", "hello world", "hello world"},
		{"WithSlash", "path/to/file", "path_to_file"},
		{"WithBackslash", "path\\to\\file", "path_to_file"},
		{"WithColon", "file:name", "file_name"},
		{"WithQuotes", `file"name`, "file_name"},
		{"WithMultipleInvalid", "a<b>c:d", "a_b_c_d"},
		{"ConsecutiveUnderscores", "a___b", "a_b"},
		{"LeadingUnderscore", "_file", "file"},
		{"TrailingUnderscore", "file_", "file"},
		{"LeadingTrailingSpaces", "  file  ", "file"},
		{"Empty", "", "untitled"},
		{"OnlyInvalidChars", "<>:", "untitled"},
		{"OnlyUnderscores", "___", "untitled"},
		{"QuestionMark", "file?name", "file_name"},
		{"Asterisk", "file*name", "file_name"},
		{"Pipe", "file|name", "file_name"},
		{"NullChar", "file\x00name", "file_name"},
		{"ControlChars", "file\x01\x02name", "file_name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeFilename(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSanitizeFilename_LongNames(t *testing.T) {
	// Create a string longer than maxFilenameLength (100)
	longName := ""
	for i := 0; i < 150; i++ {
		longName += "a"
	}

	result := SanitizeFilename(longName)
	if len(result) > 100 {
		t.Errorf("SanitizeFilename(long) length = %d, want <= 100", len(result))
	}
}

// --- CompileRegexPatterns Tests ---

func TestCompileRegexPatterns_ValidPatterns(t *testing.T) {
	patterns := []string{
		`^/docs/.*`,
		`\.html$`,
		`[a-z]+`,
	}

	compiled, err := CompileRegexPatterns(patterns)
	if err != nil {
		t.Fatalf("CompileRegexPatterns() unexpected error: %v", err)
	}
	if len(compiled) != 3 {
		t.Errorf("CompileRegexPatterns() returned %d patterns, want 3", len(compiled))
	}
}

func TestCompileRegexPatterns_EmptySlice(t *testing.T) {
	compiled, err := CompileRegexPatterns([]string{})
	if err != nil {
		t.Fatalf("CompileRegexPatterns([]) unexpected error: %v", err)
	}
	if len(compiled) != 0 {
		t.Errorf("CompileRegexPatterns([]) returned %d patterns, want 0", len(compiled))
	}
}

func TestCompileRegexPatterns_EmptyStringsSkipped(t *testing.T) {
	patterns := []string{"valid", "", "also_valid", ""}

	compiled, err := CompileRegexPatterns(patterns)
	if err != nil {
		t.Fatalf("CompileRegexPatterns() unexpected error: %v", err)
	}
	if len(compiled) != 2 {
		t.Errorf("CompileRegexPatterns() returned %d patterns, want 2", len(compiled))
	}
}

func TestCompileRegexPatterns_InvalidPattern(t *testing.T) {
	patterns := []string{
		`valid`,
		`[invalid`, // Unclosed bracket
	}

	_, err := CompileRegexPatterns(patterns)
	if err == nil {
		t.Fatal("CompileRegexPatterns() expected error for invalid pattern, got nil")
	}
	if !errors.Is(err, ErrConfigValidation) {
		t.Errorf("CompileRegexPatterns() error = %v, want wrapped ErrConfigValidation", err)
	}
}

// --- CalculateStringSHA256 Tests ---

func TestCalculateStringSHA256(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string // SHA256 hex output
	}{
		{
			name:     "EmptyString",
			input:    "",
			expected: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:     "HelloWorld",
			input:    "hello world",
			expected: "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
		},
		{
			name:     "SimpleText",
			input:    "test",
			expected: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateStringSHA256(tt.input)
			if result != tt.expected {
				t.Errorf("CalculateStringSHA256(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// --- CalculateFileSHA256 Tests ---

func TestCalculateFileSHA256(t *testing.T) {
	// Create a temporary file with known content
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.txt")

	content := "hello world"
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	result, err := CalculateFileSHA256(tmpFile)
	if err != nil {
		t.Fatalf("CalculateFileSHA256() unexpected error: %v", err)
	}

	// Same content as TestCalculateStringSHA256 "HelloWorld" case
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if result != expected {
		t.Errorf("CalculateFileSHA256() = %q, want %q", result, expected)
	}
}

func TestCalculateFileSHA256_NonExistentFile(t *testing.T) {
	_, err := CalculateFileSHA256("/nonexistent/path/file.txt")
	if err == nil {
		t.Error("CalculateFileSHA256() expected error for non-existent file, got nil")
	}
}

func TestCalculateFileSHA256_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "empty.txt")

	if err := os.WriteFile(tmpFile, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create empty test file: %v", err)
	}

	result, err := CalculateFileSHA256(tmpFile)
	if err != nil {
		t.Fatalf("CalculateFileSHA256() unexpected error: %v", err)
	}

	// SHA256 of empty content
	expected := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if result != expected {
		t.Errorf("CalculateFileSHA256(empty) = %q, want %q", result, expected)
	}
}

// --- WrapErrorf Tests ---

func TestWrapErrorf_NilError(t *testing.T) {
	result := WrapErrorf(nil, "some context")
	if result != nil {
		t.Errorf("WrapErrorf(nil, ...) = %v, want nil", result)
	}
}

func TestWrapErrorf_WrapsError(t *testing.T) {
	original := errors.New("original error")
	wrapped := WrapErrorf(original, "context %s", "value")

	if wrapped == nil {
		t.Fatal("WrapErrorf() returned nil, want error")
	}
	if !errors.Is(wrapped, original) {
		t.Error("WrapErrorf() result should wrap original error")
	}
	expectedMsg := "context value: original error"
	if wrapped.Error() != expectedMsg {
		t.Errorf("WrapErrorf() message = %q, want %q", wrapped.Error(), expectedMsg)
	}
}
