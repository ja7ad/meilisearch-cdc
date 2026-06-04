// Package docs embeds the API reference so it can be served to MCP clients.
package docs

import _ "embed"

// API is docs/api.md.
//
//go:embed api.md
var API string
