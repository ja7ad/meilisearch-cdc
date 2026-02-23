//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/web"
)

func TestProgressiveWebApp(t *testing.T) {
	e := newEnv(t)
	get := func(path string) (*http.Response, string) {
		resp, err := http.Get(e.srv.URL + path)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	resp, body := get("/sw.js")
	require.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "javascript")
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "/", resp.Header.Get("Service-Worker-Allowed"))
	assert.NotContains(t, body, "__BUILD__")
	assert.Contains(t, body, "const BUILD = '"+web.Build()+"'")
	assert.Len(t, web.Build(), 12)

	resp, body = get("/manifest.webmanifest")
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "application/manifest+json", resp.Header.Get("Content-Type"))
	var m struct {
		Name, StartURL, Display string
		Icons                   []struct{ Src, Sizes, Purpose string }
	}
	require.NoError(t, json.Unmarshal([]byte(strings.NewReplacer(`"start_url"`, `"startURL"`).Replace(body)), &m))
	assert.Equal(t, "Meili CDC", m.Name)
	assert.Equal(t, "standalone", m.Display)
	assert.Equal(t, "./#/overview", m.StartURL)
	for _, ic := range m.Icons {
		r, _ := get("/" + ic.Src)
		assert.Equal(t, 200, r.StatusCode, ic.Src)
	}

	resp, body = get("/")
	assert.Contains(t, body, `rel="manifest"`)
	assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "worker-src 'self'")
}
