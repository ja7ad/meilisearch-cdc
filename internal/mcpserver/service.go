// Package mcpserver exposes meilisearch-cdc to AI agents over the Model
// Context Protocol (Streamable HTTP) with revocable bearer tokens and a
// role based allow list of tools and resources.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ja7ad/meilisearch-cdc/internal/alerts"
	"github.com/ja7ad/meilisearch-cdc/internal/audit"
	"github.com/ja7ad/meilisearch-cdc/internal/auth"
	"github.com/ja7ad/meilisearch-cdc/internal/engine"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// TokenPrefix starts every MCP token, which makes leaked tokens easy to spot.
const TokenPrefix = "cdcmcp_"

// Access lists what one role may use.
type Access struct {
	Tools     []string `json:"tools"`
	Resources []string `json:"resources"`
}

// Policy maps roles to access.
type Policy map[auth.Role]Access

// Config is the stored MCP configuration.
type Config struct {
	Enabled bool   `json:"enabled"`
	Policy  Policy `json:"policy"`
}

// Token is a stored MCP bearer token (the raw value is never stored).
type Token struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Role       auth.Role `json:"role"`
	Prefix     string    `json:"prefix"`
	CreatedAt  time.Time `json:"createdAt"`
	CreatedBy  string    `json:"createdBy"`
	LastUsedAt time.Time `json:"lastUsedAt,omitzero"`
	ExpiresAt  time.Time `json:"expiresAt,omitzero"`
}

// ToolInfo describes a tool for the admin UI.
type ToolInfo struct {
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	MinRole     auth.Role `json:"minRole"`
	Mutating    bool      `json:"mutating"`
}

// ResourceInfo describes a resource for the admin UI.
type ResourceInfo struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Errors.
var (
	ErrNotFound = errors.New("token not found")
	ErrInvalid  = errors.New("invalid request")
)

// Service owns MCP settings, tokens and the protocol handler.
type Service struct {
	eng    *engine.Engine
	alerts *alerts.Manager
	audit  *audit.Log
	log    *slog.Logger
	cfg    *store.Bucket[Config]
	tokens *store.Bucket[Token] // keyed by token hash

	mu      sync.Mutex
	conf    *Config
	version int
	servers map[string]*mcp.Server // role@version -> server
	handler http.Handler
	now     func() time.Time
}

// New opens the MCP buckets.
func New(db *store.DB, eng *engine.Engine, am *alerts.Manager, al *audit.Log, log *slog.Logger) (*Service, error) {
	s := &Service{eng: eng, alerts: am, audit: al, log: log, servers: map[string]*mcp.Server{}, now: time.Now}
	var err error
	if s.cfg, err = store.NewBucket[Config](db, "mcp_config"); err != nil {
		return nil, err
	}
	if s.tokens, err = store.NewBucket[Token](db, "mcp_tokens"); err != nil {
		return nil, err
	}
	streamable := mcp.NewStreamableHTTPHandler(s.serverFor, &mcp.StreamableHTTPOptions{})
	s.handler = mcpauth.RequireBearerToken(s.verify, &mcpauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(streamable)
	return s, nil
}

// DefaultPolicy grants each role the tools whose minimum role it meets and
// every resource.
func DefaultPolicy() Policy {
	p := Policy{}
	for _, role := range auth.Roles {
		var a Access
		for _, t := range catalog {
			if role.AtLeast(t.info.MinRole) {
				a.Tools = append(a.Tools, t.info.Name)
			}
		}
		for _, r := range resources {
			a.Resources = append(a.Resources, r.info.URI)
		}
		p[role] = a
	}
	return p
}

// Config returns the current configuration.
func (s *Service) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conf == nil {
		c, ok, _ := s.cfg.Get("config")
		if !ok || c.Policy == nil {
			c.Policy = DefaultPolicy()
		}
		s.conf = &c
	}
	return *s.conf
}

// SetConfig validates and stores the configuration. Unknown tool and
// resource names are rejected.
func (s *Service) SetConfig(c Config) (Config, error) {
	if c.Policy == nil {
		c.Policy = s.Config().Policy
	}
	for role, a := range c.Policy {
		if !role.Valid() {
			return c, fmt.Errorf("%w: unknown role %q", ErrInvalid, role)
		}
		for _, t := range a.Tools {
			if _, ok := toolByName[t]; !ok {
				return c, fmt.Errorf("%w: unknown tool %q", ErrInvalid, t)
			}
		}
		for _, r := range a.Resources {
			if _, ok := resourceByURI[r]; !ok {
				return c, fmt.Errorf("%w: unknown resource %q", ErrInvalid, r)
			}
		}
		a.Tools, a.Resources = dedupe(a.Tools), dedupe(a.Resources)
		c.Policy[role] = a
	}
	for _, role := range auth.Roles {
		if _, ok := c.Policy[role]; !ok {
			c.Policy[role] = Access{Tools: []string{}, Resources: []string{}}
		}
	}
	if err := s.cfg.Put("config", c); err != nil {
		return c, err
	}
	s.mu.Lock()
	s.conf = &c
	s.version++
	clear(s.servers)
	s.mu.Unlock()
	return c, nil
}

func dedupe(in []string) []string {
	out := []string{}
	for _, v := range in {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

// Tools describes every tool.
func (s *Service) Tools() []ToolInfo {
	out := make([]ToolInfo, 0, len(catalog))
	for _, t := range catalog {
		out = append(out, t.info)
	}
	return out
}

// Resources describes every resource.
func (s *Service) Resources() []ResourceInfo {
	out := make([]ResourceInfo, 0, len(resources))
	for _, r := range resources {
		out = append(out, r.info)
	}
	return out
}

func (s *Service) allowedTool(role auth.Role, name string) bool {
	return slices.Contains(s.Config().Policy[role].Tools, name)
}

func (s *Service) allowedResource(role auth.Role, uri string) bool {
	return slices.Contains(s.Config().Policy[role].Resources, uri)
}

// Tokens lists tokens, newest first.
func (s *Service) Tokens() ([]Token, error) {
	out, err := s.tokens.List("", 0)
	if out == nil {
		out = []Token{}
	}
	slices.SortFunc(out, func(a, b Token) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, err
}

// CreateToken issues a token and returns its raw value once.
func (s *Service) CreateToken(name string, role auth.Role, expiresIn time.Duration, createdBy string) (string, Token, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", Token{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if !role.Valid() {
		return "", Token{}, fmt.Errorf("%w: unknown role %q", ErrInvalid, role)
	}
	if expiresIn < 0 {
		return "", Token{}, fmt.Errorf("%w: expiresIn must not be negative", ErrInvalid)
	}
	raw := auth.NewToken(TokenPrefix, 32)
	t := Token{ID: auth.NewID(), Name: name, Role: role, Prefix: raw[:len(TokenPrefix)+4], CreatedAt: s.now().UTC(), CreatedBy: createdBy}
	if expiresIn > 0 {
		t.ExpiresAt = t.CreatedAt.Add(expiresIn)
	}
	return raw, t, s.tokens.Put(auth.TokenHash(raw), t)
}

func (s *Service) tokenKey(id string) (string, Token, error) {
	var key string
	var tok Token
	err := s.tokens.ForEach("", func(k string, v Token) bool {
		if v.ID == id {
			key, tok = k, v
			return false
		}
		return true
	})
	if err == nil && key == "" {
		err = ErrNotFound
	}
	return key, tok, err
}

// RevokeToken deletes a token; it stops working immediately.
func (s *Service) RevokeToken(id string) error {
	key, _, err := s.tokenKey(id)
	if err != nil {
		return err
	}
	return s.tokens.Delete(key)
}

// RotateToken replaces a token with a new one of the same name and role.
func (s *Service) RotateToken(id, by string) (string, Token, error) {
	key, old, err := s.tokenKey(id)
	if err != nil {
		return "", Token{}, err
	}
	var ttl time.Duration
	if !old.ExpiresAt.IsZero() {
		ttl = old.ExpiresAt.Sub(old.CreatedAt)
	}
	raw, t, err := s.CreateToken(old.Name, old.Role, ttl, by)
	if err != nil {
		return "", Token{}, err
	}
	return raw, t, s.tokens.Delete(key)
}

// RevokeAllTokens deletes every token; they stop working immediately.
func (s *Service) RevokeAllTokens() (int, error) { return s.tokens.Clear() }

// Disable turns the MCP endpoint off, keeping the policy and tokens.
func (s *Service) Disable() (Config, error) {
	c := s.Config()
	c.Enabled, c.Policy = false, maps.Clone(c.Policy) // the cached map is shared with readers
	return s.SetConfig(c)
}

// ResetConfig deletes the stored configuration: the server is disabled
// and the default policy applies again.
func (s *Service) ResetConfig() (int, error) {
	n, err := s.cfg.Clear()
	s.mu.Lock()
	s.conf = nil
	s.version++
	clear(s.servers)
	s.mu.Unlock()
	return n, err
}

// verify authenticates a bearer token for the MCP endpoint.
func (s *Service) verify(_ context.Context, raw string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	if !strings.HasPrefix(raw, TokenPrefix) {
		return nil, mcpauth.ErrInvalidToken
	}
	key := auth.TokenHash(raw)
	t, ok, err := s.tokens.Get(key)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if !ok || (!t.ExpiresAt.IsZero() && now.After(t.ExpiresAt)) {
		return nil, mcpauth.ErrInvalidToken
	}
	if now.Sub(t.LastUsedAt) > time.Minute {
		_, _ = s.tokens.UpdateExisting(key, func(v *Token) error {
			v.LastUsedAt = now.UTC()
			return nil
		})
	}
	info := &mcpauth.TokenInfo{UserID: t.ID, Extra: map[string]any{"role": string(t.Role), "name": t.Name}}
	if !t.ExpiresAt.IsZero() {
		info.Expiration = t.ExpiresAt
	}
	return info, nil
}

// Handler serves /mcp.
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Config().Enabled {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"the MCP server is disabled"}`))
			return
		}
		s.handler.ServeHTTP(w, r)
	})
}

// principal turns the verified token into the caller of a tool.
func principal(ti *mcpauth.TokenInfo) *auth.Principal {
	if ti == nil {
		return nil
	}
	role, _ := ti.Extra["role"].(string)
	name, _ := ti.Extra["name"].(string)
	return &auth.Principal{ID: "mcp:" + ti.UserID, Name: "mcp:" + name, Role: auth.Role(role), Kind: "mcp"}
}

// serverFor returns the MCP server of the caller's role, listing only the
// tools and resources the policy grants it. Calls are checked again at run
// time, so policy changes apply to open sessions too.
func (s *Service) serverFor(r *http.Request) *mcp.Server {
	p := principal(mcpauth.TokenInfoFromContext(r.Context()))
	role := auth.Viewer
	if p != nil && p.Role.Valid() {
		role = p.Role
	}
	conf := s.Config()
	s.mu.Lock()
	key := fmt.Sprintf("%s@%d", role, s.version)
	if srv, ok := s.servers[key]; ok {
		s.mu.Unlock()
		return srv
	}
	s.mu.Unlock()

	srv := mcp.NewServer(&mcp.Implementation{Name: "meilisearch-cdc", Title: "Meilisearch CDC", Version: "1"}, &mcp.ServerOptions{
		Instructions: "Manage change data capture pipelines that sync databases into Meilisearch: tasks, indexes, data quality checks, dead letters and alerts. Read docs with the cdc://docs/api resource.",
	})
	access := conf.Policy[role]
	for _, t := range catalog {
		if slices.Contains(access.Tools, t.info.Name) {
			t.register(srv, s)
		}
	}
	for _, res := range resources {
		if slices.Contains(access.Resources, res.info.URI) {
			res.register(srv, s)
		}
	}
	s.mu.Lock()
	s.servers[key] = srv
	s.mu.Unlock()
	return srv
}
