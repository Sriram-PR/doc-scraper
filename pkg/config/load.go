package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

// Load reads the YAML config at path. It also returns a warning per YAML key
// that maps to no config field; loading itself stays lenient so existing
// configs keep working. Defaults are applied by Validate, not here.
func Load(path string) (*AppConfig, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	}
	var cfg AppConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}
	// A site key with nothing under it (b: or b: null) parses to a nil entry,
	// which every command would otherwise dereference.
	var errs []error
	for _, key := range slices.Sorted(maps.Keys(cfg.Sites)) {
		if cfg.Sites[key] == nil {
			errs = append(errs, fmt.Errorf("%w: site '%s' has no settings", utils.ErrConfigValidation, key))
		}
	}
	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}
	return &cfg, UnknownKeys(data), nil
}
