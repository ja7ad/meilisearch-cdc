//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func mcpConnect(t *testing.T, e *env, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	tr := &mcp.StreamableClientTransport{Endpoint: e.srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}}, MaxRetries: -1}
	s, err := client.Connect(ctx, tr, nil)
	if err == nil {
		t.Cleanup(func() { s.Close() })
	}
	return s, err
}

func toolNames(t *testing.T, s *mcp.ClientSession) []string {
	res, err := s.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var out []string
	for _, tool := range res.Tools {
		out = append(out, tool.Name)
	}
	return out
}

func call(t *testing.T, s *mcp.ClientSession, name string, args any) (map[string]any, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	if res.IsError {
		return map[string]any{"error": res.Content[0].(*mcp.TextContent).Text}, false
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, true
}

func TestMCPServer(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("mcp"), uniq("idx")
	seed(table, 25)

	// Disabled by default.
	resp, err := http.Post(e.srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	var settings map[string]any
	e.mustDo("GET", "/api/v1/settings/mcp", nil, &settings, 200)
	assert.Equal(t, false, settings["enabled"])
	policy := settings["policy"].(map[string]any)
	e.mustDo("PUT", "/api/v1/settings/mcp", map[string]any{"enabled": true, "policy": policy}, &settings, 200)

	type tokenResp struct {
		Token string `json:"token"`
		Info  struct {
			ID     string `json:"id"`
			Prefix string `json:"prefix"`
		} `json:"info"`
	}
	var viewerTok, adminTok tokenResp
	e.mustDo("POST", "/api/v1/mcp/tokens", map[string]any{"name": "reader", "role": "viewer"}, &viewerTok, 201)
	e.mustDo("POST", "/api/v1/mcp/tokens", map[string]any{"name": "agent", "role": "admin", "expiresIn": "24h"}, &adminTok, 201)
	assert.True(t, strings.HasPrefix(viewerTok.Token, "cdcmcp_"))
	assert.True(t, strings.HasPrefix(viewerTok.Token, viewerTok.Info.Prefix))
	var list []map[string]any
	e.mustDo("GET", "/api/v1/mcp/tokens", nil, &list, 200)
	assert.Len(t, list, 2)
	raw, _ := json.Marshal(list)
	assert.NotContains(t, string(raw), viewerTok.Token, "raw tokens are never listed")

	_, err = mcpConnect(t, e, "cdcmcp_forged")
	assert.Error(t, err, "unknown tokens are rejected")

	// The admin agent creates and starts a task through MCP.
	admin, err := mcpConnect(t, e, adminTok.Token)
	require.NoError(t, err)
	assert.Contains(t, toolNames(t, admin), "create_task")
	out, ok := call(t, admin, "create_task", map[string]any{"start": true, "task": map[string]any{
		"name": "from mcp", "source": map[string]any{"type": "memory", "table": table}, "index": map[string]any{"uid": index},
		"sync": map[string]any{"flushInterval": "50ms"},
	}})
	require.True(t, ok, "%v", out)
	taskID := out["task"].(map[string]any)["id"].(string)
	e.waitState(taskID, model.StateStreaming)
	e.waitDocs(index, 25)
	out, ok = call(t, admin, "search_index", map[string]any{"uid": index, "query": "product 7", "limit": 3})
	require.True(t, ok, "%v", out)
	hits := out["result"].(map[string]any)["hits"].([]any)
	assert.NotEmpty(t, hits)
	_, ok = call(t, admin, "create_task", map[string]any{"task": map[string]any{"name": "", "nope": 1}})
	assert.False(t, ok, "invalid input is reported as a tool error")

	// The viewer sees read tools only, and resources.
	viewer, err := mcpConnect(t, e, viewerTok.Token)
	require.NoError(t, err)
	names := toolNames(t, viewer)
	assert.Contains(t, names, "list_tasks")
	assert.NotContains(t, names, "task_action")
	assert.NotContains(t, names, "create_task")
	out, ok = call(t, viewer, "list_tasks", map[string]any{})
	require.True(t, ok)
	assert.Len(t, out["tasks"], 1)
	_, err = viewer.CallTool(context.Background(), &mcp.CallToolParams{Name: "task_action", Arguments: map[string]any{"id": taskID, "action": "stop"}})
	assert.Error(t, err, "tools outside the policy do not exist for the session")
	doc, err := viewer.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "cdc://docs/api"})
	require.NoError(t, err)
	assert.Contains(t, doc.Contents[0].Text, "MCP server")
	taskRes, err := viewer.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "cdc://tasks/" + taskID})
	require.NoError(t, err)
	assert.Contains(t, taskRes.Contents[0].Text, `"streaming"`)

	// Granting task_action to viewers applies to new sessions; revoking it
	// applies to the open one at call time.
	vp := policy["viewer"].(map[string]any)
	tools := append(vp["tools"].([]any), "task_action")
	vp["tools"] = tools
	e.mustDo("PUT", "/api/v1/settings/mcp", map[string]any{"enabled": true, "policy": policy}, &settings, 200)
	viewer2, err := mcpConnect(t, e, viewerTok.Token)
	require.NoError(t, err)
	assert.True(t, slices.Contains(toolNames(t, viewer2), "task_action"))
	e.mustDo("POST", "/api/v1/settings/mcp/reset-policy", nil, &settings, 200)
	out, ok = call(t, viewer2, "task_action", map[string]any{"id": taskID, "action": "stop"})
	assert.False(t, ok, "policy is checked on every call")
	assert.Contains(t, out["error"], "may not use task_action")

	// Mutating calls are audited.
	var entries []map[string]any
	e.mustDo("GET", "/api/v1/audit?action=mcp.create_task", nil, &entries, 200)
	require.Len(t, entries, 1)
	assert.Equal(t, "mcp:agent", entries[0]["userEmail"])

	// Rotation and revocation take effect immediately.
	var rotated tokenResp
	e.mustDo("POST", "/api/v1/mcp/tokens/"+adminTok.Info.ID+"/rotate", nil, &rotated, 200)
	_, err = mcpConnect(t, e, adminTok.Token)
	assert.Error(t, err, "the rotated token is dead")
	_, err = mcpConnect(t, e, rotated.Token)
	assert.NoError(t, err)
	e.mustDo("DELETE", "/api/v1/mcp/tokens/"+viewerTok.Info.ID, nil, nil, 204)
	_, err = viewer.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{}})
	assert.Error(t, err, "a revoked token stops working in open sessions")

	// Disabling turns the endpoint off.
	e.mustDo("PUT", "/api/v1/settings/mcp", map[string]any{"enabled": false}, &settings, 200)
	_, err = mcpConnect(t, e, rotated.Token)
	assert.Error(t, err)
	assert.Equal(t, 422, e.do("PUT", "/api/v1/settings/mcp", map[string]any{"enabled": true, "policy": map[string]any{"viewer": map[string]any{"tools": []string{"rm_rf"}}}}, nil))
}
