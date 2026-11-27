package config

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
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
			return fmt.Errorf("site '%s' not found. Available sites: %s", key, strings.Join(GetAllSiteKeys(appCfg), ", "))
		}
	}
	return nil
}

// reservedDirSuffixes name the directories a crawl keeps beside a site's own.
var reservedDirSuffixes = []string{utils.StagingSuffix, ".old"}

// checkSiteKeys rejects keys whose output and state paths would be shared with
// another key's or with doc-scraper's own staging and backup directories.
func checkSiteKeys(keys []string) []error {
	var errs []error
	seen := map[string]string{} // lowercased directory name -> first key
	for _, key := range slices.Sorted(slices.Values(keys)) {
		if !strings.ContainsFunc(key, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			errs = append(errs, fmt.Errorf("%w: site key '%s' needs at least one letter or digit", utils.ErrConfigValidation, key))
			continue
		}
		dir := utils.SanitizeFilename(key)
		folded := strings.ToLower(dir)
		if i := slices.IndexFunc(reservedDirSuffixes, func(suf string) bool { return strings.HasSuffix(folded, suf) }); i >= 0 {
			errs = append(errs, fmt.Errorf("%w: site key '%s' ends in %s, which doc-scraper uses for its own directories; rename it",
				utils.ErrConfigValidation, key, reservedDirSuffixes[i]))
			continue
		}
		first, dup := seen[folded]
		if !dup {
			seen[folded] = key
			continue
		}
		where := ""
		if utils.SanitizeFilename(first) != dir {
			where = " on case-insensitive filesystems"
		}
		errs = append(errs, fmt.Errorf("%w: site keys '%s' and '%s' share the directory '%s'%s; rename one",
			utils.ErrConfigValidation, first, key, dir, where))
	}
	return errs
}
