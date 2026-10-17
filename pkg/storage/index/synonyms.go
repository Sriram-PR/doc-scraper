package index

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// synonymGroups lists interchangeable terms for the synonym fill stages of
// SearchChunks. Entries are lowercase words or phrases of up to three words.
var synonymGroups = [][]string{}

var synonymIndex = buildSynonymIndex(synonymGroups)

const (
	maxSynonymWords = 3
	maxSynonymGroup = 5
)

func buildSynonymIndex(groups [][]string) map[string][]string {
	idx := make(map[string][]string)
	for _, g := range groups {
		for _, k := range g {
			for _, v := range g {
				if v != k {
					idx[k] = append(idx[k], v)
				}
			}
		}
	}
	return idx
}

type termGroup struct {
	text string
	alts []string
}

// expandTerms splits query words into units, joining the longest run of
// words that is a synonym entry, and attaches each unit's synonyms.
func expandTerms(words []string) []termGroup {
	var out []termGroup
	for i := 0; i < len(words); {
		n, alts := matchSynonym(words[i:])
		out = append(out, termGroup{text: strings.Join(words[i:i+n], " "), alts: alts})
		i += n
	}
	return out
}

func matchSynonym(words []string) (int, []string) {
	for n := min(maxSynonymWords, len(words)); n >= 2; n-- {
		if alts, ok := synonymIndex[strings.ToLower(strings.Join(words[:n], " "))]; ok {
			return n, alts
		}
	}
	w := strings.ToLower(words[0])
	if alts, ok := synonymIndex[w]; ok {
		return 1, alts
	}
	// The porter tokenizer matches inflections, but lookup is on the surface
	// form, so a plural query word still finds its singular entry.
	if singular, ok := strings.CutSuffix(w, "s"); ok {
		return 1, synonymIndex[singular]
	}
	return 1, nil
}

var synonymEntry = regexp.MustCompile(`^[a-z0-9_-]+( [a-z0-9_-]+)*$`)

func synonymListProblems(groups [][]string) []string {
	var problems []string
	seen := make(map[string]int)
	for gi, g := range groups {
		if len(g) < 2 || len(g) > maxSynonymGroup {
			problems = append(problems, fmt.Sprintf("group %d: %d entries, want 2 to %d", gi, len(g), maxSynonymGroup))
		}
		for _, e := range g {
			words := strings.Fields(e)
			switch {
			case !synonymEntry.MatchString(e):
				problems = append(problems, fmt.Sprintf("group %d: %q is not lowercase words separated by single spaces", gi, e))
			case len(words) > maxSynonymWords:
				problems = append(problems, fmt.Sprintf("group %d: %q has more than %d words", gi, e, maxSynonymWords))
			case !slices.ContainsFunc(words, func(w string) bool { _, stop := ftsStopwords[w]; return !stop }):
				problems = append(problems, fmt.Sprintf("group %d: %q is only stopwords", gi, e))
			}
			if prev, ok := seen[e]; ok {
				problems = append(problems, fmt.Sprintf("group %d: %q is also in group %d", gi, e, prev))
			}
			seen[e] = gi
		}
	}
	return problems
}
