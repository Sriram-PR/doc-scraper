package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// Exactly one query-type tag per query, so per-type numbers partition the set.
var queryTypeTags = []string{"keyword", "question", "api"}

var otherTags = []string{"mismatch", "drafted"}

type query struct {
	ID       string   `json:"id"`
	Query    string   `json:"query"`
	Tags     []string `json:"tags"`
	Expected []target `json:"expected"`
}

type querySet struct {
	SiteKey string  `json:"site_key"`
	Queries []query `json:"queries"`
}

// loadQueries reads a query file and reports every problem in it at once.
func loadQueries(path string) (querySet, error) {
	f, err := os.Open(path)
	if err != nil {
		return querySet{}, err
	}
	defer func() { _ = f.Close() }()

	var qs querySet
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&qs); err != nil {
		return querySet{}, fmt.Errorf("%s: %w", path, err)
	}

	var errs []error
	if qs.SiteKey == "" {
		errs = append(errs, errors.New("site_key is empty"))
	}
	if len(qs.Queries) == 0 {
		errs = append(errs, errors.New("no queries"))
	}
	ids := make(map[string]bool, len(qs.Queries))
	for i, q := range qs.Queries {
		name := q.ID
		if name == "" {
			name = fmt.Sprintf("queries[%d]", i)
			errs = append(errs, fmt.Errorf("%s: id is empty", name))
		} else if ids[q.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id", name))
		}
		ids[q.ID] = true
		if strings.TrimSpace(q.Query) == "" {
			errs = append(errs, fmt.Errorf("%s: query is empty", name))
		}
		types := 0
		for _, tag := range q.Tags {
			switch {
			case slices.Contains(queryTypeTags, tag):
				types++
			case slices.Contains(otherTags, tag):
			default:
				errs = append(errs, fmt.Errorf("%s: unknown tag %q", name, tag))
			}
		}
		if types != 1 {
			errs = append(errs, fmt.Errorf("%s: needs exactly one of %s, has %d", name, strings.Join(queryTypeTags, "/"), types))
		}
		if len(q.Expected) == 0 {
			errs = append(errs, fmt.Errorf("%s: expected is empty", name))
		}
		for _, t := range q.Expected {
			switch {
			case t.URL == "":
				errs = append(errs, fmt.Errorf("%s: expected url is empty", name))
			case strings.Contains(t.URL, "#"):
				errs = append(errs, fmt.Errorf("%s: expected url %q has a fragment; put it in anchor", name, t.URL))
			}
		}
	}
	if len(errs) > 0 {
		return querySet{}, fmt.Errorf("%s: %w", path, errors.Join(errs...))
	}
	return qs, nil
}

// validate reports every expected URL that is not a page in the corpus.
func (qs querySet) validate(pages map[string]struct{}) error {
	var missing []string
	for _, q := range qs.Queries {
		for _, t := range q.Expected {
			if _, ok := pages[t.URL]; !ok {
				missing = append(missing, q.ID+": "+t.URL)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: %d expected URLs are not pages in the corpus (stale after a recrawl?):\n  %s",
			qs.SiteKey, len(missing), strings.Join(missing, "\n  "))
	}
	return nil
}
