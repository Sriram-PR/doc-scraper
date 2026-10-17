package index

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// synonymGroups lists interchangeable terms for the synonym fill stages of
// SearchChunks. Entries are lowercase words or phrases of up to three words.
var synonymGroups = [][]string{
	{"delete", "remove"},
	{"log in", "sign in", "login", "signin"},
	{"log out", "sign out", "logout", "signout"},
	{"sign up", "signup", "register"},
	{"env var", "environment variable", "envvar"},
	{"config", "configuration", "settings", "cfg", "conf"},
	{"configure", "set up", "setup"},
	{"autocomplete", "autocompletion", "completion"},
	{"filename", "file name"},
	{"pathname", "path name"},
	{"dirname", "directory name"},
	{"username", "user name"},
	{"hostname", "host name"},
	{"timestamp", "time stamp"},
	{"timeout", "time out"},
	{"callback", "call back"},
	{"backend", "back end"},
	{"frontend", "front end"},
	{"plugin", "add-on", "addon"},
	{"color", "colour"},
	{"serialize", "serialise", "marshal"},
	{"deserialize", "deserialise", "unmarshal"},
	{"initialize", "initialise", "init"},
	{"customize", "customise"},
	{"normalize", "normalise"},
	{"authorize", "authorise"},
	{"optimize", "optimise"},
	{"behavior", "behaviour"},
	{"cancelled", "canceled"},
	{"repo", "repository"},
	{"auth", "authentication"},
	{"authz", "authorization"},
	{"dir", "directory", "folder"},
	{"dev", "development"},
	{"prod", "production"},
	{"docs", "documentation"},
	{"param", "parameter"},
	{"arg", "argument", "args", "arguments"},
	{"cmd", "command"},
	{"dependency", "dep", "deps", "dependencies"},
	{"lib", "library"},
	{"pkg", "package"},
	{"func", "function"},
	{"var", "variable"},
	{"const", "constant"},
	{"info", "information"},
	{"admin", "administrator"},
	{"app", "application"},
	{"db", "database"},
	{"regex", "regexp", "regular expression"},
	{"stdin", "standard input"},
	{"stdout", "standard output"},
	{"stderr", "standard error"},
	{"tmp", "temp", "temporary"},
	{"impl", "implementation"},
	{"spec", "specification"},
	{"env", "environment"},
	{"msg", "message"},
	{"req", "request"},
	{"res", "response"},
	{"ctx", "context"},
	{"perf", "performance"},
	{"i18n", "internationalization", "localization"},
	{"a11y", "accessibility"},
	{"k8s", "kubernetes"},
	{"js", "javascript"},
	{"postgres", "postgresql"},
	{"mongo", "mongodb"},
	{"run", "execute"},
	{"enable", "turn on", "activate"},
	{"disable", "turn off", "deactivate"},
	{"bug", "defect"},
	{"deprecated", "obsolete"},
	{"retry", "retries", "backoff"},
	{"rate limit", "throttle", "throttling"},
	{"async", "asynchronous"},
	{"sync", "synchronous"},
	{"unit test", "unittest"},
	{"e2e", "end to end", "end-to-end"},
	{"ci", "continuous integration"},
	{"deploy", "deployment"},
	{"format", "formatter", "prettify"},
	{"hot reload", "hot reloading", "live reload"},
	{"cli", "command line", "command-line"},
	{"subcommand", "sub command", "sub-command"},
	{"alias", "shortcut"},
	{"password", "passphrase"},
	{"credentials", "creds"},
	{"ssl", "tls"},
	{"cert", "certificate"},
	{"cors", "cross origin"},
	{"webhook", "web hook"},
	{"websocket", "web socket"},
	{"url", "uri"},
	{"yml", "yaml"},
	{"readme", "read me"},
	{"changelog", "change log", "release notes"},
	{"quickstart", "quick start", "getting started"},
	{"howto", "how-to"},
	{"tutorial", "walkthrough"},
	{"faq", "frequently asked questions"},
	{"boilerplate", "scaffold"},
	{"migrate", "migration"},
	{"override", "overwrite"},
	{"mandatory", "required"},
	{"boolean", "bool"},
	{"integer", "int"},
	{"dictionary", "dict", "hash map"},
}

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
			case !slices.ContainsFunc(words, func(w string) bool { return !isStopword(w) }):
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
