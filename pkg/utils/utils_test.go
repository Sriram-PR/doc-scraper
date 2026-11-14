package utils

import (
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			assert.Equal(t, tt.expected, SanitizeFilename(tt.input), "input %q", tt.input)
		})
	}
}

func TestIsWithinDir(t *testing.T) {
	sep := string(filepath.Separator)
	dir := filepath.FromSlash("/out/site")
	tests := []struct {
		name string
		dir  string
		path string
		want bool
	}{
		{"Child", dir, filepath.FromSlash("/out/site/page.md"), true},
		{"Nested", dir, filepath.FromSlash("/out/site/a/b/page.md"), true},
		{"DirWithTrailingSeparator", dir + sep, filepath.FromSlash("/out/site/page.md"), true},
		{"UncleanedChild", dir, filepath.FromSlash("/out/site/a/../page.md"), true},
		{"UncleanedDir", filepath.FromSlash("/out/x/../site"), filepath.FromSlash("/out/site/page.md"), true},
		{"DirItself", dir, dir, false},
		{"DirItselfUncleaned", dir, filepath.FromSlash("/out/site/a/.."), false},
		{"Parent", dir, filepath.FromSlash("/out"), false},
		{"Sibling", dir, filepath.FromSlash("/out/other/page.md"), false},
		{"SiblingSharingPrefix", dir, filepath.FromSlash("/out/site-evil/page.md"), false},
		{"EscapeViaDotDot", dir, filepath.FromSlash("/out/site/../../etc/passwd"), false},
		{"UnderRoot", sep, filepath.FromSlash("/etc/passwd"), true},
		{"RootItself", sep, sep, false},
		{"RelativeChild", "out", filepath.FromSlash("out/site"), true},
		{"RelativeEscape", "out", filepath.FromSlash("out/../site"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsWithinDir(tt.dir, tt.path), "IsWithinDir(%q, %q)", tt.dir, tt.path)
		})
	}
}

func TestSanitizeFilename_LongNames(t *testing.T) {
	// Create a string longer than maxFilenameLength (100)
	longName := ""
	for range 150 {
		longName += "a"
	}

	assert.LessOrEqual(t, len(SanitizeFilename(longName)), 100)
}

func TestCompileRegexPatterns_ValidPatterns(t *testing.T) {
	patterns := []string{
		`^/docs/.*`,
		`\.html$`,
		`[a-z]+`,
	}

	compiled, err := CompileRegexPatterns(patterns)
	require.NoError(t, err)
	assert.Len(t, compiled, 3)
}

func TestCompileRegexPatterns_EmptySlice(t *testing.T) {
	compiled, err := CompileRegexPatterns([]string{})
	require.NoError(t, err)
	assert.Empty(t, compiled)
}

func TestCompileRegexPatterns_EmptyStringsSkipped(t *testing.T) {
	patterns := []string{"valid", "", "also_valid", ""}

	compiled, err := CompileRegexPatterns(patterns)
	require.NoError(t, err)
	assert.Len(t, compiled, 2)
}

func TestCompileRegexPatterns_InvalidPattern(t *testing.T) {
	patterns := []string{
		`valid`,
		`[invalid`, // Unclosed bracket
	}

	_, err := CompileRegexPatterns(patterns)
	require.ErrorIs(t, err, ErrConfigValidation)
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
			assert.Equal(t, tt.expected, CalculateStringSHA256(tt.input))
		})
	}
}

func TestSyncDir(t *testing.T) {
	require.NoError(t, SyncDir(t.TempDir()))
	err := SyncDir(filepath.Join(t.TempDir(), "missing"))
	if runtime.GOOS == "windows" {
		assert.NoError(t, err, "a no-op on Windows")
		return
	}
	assert.Error(t, err)
}

var hashedName = regexp.MustCompile(`_[0-9a-f]{8}$`)

func TestSafeSegment_KeepsSafeNames(t *testing.T) {
	for _, s := range []string{"_modules", "__init__", "3.0.1", "fastapi.HTTPException", "index", "guide", "日本語", "a b", ".hidden", "console", "con_tents"} {
		assert.Equal(t, s, SafeSegment(s), "%q", s)
	}
}

func TestSafeSegment_HashesUnsafeNames(t *testing.T) {
	for _, s := range []string{
		"a<b", "a>b", "a:b", `a"b`, "a/b", `a\b`, "a|b", "a?b", "a*b", "a#b", "a%b",
		"a\x01b", "a\x7fb", "a\u0085b", "a\u202eb", "a\u200bb", "a\ufeffb",
		"name.", "name ", "", ".", "..", "...",
		"CON", "con.txt", "Nul", "COM1", "lpt9.md", "COM¹",
		"a\xffb", strings.Repeat("a", 101),
	} {
		got := SafeSegment(s)
		assert.Regexp(t, hashedName, got, "%q", s)
		assert.True(t, utf8.ValidString(got), "%q gave invalid UTF-8 %q", s, got)
		assert.LessOrEqual(t, len(got), 100, "%q", s)
		assert.Equal(t, got, SafeSegment(got), "the name made for %q must itself be safe", s)
	}
}

func TestSafeSegment_DistinctInputsGiveDistinctNames(t *testing.T) {
	long := strings.Repeat("x", 120)
	inputs := []string{"a_b", "a:b", "a#b", "a?b", "CON", "con", "..", "...", long + strings.Repeat("1", 30), long + strings.Repeat("2", 30)}
	seen := map[string]string{}
	for _, in := range inputs {
		out := SafeSegment(in)
		if prev, ok := seen[out]; ok {
			t.Errorf("%q and %q both give %q", prev, in, out)
		}
		seen[out] = in
	}
}

func TestSafeSegment_TruncatesOnACharacterBoundary(t *testing.T) {
	got := SafeSegment(strings.Repeat("a", 99) + "é")
	assert.True(t, utf8.ValidString(got))
	assert.LessOrEqual(t, len(got), 100)

	// é occupies bytes 90-91, so a 91-byte cut would split it.
	got = SafeSegment(strings.Repeat("a", 90) + "é" + strings.Repeat("b", 10))
	assert.True(t, utf8.ValidString(got))
	assert.Equal(t, strings.Repeat("a", 90)+"_", got[:91])
}

func TestSafeSegment_DeviceNameStaysPrefixedAfterCleaning(t *testing.T) {
	got := HashedSegment("con.txt")
	assert.True(t, strings.HasPrefix(got, "_con.txt_"), "got %q", got)
	assert.Regexp(t, `^_[0-9a-f]{8}$`, HashedSegment(".."), "an empty cleaned name is just the hash")
}
