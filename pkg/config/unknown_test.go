package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
	assert.Equal(t, want, got)
}

func TestUnknownKeys_CleanAndMalformed(t *testing.T) {
	assert.Empty(t, UnknownKeys([]byte("num_workers: 2\nsites: {}\n")))
	assert.Nil(t, UnknownKeys([]byte("a: [\n")), "malformed yaml should yield nil")
	assert.Nil(t, UnknownKeys(nil))
}
