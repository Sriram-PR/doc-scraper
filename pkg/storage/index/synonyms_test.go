package index

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

var productionSynonymIndex map[string][]string

// Search tests pin exact rewrites and result sets, so they run against an
// empty vocabulary; tests of synonym behaviour install their own groups.
func TestMain(m *testing.M) {
	productionSynonymIndex = synonymIndex
	synonymIndex = nil
	os.Exit(m.Run())
}

func TestProductionSynonymIndexIsBuiltFromTheList(t *testing.T) {
	assert.Equal(t, buildSynonymIndex(synonymGroups), productionSynonymIndex)
}

func withSynonyms(t *testing.T, groups [][]string) {
	t.Helper()
	prev := synonymIndex
	synonymIndex = buildSynonymIndex(groups)
	t.Cleanup(func() { synonymIndex = prev })
}

func TestExpandTerms(t *testing.T) {
	withSynonyms(t, [][]string{
		{"autocomplete", "completion"},
		{"query string", "url parameters"},
		{"query string parameters", "request args"},
		{"boot", "startup"},
	})
	cases := []struct {
		words []string
		want  []termGroup
	}{
		{[]string{"Autocomplete"}, []termGroup{{text: "Autocomplete", alts: []string{"completion"}}}},
		{[]string{"read", "query", "string"}, []termGroup{{text: "read"}, {text: "query string", alts: []string{"url parameters"}}}},
		{[]string{"Query", "String"}, []termGroup{{text: "Query String", alts: []string{"url parameters"}}}},
		{[]string{"query", "string", "parameters"}, []termGroup{{text: "query string parameters", alts: []string{"request args"}}}},
		{[]string{"server", "boots"}, []termGroup{{text: "server"}, {text: "boots", alts: []string{"startup"}}}},
		{[]string{"install"}, []termGroup{{text: "install"}}},
		{nil, nil},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, expandTerms(c.words), c.words)
	}
}

func TestBuildSynonymIndexLinksEveryPairInAGroup(t *testing.T) {
	idx := buildSynonymIndex([][]string{{"a", "b", "c"}})
	assert.Equal(t, map[string][]string{"a": {"b", "c"}, "b": {"a", "c"}, "c": {"a", "b"}}, idx)
}

func TestSynonymListProblems(t *testing.T) {
	cases := map[string][][]string{
		"uppercase":        {{"Login", "sign in"}},
		"syntax character": {{"log*", "sign in"}},
		"quote":            {{`"login"`, "sign in"}},
		"four words":       {{"sign in to the app", "login"}},
		"group of one":     {{"login"}},
		"group of six":     {{"a1", "a2", "a3", "a4", "a5", "a6"}},
		"entry in two":     {{"login", "sign in"}, {"login", "log on"}},
		"stopword entry":   {{"how", "login"}},
		"stopword phrase":  {{"how do i", "login"}},
		"empty entry":      {{"", "login"}},
		"double space":     {{"sign  in", "login"}},
		"duplicate in one": {{"login", "login"}},
		"padded entry":     {{" login", "sign in"}},
	}
	for name, groups := range cases {
		assert.NotEmpty(t, synonymListProblems(groups), name)
	}
	assert.Empty(t, synonymListProblems([][]string{{"login", "sign in", "log-in", "sign_on2"}}))
}

func TestSynonymGroupsAreClean(t *testing.T) {
	assert.Empty(t, synonymListProblems(synonymGroups))
}
