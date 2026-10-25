package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

type saveSiteResult struct {
	SiteKey     string `json:"site_key"`
	Written     bool   `json:"written"`
	Replaced    bool   `json:"replaced"`
	NextActions string `json:"next_actions"`
}

func (s *Server) handleSaveSite(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if errResult := checkArgTypes(request, map[string]argKind{"site_key": argString, "entry": argObject, "replace": argBool}); errResult != nil {
		return errResult, nil
	}
	if !s.cfg.AllowConfigWrites {
		return mcp.NewToolResultError("config writes are disabled; restart mcp-server with -allow-config-writes to let save_site write the config file"), nil
	}
	siteKey := request.GetString("site_key", "")
	if siteKey == "" {
		return mcp.NewToolResultError("site_key parameter is required"), nil
	}
	fields, ok := request.GetArguments()["entry"].(map[string]any)
	if !ok {
		return mcp.NewToolResultError("entry parameter is required: a site entry object, such as draft_site's entry"), nil
	}
	replace := request.GetBool("replace", false)

	prev, exists := s.site(siteKey)
	// The JSONL fields can't be set here, so a replace keeps the entry's own.
	var base config.SiteConfig
	if exists {
		base.EnableJSONLOutput = prev.EnableJSONLOutput
		base.JSONLOutputFilename = prev.JSONLOutputFilename
	}
	site, err := config.MergeSiteFields(base, fields)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	// Validate normalizes its receiver; the entry is written as given, like add does.
	check := site
	if _, err := check.Validate(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid site entry: %v", err)), nil
	}
	switch {
	case exists && !replace:
		return mcp.NewToolResultError(fmt.Sprintf("site '%s' exists; pass replace: true to overwrite it", clipInput(siteKey))), nil
	case !exists && replace:
		return mcp.NewToolResultError(fmt.Sprintf("site '%s' doesn't exist; omit replace to add it", clipInput(siteKey))), nil
	}

	release, err := s.jobManager.ReserveSite(siteKey)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer release()
	// The reservation is per site; saves of different sites still share the file.
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	if err := config.UpsertSite(s.cfg.ConfigPath, siteKey, &site, replace); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("config not written: %v", err)), nil
	}
	// Reload from disk so the running server holds exactly what a restart would load.
	loaded, _, err := config.Load(s.cfg.ConfigPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("config written, but reloading it failed: %v", err)), nil
	}
	saved, ok := loaded.Sites[siteKey]
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("config written, but site '%s' is missing after reload", clipInput(siteKey))), nil
	}
	var before config.SiteConfig
	if exists {
		before = *prev
	}
	s.putSite(siteKey, saved)
	s.log.Info("Saved site entry over MCP", "site_key", siteKey, "replaced", exists,
		"changed_fields", config.ChangedSiteFields(before, *saved), "config_path", s.cfg.ConfigPath)

	return structuredResult(saveSiteResult{
		SiteKey:     siteKey,
		Written:     true,
		Replaced:    exists,
		NextActions: "Call crawl_site with this site_key to crawl it.",
	}), nil
}
