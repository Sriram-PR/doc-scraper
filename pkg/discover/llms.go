package discover

import (
	"bufio"
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// LlmsTxtInfo summarizes an llms.txt file if the site serves one. Only the
// structured markdown links are read; prose in these files can contain
// instructions aimed at AI agents and must never be interpreted.
type LlmsTxtInfo struct {
	Found bool
	Links []string
}

// llmsTxtCorroborationMin is the link count below which an llms.txt is too
// sparse to corroborate scope (hand-curated files often list a dozen pages).
const llmsTxtCorroborationMin = 30

var mdLinkRe = regexp.MustCompile(`\]\((https?://[^)\s]+)\)`)

func (d *Discoverer) fetchLlmsTxt(ctx context.Context, finalURL *url.URL) LlmsTxtInfo {
	var info LlmsTxtInfo
	body, status, err := d.get(ctx, finalURL.Scheme+"://"+finalURL.Host+"/llms.txt")
	if err != nil || status != http.StatusOK {
		return info
	}
	text := string(body)
	if strings.Contains(strings.ToLower(text), "<html") {
		return info
	}
	info.Found = true
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		for _, m := range mdLinkRe.FindAllStringSubmatch(sc.Text(), -1) {
			info.Links = append(info.Links, m[1])
		}
	}
	return info
}
