package config

import (
	"reflect"
	"testing"
)

func TestUnknownKeys(t *testing.T) {
	yml := `
num_workerz: 9
num_workers: 2
http_client_settings:
  timeout: 5s
  bogus: 1
sites:
  a:
    start_urls: [https://x.test/]
    contnt_selector: main
  b:
    max_depth: 1
`
	got := UnknownKeys([]byte(yml))
	want := []string{
		"line 2: unknown key \"num_workerz\"",
		"line 6: unknown key \"http_client_settings.bogus\"",
		"line 10: unknown key \"sites.a.contnt_selector\"",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestUnknownKeys_CleanAndMalformed(t *testing.T) {
	if got := UnknownKeys([]byte("num_workers: 2\nsites: {}\n")); len(got) != 0 {
		t.Fatalf("unexpected: %q", got)
	}
	if got := UnknownKeys([]byte("a: [\n")); got != nil {
		t.Fatalf("malformed yaml should yield nil, got %q", got)
	}
	if got := UnknownKeys(nil); got != nil {
		t.Fatalf("empty: %q", got)
	}
}
