package config

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSiteFieldSchema_DescribesEverySettableField(t *testing.T) {
	props := SiteFieldSchema()
	for name := range yamlFields(reflect.TypeOf(SiteConfig{})) {
		if refusedSiteFields[name] {
			assert.NotContains(t, props, name)
			continue
		}
		require.Contains(t, props, name, "every SiteConfig field needs a schema property and a description")
		p := props[name].(map[string]any)
		assert.NotEmpty(t, p["description"], name)
	}
	assert.Len(t, props, 15)
}

func TestSiteFieldSchema_Types(t *testing.T) {
	props := SiteFieldSchema()
	typ := func(name string) any { return props[name].(map[string]any)["type"] }
	assert.Equal(t, "array", typ("start_urls"))
	assert.Equal(t, map[string]any{"type": "string"}, props["start_urls"].(map[string]any)["items"])
	assert.EqualValues(t, 1, props["start_urls"].(map[string]any)["minItems"])
	assert.Equal(t, "string", typ("allowed_domain"))
	assert.Equal(t, "boolean", typ("respect_nofollow"))
	assert.Equal(t, "boolean", typ("respect_noindex"))
	assert.Equal(t, "boolean", typ("skip_images"))
	assert.Equal(t, "string", typ("delay_per_host"))
	assert.Equal(t, "integer", typ("max_depth"))
	assert.EqualValues(t, 0, props["max_depth"].(map[string]any)["minimum"])
	assert.Equal(t, "integer", typ("max_image_size_bytes"))
	assert.EqualValues(t, 0, props["max_image_size_bytes"].(map[string]any)["minimum"])
}

func TestSiteFieldSchema_MaxDepthIsExclusive(t *testing.T) {
	d := SiteFieldSchema()["max_depth"].(map[string]any)["description"]
	assert.Equal(t, "Exclusive depth bound: start pages are depth 0, so 1 fetches only the start pages and 2 adds the pages they link to. 0 means unlimited", d)
}

func TestSiteFieldSchema_PlainKeywordsOnly(t *testing.T) {
	raw, err := json.Marshal(SiteFieldSchema())
	require.NoError(t, err)
	for _, kw := range []string{`"$schema"`, `"$ref"`, `"oneOf"`, `"additionalProperties"`} {
		assert.NotContains(t, string(raw), kw)
	}
}

func TestSiteFieldSchema_FreshMapEachCall(t *testing.T) {
	a := SiteFieldSchema()
	a["start_urls"].(map[string]any)["description"] = "changed"
	assert.NotEqual(t, "changed", SiteFieldSchema()["start_urls"].(map[string]any)["description"])
}
