package indexmgr

import (
	"encoding/json"
	"testing"

	"github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSettingsRejectsUnknownKeys(t *testing.T) {
	_, err := ParseSettings(json.RawMessage(`{"filterableAttribute":["x"]}`))
	assert.Error(t, err)
	s, err := ParseSettings(json.RawMessage(`{"filterableAttributes":["x"],"searchCutoffMs":150}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"x"}, s.FilterableAttributes)
	_, err = ParseSettings(nil)
	assert.NoError(t, err)
}

func TestDiffSettings(t *testing.T) {
	distinct := "sku"
	live := &meilisearch.Settings{
		FilterableAttributes: []string{"b", "a"},
		SearchableAttributes: []string{"title", "body"},
		DistinctAttribute:    &distinct,
		TypoTolerance:        &meilisearch.TypoTolerance{Enabled: true},
		RankingRules:         []string{"words", "typo"},
	}
	liveJSON, _ := json.Marshal(live)
	changes, err := DiffSettings(liveJSON, json.RawMessage(`{
		"filterableAttributes": ["a","b"],
		"searchableAttributes": ["body","title"],
		"distinctAttribute": "sku",
		"typoTolerance": {"enabled": true},
		"sortableAttributes": [],
		"stopWords": ["the"]
	}`))
	require.NoError(t, err)
	keys := []string{}
	for _, c := range changes {
		keys = append(keys, c.Key)
	}
	// Sets ignore order, ordered lists do not; empty desired equals unset.
	assert.Equal(t, []string{"searchableAttributes", "stopWords"}, keys)
}

func TestShadowNames(t *testing.T) {
	n := ShadowName("products")
	assert.True(t, IsShadow(n))
	assert.False(t, IsShadow("products"))
	assert.Contains(t, n, "products__cdc_shadow_")
}

func TestGranularFilterableAttributes(t *testing.T) {
	p, _, err := parse(json.RawMessage(`{"filterableAttributes":[{"attributePatterns":["genre"],"features":{"facetSearch":true}}],"sortableAttributes":["price"],"facetSearch":false}`), true)
	require.NoError(t, err)
	require.NotNil(t, p.filterable)
	assert.Len(t, *p.filterable, 1)
	assert.Equal(t, []string{"price"}, p.settings.SortableAttributes)
	assert.True(t, p.facetSearchOff)

	p, _, err = parse(json.RawMessage(`{"filterableAttributes":["a","b"]}`), true)
	require.NoError(t, err)
	assert.Nil(t, p.filterable)
	assert.Equal(t, []string{"a", "b"}, p.settings.FilterableAttributes)

	_, _, err = parse(json.RawMessage(`{"filterableAttributes": 3}`), true)
	assert.Error(t, err)
	_, dropped, err := parse(json.RawMessage(`{"futureSetting": 1, "stopWords": ["a"]}`), false)
	require.NoError(t, err)
	assert.Equal(t, []string{"futureSetting"}, dropped)
	_, _, err = parse(json.RawMessage(`{"futureSetting": 1}`), true)
	assert.Error(t, err)
}
