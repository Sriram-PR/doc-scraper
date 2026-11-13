package mcp

import (
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

type headerLine struct{ key, value string }

var headerNewlines = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")

// pageTextResult returns page content as one text block with no
// structuredContent: clients that get structuredContent forward only that,
// so the model would read the markdown escaped inside JSON.
func pageTextResult(header []headerLine, content string) *mcp.CallToolResult {
	var b strings.Builder
	for _, h := range header {
		b.WriteString(h.key)
		b.WriteString(": ")
		b.WriteString(headerNewlines.Replace(h.value))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(content)
	return mcp.NewToolResultText(b.String())
}

func contentLine(start, end, total int, truncated bool) headerLine {
	v := fmt.Sprintf("bytes %d-%d of %d", start, end, total)
	if truncated {
		v += fmt.Sprintf(" (truncated; next_offset: %d)", end)
	}
	return headerLine{"content", v}
}
