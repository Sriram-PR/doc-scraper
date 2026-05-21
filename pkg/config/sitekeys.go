package config

import (
	"fmt"
	"slices"
)

// GetAllSiteKeys returns all site keys from the config in sorted order. Sorted
// so callers (e.g. --all-sites crawl ordering, error messages) are
// deterministic across runs; Go map iteration is randomized.
func GetAllSiteKeys(appCfg *AppConfig) []string {
	keys := make([]string, 0, len(appCfg.Sites))
	for k := range appCfg.Sites {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ValidateSiteKeys checks that every key in siteKeys exists in appCfg.Sites.
// On failure the error message lists the available keys in sorted order.
func ValidateSiteKeys(appCfg *AppConfig, siteKeys []string) error {
	for _, key := range siteKeys {
		if _, exists := appCfg.Sites[key]; !exists {
			available := make([]string, 0, len(appCfg.Sites))
			for k := range appCfg.Sites {
				available = append(available, k)
			}
			slices.Sort(available)
			return fmt.Errorf("site '%s' not found. Available sites: %v", key, available)
		}
	}
	return nil
}
