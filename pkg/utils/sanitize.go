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

// SanitizeFilename cleans a string to be safe for use as a filename component.
// Unlike SafeSegment it adds no hash, so site keys keep stable paths.
func SanitizeFilename(name string) string {
	sanitized := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			return '_'
		}
		return r
	}, name)
	sanitized = invalidFilenameChars.ReplaceAllString(sanitized, "_")
	sanitized = consecutiveUnderscores.ReplaceAllString(sanitized, "_")
	sanitized = trimFilename(sanitized)
	if len(sanitized) > maxFilenameLength {
		sanitized = trimFilename(truncateUTF8(sanitized, maxFilenameLength))
	}

	// Segments of only dots would resolve to the current or parent directory.
	if sanitized == "" || strings.Trim(sanitized, ".") == "" {
		sanitized = "untitled"
	}
	if isDeviceName(sanitized) {
		sanitized = "_" + strings.TrimRight(truncateUTF8(sanitized, maxFilenameLength-1), "_ .")
	}
	return sanitized
}

// trimFilename drops leading underscores and spaces, and trailing ones plus
// dots, which Windows strips from names.
func trimFilename(s string) string {
	return strings.TrimRight(strings.TrimLeft(s, "_ "), "_ .")
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

// SafeSegment returns a decoded URL path segment unchanged when it is a portable
// file name and HashedSegment(seg) otherwise, so different segments don't collide.
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
