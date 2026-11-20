package chunk

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/*.golden from the current chunker output")

const goldenHeader = "chunk.FormatVersion %d fixture %x\n"

// TestSplitGolden pins Split and IdentifierTokens output for the fixtures, so
// a chunker change that would leave indexed sites stale fails until
// FormatVersion is bumped and the goldens are regenerated with -update.
func TestSplitGolden(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "golden", "*.md"))
	require.NoError(t, err)
	require.NotEmpty(t, fixtures)

	for _, path := range fixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			md, err := os.ReadFile(path)
			require.NoError(t, err)
			md = normalizeEOL(md)
			got := renderGolden(Split(string(md)))
			fixtureSum := sha256.Sum256(md)

			goldenPath := strings.TrimSuffix(path, ".md") + ".golden"
			raw, readErr := os.ReadFile(goldenPath)
			wantVersion, wantSum, want := parseGolden(t, string(normalizeEOL(raw)))

			if *update {
				if readErr == nil && wantVersion == FormatVersion && bytes.Equal(wantSum, fixtureSum[:]) && want != got {
					t.Fatalf("%s: output changed but chunk.FormatVersion is still %d; bump it first so indexed sites re-chunk", goldenPath, FormatVersion)
				}
				header := fmt.Sprintf(goldenHeader, FormatVersion, fixtureSum)
				require.NoError(t, os.WriteFile(goldenPath, []byte(header+got), 0o644))
				return
			}

			require.NoError(t, readErr, "missing golden; run go test ./pkg/chunk -run TestSplitGolden -update")
			if want != got {
				assert.Equal(t, want, got, "chunker output changed: bump chunk.FormatVersion unless only the fixture changed, then run go test ./pkg/chunk -run TestSplitGolden -update")
				return
			}
			assert.Equal(t, FormatVersion, wantVersion, "golden was written for another chunk.FormatVersion; run go test ./pkg/chunk -run TestSplitGolden -update")
		})
	}
}

func renderGolden(chunks []Chunk) string {
	var b strings.Builder
	for _, c := range chunks {
		fmt.Fprintf(&b, "\n=== seq %d\npath: %s\nanchor: %s\nidentifiers: %s\n---\n%s\n", c.Seq, c.HeadingPath, c.Anchor, IdentifierTokens(c.Text), c.Text)
	}
	return b.String()
}

func parseGolden(t *testing.T, raw string) (version int, fixtureSum []byte, body string) {
	t.Helper()
	if raw == "" {
		return 0, nil, ""
	}
	header, body, _ := strings.Cut(raw, "\n")
	if _, err := fmt.Sscanf(header+"\n", goldenHeader, &version, &fixtureSum); err != nil {
		t.Fatalf("golden header %q: %v", header, err)
	}
	return version, fixtureSum, body
}

// normalizeEOL undoes the CRLF conversion of Windows checkouts; the fixtures
// hold no \r of their own.
func normalizeEOL(b []byte) []byte {
	return []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
}
