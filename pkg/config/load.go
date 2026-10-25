package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
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
	return &cfg, UnknownKeys(data), nil
}
