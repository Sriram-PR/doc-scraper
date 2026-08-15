// Package taskspec parses a JSON task description used by the `doc-scraper run`
// subcommand. It exists so orchestration agents can drive doc-scraper as a
// subprocess by piping a single JSON object instead of constructing shell
// arguments.
package taskspec

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/watch"
)

// Command is the verb the task spec dispatches to.
type Command string

const (
	CommandCrawl Command = "crawl"
	CommandWatch Command = "watch"
)

// TaskSpec mirrors the CLI flag surface of `crawl` and `watch` in a JSON form.
// Field names are snake_case so an agent can build the JSON object naturally.
type TaskSpec struct {
	Command     Command  `json:"command"`
	Config      string   `json:"config,omitempty"`
	Site        string   `json:"site,omitempty"`
	Sites       []string `json:"sites,omitempty"`
	AllSites    bool     `json:"all_sites,omitempty"`
	Resume      bool     `json:"resume,omitempty"`
	Incremental bool     `json:"incremental,omitempty"`
	Full        bool     `json:"full,omitempty"`
	Interval    string   `json:"interval,omitempty"`
	Loglevel    string   `json:"loglevel,omitempty"`
	JSONLogs    bool     `json:"json_logs,omitempty"`
	Pprof       string   `json:"pprof,omitempty"`
}

// Parse decodes a TaskSpec from r. Strict-mode: unknown JSON fields are
// rejected so typos like `"sit": "x"` fail loudly instead of silently
// running with the default site selection.
func Parse(r io.Reader) (*TaskSpec, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var spec TaskSpec
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("parse task spec: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("parse task spec: extra JSON content after first object")
	}
	spec.applyDefaults()
	return &spec, nil
}

func (s *TaskSpec) applyDefaults() {
	if s.Config == "" {
		s.Config = "config.yaml"
	}
	if s.Loglevel == "" {
		s.Loglevel = "info"
	}
	if s.Command == CommandWatch && s.Interval == "" {
		s.Interval = "24h"
	}
}

// Validate enforces field constraints that JSON shape cannot express:
// supported command, mutually exclusive flags, exactly one site selection,
// well-formed interval for watch.
func (s *TaskSpec) Validate() error {
	switch s.Command {
	case CommandCrawl, CommandWatch:
	case "":
		return fmt.Errorf("command is required (one of: crawl, watch)")
	default:
		return fmt.Errorf("unknown command %q (expected one of: crawl, watch)", s.Command)
	}

	selectorCount := 0
	if s.Site != "" {
		selectorCount++
	}
	if len(s.Sites) > 0 {
		selectorCount++
	}
	if s.AllSites {
		selectorCount++
	}
	switch selectorCount {
	case 0:
		return fmt.Errorf("one of site, sites, or all_sites is required")
	case 1:
	default:
		return fmt.Errorf("site, sites, and all_sites are mutually exclusive; provide exactly one")
	}

	for i, k := range s.Sites {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("sites[%d] is empty", i)
		}
	}

	if s.Command == CommandCrawl {
		if s.Incremental && s.Full {
			return fmt.Errorf("incremental and full are mutually exclusive")
		}
	}

	if s.Command == CommandWatch {
		if _, err := watch.ParseInterval(s.Interval); err != nil {
			return fmt.Errorf("invalid interval %q: %w", s.Interval, err)
		}
		if s.Resume || s.Incremental || s.Full || s.Pprof != "" {
			return fmt.Errorf("resume, incremental, full, and pprof are not valid for watch (watch is always incremental)")
		}
	}

	return nil
}

// SiteKeys returns the resolved site selection: empty slice signals
// "all sites" (the caller is expected to expand it from the loaded config).
func (s *TaskSpec) SiteKeys() []string {
	if s.AllSites {
		return nil
	}
	if len(s.Sites) > 0 {
		out := make([]string, 0, len(s.Sites))
		for _, k := range s.Sites {
			k = strings.TrimSpace(k)
			if k != "" {
				out = append(out, k)
			}
		}
		return out
	}
	return []string{s.Site}
}
