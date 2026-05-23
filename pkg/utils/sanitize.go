package utils

import (
	"regexp"
	"strings"
)

var invalidFilenameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]`) // Characters invalid in Windows/Unix filenames
var consecutiveUnderscores = regexp.MustCompile(`_+`)                  // Pattern to replace multiple underscores with one
const maxFilenameLength = 100                                          // Max length for sanitized filenames

// SanitizeFilename cleans a string to be safe for use as a filename component
func SanitizeFilename(name string) string {
	sanitized := invalidFilenameChars.ReplaceAllString(name, "_")       // Replace invalid chars with underscore
	sanitized = consecutiveUnderscores.ReplaceAllString(sanitized, "_") // Collapse multiple underscores
	sanitized = strings.Trim(sanitized, "_ ")                           // Remove leading/trailing underscores or spaces

	// Limit filename length (considering multi-byte characters)
	if len(sanitized) > maxFilenameLength {
		// Simple truncation by byte length is usually sufficient for sanitization purposes
		sanitized = sanitized[:maxFilenameLength]
		// Trim again in case truncation created leading/trailing underscores
		sanitized = strings.Trim(sanitized, "_ ")
	}

	// Reject segments composed entirely of dots (".", "..", "...") — these
	// would resolve to parent/current directory references when used in
	// filesystem paths and could escape the intended output directory.
	if sanitized == "" || strings.Trim(sanitized, ".") == "" {
		sanitized = "untitled" // Provide a default name
	}
	return sanitized
}
