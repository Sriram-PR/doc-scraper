package watch

import (
	"testing"
	"time"
)

func TestParseInterval_StrictValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		expected time.Duration
	}{
		{name: "whole days", input: "7d", expected: 7 * 24 * time.Hour},
		{name: "days and hours", input: "1d12h", expected: 36 * time.Hour},
		{name: "standard duration", input: "90m", expected: 90 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseInterval(tc.input)
			if err != nil {
				t.Fatalf("ParseInterval(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.expected {
				t.Fatalf("ParseInterval(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}

	for _, input := range []string{
		"5x",
		"1.5d",
		"0",
		"0s",
		"-5m",
		"0d",
		"1d-2h",
	} {
		t.Run("reject_"+input, func(t *testing.T) {
			if got, err := ParseInterval(input); err == nil {
				t.Fatalf("ParseInterval(%q) = %v, expected an error", input, got)
			}
		})
	}
}
