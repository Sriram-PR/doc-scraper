package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/discover"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
)

// draftTimeout matches the CLI add command's discovery budget.
const draftTimeout = 3 * time.Minute

type draftSiteResult struct {
	SiteKey          string           `json:"site_key"`
	Exists           bool             `json:"exists" jsonschema_description:"Whether site_key is configured now; save_site then needs replace: true"`
	Entry            map[string]any   `json:"entry" jsonschema_description:"The site entry by config field name; pass it, changed as needed, to save_site"`
	EntryYAML        string           `json:"entry_yaml"`
	Framework        string           `json:"framework,omitempty"`
	Confidence       string           `json:"confidence,omitempty"`
	SignalSource     string           `json:"signal_source,omitempty"`
	Generator        string           `json:"generator,omitempty"`
	SitemapPageCount int              `json:"sitemap_page_count,omitempty"`
	Evidence         []string         `json:"evidence"`
	Warnings         []string         `json:"warnings"`
	Preview          discover.Preview `json:"preview"`
	ConfigWrites     string           `json:"config_writes" jsonschema_description:"enabled or disabled; save_site needs enabled"`
	NextActions      string           `json:"next_actions"`
}

func (s *Server) configWrites() string {
	if s.cfg.AllowConfigWrites {
		return "enabled"
	}
	return "disabled"
}

func (s *Server) discoverer() *discover.Discoverer {
	ua := s.cfg.AppConfig.DefaultUserAgent
	if ua == "" {
		ua = fetch.DefaultUserAgent()
	}
	return &discover.Discoverer{
		Client:    fetch.NewClient(s.cfg.AppConfig.HTTPClientSettings, s.log),
		UserAgent: ua,
		Log:       s.log,
	}
}

func (s *Server) handleDraftSite(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if errResult := checkArgTypes(request, map[string]argKind{
		"url": argString, "site_key": argString, "preview_url": argString, "overrides": argObject,
	}); errResult != nil {
		return errResult, nil
	}
	req := discover.DraftRequest{
		URL:        request.GetString("url", ""),
		SiteKey:    request.GetString("site_key", ""),
		PreviewURL: request.GetString("preview_url", ""),
	}
	if o, ok := request.GetArguments()["overrides"].(map[string]any); ok {
		req.Overrides = o
	}
	switch {
	case req.URL == "" && req.SiteKey == "":
		return mcp.NewToolResultError("draft_site needs url (a new site) or site_key (a configured site)"), nil
	case req.URL == "":
		base, errResult := s.resolveSiteOrError(req.SiteKey)
		if errResult != nil {
			return errResult, nil
		}
		req.Base = base
	}

	ctx, cancel := context.WithTimeout(ctx, draftTimeout)
	defer cancel()
	res, err := s.discoverer().DraftSite(ctx, req)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("draft failed: %v", err)), nil
	}
	draft := res.Draft
	entry, err := config.SiteFieldMap(&draft.Site)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	entryYAML, err := config.RenderSiteEntry(draft.SiteKey, &draft.Site)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	_, exists := s.site(draft.SiteKey)
	preview := res.Preview
	preview.Markdown = discover.FirstLines(preview.Markdown, 40)

	next := "If the preview looks wrong, call draft_site again with overrides. To keep this entry, call save_site with site_key and entry"
	if exists {
		next += " and replace: true"
	}
	next += ", then crawl_site."
	if !s.cfg.AllowConfigWrites {
		next += " Config writes are disabled on this server: save_site needs mcp-server -allow-config-writes."
	}
	result := draftSiteResult{
		SiteKey:          draft.SiteKey,
		Exists:           exists,
		Entry:            entry,
		EntryYAML:        entryYAML,
		SitemapPageCount: draft.PageCount,
		Generator:        draft.Version,
		Evidence:         nonNilStrings(draft.Evidence),
		Warnings:         nonNilStrings(draft.Warnings),
		Preview:          preview,
		ConfigWrites:     s.configWrites(),
		NextActions:      next,
	}
	if req.URL != "" {
		result.Framework = string(draft.Framework)
		result.Confidence = string(draft.Confidence)
		result.SignalSource = string(draft.Source)
	}
	return structuredResult(result), nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
