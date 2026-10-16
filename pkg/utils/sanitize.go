package utils

import (
	"path/filepath"
	"regexp"
	"strings"
)

var invalidFilenameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]`) // Characters invalid in Windows/Unix filenames
var consecutiveUnderscores = regexp.MustCompile(`_+`)                  // Pattern to replace multiple underscores with one
const maxFilenameLength = 100                                          // Max length for sanitized filenames

// SanitizeFilename cleans a string to be safe for use as a filename component
func SanitizeFilename(name string) string {
	sanitized := invalidFilenameChars.ReplaceAllString(name, "_")
	sanitized = consecutiveUnderscores.ReplaceAllString(sanitized, "_")
	sanitized = strings.Trim(sanitized, "_ ")

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
		sanitized = "untitled"
	}
	return sanitized
}

// IsWithinDir reports whether path lies strictly inside dir, comparing the
// cleaned paths lexically (symlinks are not resolved).
func IsWithinDir(dir, path string) bool {
	dir = filepath.Clean(dir)
	path = filepath.Clean(path)
	prefix := dir
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return path != dir && strings.HasPrefix(path, prefix)
}

// StagingSuffix names the sibling a fresh crawl builds into before replacing the live path.
const StagingSuffix = ".staging"
