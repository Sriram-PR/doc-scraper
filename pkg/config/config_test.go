package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func boolPtr(b bool) *bool {
	return &b
}

func TestGetEffectiveUserAgent(t *testing.T) {
	tests := []struct {
		name     string
		siteCfg  SiteConfig
		appCfg   AppConfig
		expected string
	}{
		{
			name:     "site UA overrides global",
			siteCfg:  SiteConfig{UserAgent: "SiteBot/1.0"},
			appCfg:   AppConfig{DefaultUserAgent: "GlobalBot/1.0"},
			expected: "SiteBot/1.0",
		},
		{
			name:     "empty site UA falls back to global",
			siteCfg:  SiteConfig{UserAgent: ""},
			appCfg:   AppConfig{DefaultUserAgent: "GlobalBot/1.0"},
			expected: "GlobalBot/1.0",
		},
		{
			name:     "both empty returns empty",
			siteCfg:  SiteConfig{UserAgent: ""},
			appCfg:   AppConfig{DefaultUserAgent: ""},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetEffectiveUserAgent(&tt.siteCfg, &tt.appCfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetEffectiveSkipImages(t *testing.T) {
	tests := []struct {
		name     string
		siteCfg  SiteConfig
		appCfg   AppConfig
		expected bool
	}{
		{
			name:     "both unset defaults to skip (opt-in)",
			siteCfg:  SiteConfig{SkipImages: nil},
			appCfg:   AppConfig{SkipImages: nil},
			expected: true,
		},
		{
			name:     "global opt-in (false) used when site unset",
			siteCfg:  SiteConfig{SkipImages: nil},
			appCfg:   AppConfig{SkipImages: boolPtr(false)},
			expected: false,
		},
		{
			name:     "global skip (true) used when site unset",
			siteCfg:  SiteConfig{SkipImages: nil},
			appCfg:   AppConfig{SkipImages: boolPtr(true)},
			expected: true,
		},
		{
			name:     "site opt-in overrides global skip",
			siteCfg:  SiteConfig{SkipImages: boolPtr(false)},
			appCfg:   AppConfig{SkipImages: boolPtr(true)},
			expected: false,
		},
		{
			name:     "site skip overrides global opt-in",
			siteCfg:  SiteConfig{SkipImages: boolPtr(true)},
			appCfg:   AppConfig{SkipImages: boolPtr(false)},
			expected: true,
		},
		{
			name:     "site opt-in overrides unset global default",
			siteCfg:  SiteConfig{SkipImages: boolPtr(false)},
			appCfg:   AppConfig{SkipImages: nil},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetEffectiveSkipImages(&tt.siteCfg, &tt.appCfg)
			assert.Equal(t, tt.expected, result)
		})
	}
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
			result := GetEffectiveEnableJSONLOutput(&tt.siteCfg, &tt.appCfg)
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
			result := GetEffectiveJSONLOutputFilename(&tt.siteCfg, &tt.appCfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}
