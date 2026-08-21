package utils

import (
	"errors"
	"testing"
)

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
		{"SingleDot", ".", "untitled"},
		{"DoubleDot", "..", "untitled"},
		{"TripleDot", "...", "untitled"},
		{"DotsWithExtension", "file.html", "file.html"},
		{"LeadingDot", ".hidden", ".hidden"},
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
	for range 150 {
		longName += "a"
	}

	result := SanitizeFilename(longName)
	if len(result) > 100 {
		t.Errorf("SanitizeFilename(long) length = %d, want <= 100", len(result))
	}
}

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
