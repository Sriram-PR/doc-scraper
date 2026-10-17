package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const runFileVersion = 1

type corpusInfo struct {
	Site        string      `json:"site"`
	Fingerprint fingerprint `json:"fingerprint"`
}

type runFile struct {
	Version   int           `json:"version"`
	Created   string        `json:"created"`
	GitCommit string        `json:"git_commit"`
	Corpora   []corpusInfo  `json:"corpora"`
	Queries   []queryResult `json:"queries"`
}

func writeRun(path string, r runFile) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func readRun(path string) (runFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return runFile{}, err
	}
	var r runFile
	if err := json.Unmarshal(data, &r); err != nil {
		return runFile{}, fmt.Errorf("%s: %w", path, err)
	}
	if r.Version != runFileVersion {
		return runFile{}, fmt.Errorf("%s: run file version %d, want %d", path, r.Version, runFileVersion)
	}
	return r, nil
}

// gitCommit returns HEAD, suffixed -dirty for uncommitted changes, or "" when
// git is unavailable.
func gitCommit(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	commit := strings.TrimSpace(string(out))
	if st, err := exec.CommandContext(ctx, "git", "status", "--porcelain").Output(); err == nil && len(strings.TrimSpace(string(st))) > 0 {
		commit += "-dirty"
	}
	return commit
}

func formatMean(m metricMean) string {
	if m.n == 0 {
		return "-"
	}
	return fmt.Sprintf("%.3f", m.mean)
}

func printScorecard(w io.Writer, rs []queryResult, verbose bool) {
	fmt.Fprintf(w, "%-20s %4s", "group", "n")
	for _, def := range metricDefs {
		fmt.Fprintf(w, " %8s", def.name)
	}
	fmt.Fprintf(w, " %6s\n", "zero")
	for _, g := range groupResults(rs) {
		s := summarize(g.results)
		fmt.Fprintf(w, "%-20s %4d", g.name, s.n)
		for _, m := range s.metrics {
			fmt.Fprintf(w, " %8s", formatMean(m))
		}
		fmt.Fprintf(w, " %6d\n", s.zero)
	}

	sorted := slices.Clone(rs)
	slices.SortFunc(sorted, func(a, b queryResult) int {
		return cmp.Or(cmp.Compare(a.Site, b.Site), cmp.Compare(a.ID, b.ID))
	})
	if verbose {
		fmt.Fprintln(w)
		for _, r := range sorted {
			fmt.Fprintf(w, "%s %s page=%d chunk=%d section=%d zero=%t %q\n", r.Site, r.ID, r.PageRank, r.ChunkRank, r.SectionRank, r.Zero, r.Query)
		}
	}

	var misses []queryResult
	for _, r := range sorted {
		if within(r.PageRank, 10) == 0 {
			misses = append(misses, r)
		}
	}
	fmt.Fprintf(w, "\npage misses at 10: %d\n", len(misses))
	for _, r := range misses {
		top := "(no results)"
		if len(r.TopURLs) > 0 {
			top = strings.Join(r.TopURLs[:min(3, len(r.TopURLs))], ", ")
		}
		fmt.Fprintf(w, "  %s %s %q -> %s\n", r.Site, r.ID, r.Query, top)
	}
}
