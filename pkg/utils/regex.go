package utils

import (
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
			return nil, WrapErrorf(ErrConfigValidation, "invalid regex pattern #%d ('%s')", i+1, pattern)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}
