package config

import (
	"path/filepath"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

// SiteOutputDir returns a site's output directory, keyed by site key so two site
// configs targeting the same domain stay isolated. Readers (MCP tools) and the
// crawler must derive the path here so they cannot drift.
func (c *AppConfig) SiteOutputDir(siteKey string) string {
	return filepath.Join(c.OutputBaseDir, utils.SanitizeFilename(siteKey))
}
