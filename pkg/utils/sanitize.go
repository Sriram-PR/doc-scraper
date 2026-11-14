package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
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

const segmentHashLen = 8

// unsafeSegmentChars are invalid on Windows, or break a Markdown link (# and %).
const unsafeSegmentChars = `<>:"/\|?*#%`

var deviceNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM0": true, "COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT0": true, "LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	"COM¹": true, "COM²": true, "COM³": true, "LPT¹": true, "LPT²": true, "LPT³": true,
}

// isDeviceName reports whether Windows treats name as a device, which it does
// whatever extension follows the first dot.
func isDeviceName(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	return deviceNames[strings.ToUpper(base)]
}

func unsafeSegmentRune(r rune) bool {
	return strings.ContainsRune(unsafeSegmentChars, r) || unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r)
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// SafeSegment turns one decoded URL path segment into a file or directory
// name: a segment that is already a portable name is kept, any other gets
// HashedSegment, so two different segments never share a name.
func SafeSegment(seg string) string {
	if segmentIsSafe(seg) {
		return seg
	}
	return HashedSegment(seg)
}

// HashedSegment cleans seg and appends a short hash of the original.
func HashedSegment(seg string) string {
	var b strings.Builder
	for _, r := range seg {
		if r == utf8.RuneError || unsafeSegmentRune(r) {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	cleaned := strings.TrimRight(b.String(), ". ")
	if isDeviceName(cleaned) {
		cleaned = "_" + cleaned
	}
	cleaned = strings.TrimRight(truncateUTF8(cleaned, maxFilenameLength-1-segmentHashLen), ". ")
	sum := sha256.Sum256([]byte(seg))
	return cleaned + "_" + hex.EncodeToString(sum[:])[:segmentHashLen]
}

func segmentIsSafe(seg string) bool {
	if seg == "" || len(seg) > maxFilenameLength || !utf8.ValidString(seg) {
		return false
	}
	if strings.Trim(seg, ".") == "" || strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
		return false
	}
	for _, r := range seg {
		if unsafeSegmentRune(r) {
			return false
		}
	}
	return !isDeviceName(seg)
}
