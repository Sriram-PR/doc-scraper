package mcp

import "github.com/Sriram-PR/doc-scraper/v2/pkg/config"

// site returns key's entry. save_site swaps entries rather than mutating
// them, so the pointer stays valid for a crawl already using it.
func (s *Server) site(key string) (*config.SiteConfig, bool) {
	s.sitesMu.RLock()
	defer s.sitesMu.RUnlock()
	c, ok := s.cfg.AppConfig.Sites[key]
	return c, ok
}

func (s *Server) siteKeys() []string {
	s.sitesMu.RLock()
	defer s.sitesMu.RUnlock()
	return config.GetAllSiteKeys(s.cfg.AppConfig)
}

func (s *Server) putSite(key string, c *config.SiteConfig) {
	s.sitesMu.Lock()
	defer s.sitesMu.Unlock()
	if s.cfg.AppConfig.Sites == nil {
		s.cfg.AppConfig.Sites = map[string]*config.SiteConfig{}
	}
	s.cfg.AppConfig.Sites[key] = c
}
