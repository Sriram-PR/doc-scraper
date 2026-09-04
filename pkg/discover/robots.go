package discover

import (
	"bufio"
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/temoto/robotstxt"
)

// RobotsInfo summarizes a site's robots.txt: declared sitemaps, path-shaped
// disallow rules worth folding into the draft, whether the seed itself is off
// limits, and any AI-crawler restrictions the user should know about before
// crawling someone's docs.
type RobotsInfo struct {
	Sitemaps       []string
	Disallows      []string
	SeedDisallowed bool
	AIRestricted   bool
	AINote         string
}

var aiCrawlerAgents = []string{"gptbot", "claudebot", "claude-web", "anthropic-ai", "ccbot", "google-extended", "perplexitybot", "bytespider"}

func (d *Discoverer) fetchRobots(ctx context.Context, seed *url.URL) RobotsInfo {
	var info RobotsInfo
	robotsURL := seed.Scheme + "://" + seed.Host + "/robots.txt"
	body, status, err := d.get(ctx, robotsURL)
	if err != nil || status != http.StatusOK {
		return info
	}

	if data, perr := robotstxt.FromBytes(body); perr == nil {
		agent := d.UserAgent
		if agent == "" {
			agent = "doc-scraper"
		}
		info.SeedDisallowed = !data.TestAgent(seed.Path, agent)
	}
	parseRobotsLines(string(body), &info)
	return info
}

func parseRobotsLines(body string, info *RobotsInfo) {
	var inGenericGroup, inAIGroup bool
	var restrictedAgents []string
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "sitemap":
			if value != "" {
				info.Sitemaps = append(info.Sitemaps, value)
			}
		case "user-agent":
			agent := strings.ToLower(value)
			inGenericGroup = agent == "*"
			inAIGroup = false
			for _, ai := range aiCrawlerAgents {
				if agent == ai {
					inAIGroup = true
					restrictedAgents = append(restrictedAgents, value)
				}
			}
		case "disallow":
			if inGenericGroup && value != "" && value != "/" && strings.HasPrefix(value, "/") {
				info.Disallows = append(info.Disallows, value)
			}
			if inAIGroup && (value == "/" || value == "") {
				info.AIRestricted = value == "/"
			}
		case "content-signal":
			if strings.Contains(strings.ReplaceAll(strings.ToLower(value), " ", ""), "ai-input=no") {
				info.AIRestricted = true
				info.AINote = "robots.txt sets Content-Signal ai-input=no"
			}
		}
	}
	if info.AIRestricted && info.AINote == "" {
		info.AINote = "robots.txt blocks AI crawlers (" + strings.Join(restrictedAgents, ", ") + ")"
	}
}
