package chunk

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzSplit codifies the invariants established by the pre-release chunker
// verification: every chunk stays under the hard size cap, cuts land on rune
// boundaries, sequence numbers are strictly ordered, and no input panics.
func FuzzSplit(f *testing.F) {
	f.Add("# Title\n\nBody text.\n\n## Section\n\nMore text.\n")
	f.Add("```go\nfunc main() {}\n```\n")
	f.Add("## foo##\n\n#### deep ###\n\ncontent\n")
	f.Add("    # indented, not a heading\n\n   # this one is\n")
	f.Add(strings.Repeat("x", 20000))
	f.Add(strings.Repeat("word ", 4000))
	f.Add("# A\r\n\r\n```py\r\n" + strings.Repeat("print(1)\r\n", 400) + "```\r\n")
	f.Add("# é世界" + strings.Repeat("é", 8000))
	f.Add("# H\n\n~~~\nfence\n~~~ info trailing\n")
	f.Add("#\n##\n###\n")
	f.Fuzz(func(t *testing.T, markdown string) {
		chunks := Split(markdown)
		lastSeq := -1
		for _, c := range chunks {
			if len(c.Text) > hardMaxBytes {
				t.Fatalf("chunk of %d bytes exceeds hard cap %d", len(c.Text), hardMaxBytes)
			}
			if utf8.ValidString(markdown) && !utf8.ValidString(c.Text) {
				t.Fatal("chunk broke a rune boundary")
			}
			if strings.TrimSpace(c.Text) == "" {
				t.Fatal("empty chunk emitted")
			}
			if c.Seq <= lastSeq {
				t.Fatalf("seq %d not increasing after %d", c.Seq, lastSeq)
			}
			lastSeq = c.Seq
		}
	})
}
