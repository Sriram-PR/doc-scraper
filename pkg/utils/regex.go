package utils

import (
	"fmt"
	"regexp"
)

// CompileRegexPatterns compiles regex strings into usable *regexp.Regexp objects.
// Returns an error if any pattern is invalid.
func CompileRegexPatterns(patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for i, pattern := range patterns {
		if pattern == "" { // Skip empty patterns silently
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			// Return a specific error including the pattern index and content
			// Use the config validation sentinel error?
			return nil, fmt.Errorf("%w: invalid regex pattern #%d ('%s'): %w", ErrConfigValidation, i+1, pattern, err)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}
