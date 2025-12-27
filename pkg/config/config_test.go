package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func boolPtr(b bool) *bool {
	return &b
}

func TestGetEffectiveEnableJSONLOutput(t *testing.T) {
	tests := []struct {
		name     string
		siteCfg  SiteConfig
		appCfg   AppConfig
		expected bool
	}{
		{
			name:     "site enabled overrides global disabled",
			siteCfg:  SiteConfig{EnableJSONLOutput: boolPtr(true)},
			appCfg:   AppConfig{EnableJSONLOutput: false},
			expected: true,
		},
		{
			name:     "site disabled overrides global enabled",
			siteCfg:  SiteConfig{EnableJSONLOutput: boolPtr(false)},
			appCfg:   AppConfig{EnableJSONLOutput: true},
			expected: false,
		},
		{
			name:     "site nil uses global enabled",
			siteCfg:  SiteConfig{EnableJSONLOutput: nil},
			appCfg:   AppConfig{EnableJSONLOutput: true},
			expected: true,
		},
		{
			name:     "site nil uses global disabled",
			siteCfg:  SiteConfig{EnableJSONLOutput: nil},
			appCfg:   AppConfig{EnableJSONLOutput: false},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetEffectiveEnableJSONLOutput(tt.siteCfg, tt.appCfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetEffectiveJSONLOutputFilename(t *testing.T) {
	tests := []struct {
		name     string
		siteCfg  SiteConfig
		appCfg   AppConfig
		expected string
	}{
		{
			name:     "site filename overrides global",
			siteCfg:  SiteConfig{JSONLOutputFilename: "site.jsonl"},
			appCfg:   AppConfig{JSONLOutputFilename: "global.jsonl"},
			expected: "site.jsonl",
		},
		{
			name:     "site empty uses global filename",
			siteCfg:  SiteConfig{JSONLOutputFilename: ""},
			appCfg:   AppConfig{JSONLOutputFilename: "global.jsonl"},
			expected: "global.jsonl",
		},
		{
			name:     "both empty uses default",
			siteCfg:  SiteConfig{JSONLOutputFilename: ""},
			appCfg:   AppConfig{JSONLOutputFilename: ""},
			expected: "pages.jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetEffectiveJSONLOutputFilename(tt.siteCfg, tt.appCfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}
