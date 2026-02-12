//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ja7ad/meilisearch-cdc/internal/alerts"
	"github.com/ja7ad/meilisearch-cdc/internal/config"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/signature"
)

// browser is a cookie based client that behaves like the dashboard.
type browser struct {
	t      *testing.T
	base   string
	c      *http.Client
	origin string
}

func newBrowser(t *testing.T, e *env) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: e.srv.URL, c: &http.Client{Jar: jar}, origin: e.srv.URL}
}

func (b *browser) do(method, path string, body any, out any) (int, http.Header) {
	b.t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, b.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	if b.origin != "" {
		req.Header.Set("Origin", b.origin)
	}
	resp, err := b.c.Do(req)
	require.NoError(b.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		require.NoError(b.t, json.Unmarshal(raw, out), string(raw))
	}
	return resp.StatusCode, resp.Header
}

func (b *browser) code(method, path string, body any) int {
	c, _ := b.do(method, path, body, nil)
	return c
}

// mailpit captures email sent by the service.
type mailpit struct {
	smtpHost string
	smtpPort int
	api      string
}

func startMailpit(t *testing.T) *mailpit {
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "axllent/mailpit:latest",
			ExposedPorts: []string{"1025/tcp", "8025/tcp"},
			WaitingFor:   wait.ForHTTP("/livez").WithPort("8025/tcp").WithStartupTimeout(time.Minute),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	smtp, err := c.PortEndpoint(ctx, "1025/tcp", "")
	require.NoError(t, err)
	host, port, _ := net.SplitHostPort(smtp)
	p, _ := strconv.Atoi(port)
	api, _ := c.PortEndpoint(ctx, "8025/tcp", "http")
	return &mailpit{smtpHost: host, smtpPort: p, api: api}
}

// waitMail returns the text body of the newest message to addr.
func (m *mailpit) waitMail(t *testing.T, to, subject string) string {
	t.Helper()
	var body string
	eventually(t, 30*time.Second, func() bool {
		resp, err := http.Get(m.api + "/api/v1/search?query=" + "to:" + to)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var list struct {
			Messages []struct {
				ID      string `json:"ID"`
				Subject string `json:"Subject"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&list)
		for _, msg := range list.Messages {
			if strings.Contains(msg.Subject, subject) {
				r, err := http.Get(m.api + "/api/v1/message/" + msg.ID)
				if err != nil {
					return false
				}
				var full struct {
					Text string `json:"Text"`
				}
				_ = json.NewDecoder(r.Body).Decode(&full)
				r.Body.Close()
				body = full.Text
				return true
			}
		}
		return false
	}, func() string { return "no email to " + to + " with subject " + subject })
	return body
}

var linkRe = regexp.MustCompile(`https?://\S+/#/(invite|reset)/([A-Za-z0-9_-]+)`)

func tokenFrom(t *testing.T, body string) string {
	m := linkRe.FindStringSubmatch(body)
	require.NotNil(t, m, body)
	return m[2]
}

func TestAccountsRolesAndEmail(t *testing.T) {
	mp := startMailpit(t)
	e := newEnv(t, func(c *config.Config) { c.AdminToken = "" }) // humans only
	e.token = ""
	admin := newBrowser(t, e)

	var cfg map[string]any
	admin.do("GET", "/api/v1/auth/config", nil, &cfg)
	assert.Equal(t, true, cfg["setupRequired"])
	assert.Equal(t, false, cfg["smtpConfigured"])
	assert.Equal(t, 401, admin.code("GET", "/api/v1/tasks", nil))

	// First run setup creates the admin and signs in.
	var me struct{ User map[string]any }
	code, hdr := admin.do("POST", "/api/v1/auth/setup", map[string]any{"email": "ada@example.com", "name": "Ada", "password": "password123"}, &me)
	require.Equal(t, 200, code)
	assert.Equal(t, "admin", me.User["role"])
	cookie := hdr.Get("Set-Cookie")
	assert.Contains(t, cookie, "HttpOnly")
	assert.Contains(t, cookie, "SameSite=Lax")
	assert.Equal(t, 409, admin.code("POST", "/api/v1/auth/setup", map[string]any{"email": "x@example.com", "password": "password123"}))
	assert.Equal(t, 200, admin.code("GET", "/api/v1/tasks", nil))

	// CSRF: a cookie authenticated write without our Origin is refused.
	admin.origin = "https://evil.example"
	assert.Equal(t, 403, admin.code("POST", "/api/v1/users", map[string]any{"email": "e@example.com", "role": "viewer", "password": "password123"}))
	admin.origin = ""
	assert.Equal(t, 403, admin.code("POST", "/api/v1/users", map[string]any{"email": "e@example.com", "role": "viewer", "password": "password123"}))
	admin.origin = e.srv.URL

	// Users with each role.
	for _, u := range []struct{ email, role string }{{"mod@example.com", "moderator"}, {"view@example.com", "viewer"}} {
		require.Equal(t, 201, admin.code("POST", "/api/v1/users", map[string]any{"email": u.email, "name": u.role, "role": u.role, "password": "password123"}))
	}
	assert.Equal(t, 409, admin.code("POST", "/api/v1/users", map[string]any{"email": "mod@example.com", "role": "viewer", "password": "password123"}))
	assert.Equal(t, 422, admin.code("POST", "/api/v1/users", map[string]any{"email": "weak@example.com", "role": "viewer", "password": "short"}))

	table, index := uniq("rbac"), uniq("idx")
	seed(table, 3)
	var task model.TaskView
	code, _ = admin.do("POST", "/api/v1/tasks", memTask(table, index), &task)
	require.Equal(t, 201, code)

	login := func(email string) *browser {
		b := newBrowser(t, e)
		require.Equal(t, 200, b.code("POST", "/api/v1/auth/login", map[string]any{"email": email, "password": "password123"}))
		return b
	}
	viewer, mod := login("view@example.com"), login("mod@example.com")

	assert.Equal(t, 200, viewer.code("GET", "/api/v1/tasks/"+task.ID, nil))
	assert.NotEqual(t, 403, viewer.code("POST", "/api/v1/indexes/"+index+"/search", map[string]any{"q": ""}), "search is read only")
	assert.Equal(t, 403, viewer.code("POST", "/api/v1/tasks/"+task.ID+"/start", nil))
	assert.Equal(t, 403, viewer.code("GET", "/api/v1/users", nil))
	assert.Equal(t, 200, viewer.code("GET", "/metrics", nil), "metrics are read only")
	assert.Equal(t, 401, newBrowser(t, e).code("GET", "/metrics", nil))

	assert.Equal(t, 200, mod.code("POST", "/api/v1/tasks/"+task.ID+"/start", nil), "moderators operate tasks")
	eventually(t, time.Minute, func() bool {
		var v model.TaskView
		admin.do("GET", "/api/v1/tasks/"+task.ID, nil, &v)
		return v.Status.State == model.StateStreaming
	}, func() string { return "task did not start" })
	assert.Equal(t, 200, mod.code("POST", "/api/v1/tasks/"+task.ID+"/stop", nil))
	assert.Equal(t, 403, mod.code("POST", "/api/v1/tasks", memTask(table, uniq("idx"))), "but cannot create")
	assert.Equal(t, 403, mod.code("DELETE", "/api/v1/tasks/"+task.ID, nil))
	assert.Equal(t, 403, mod.code("POST", "/api/v1/tasks/batch", map[string]any{"ids": []string{task.ID}, "action": "delete"}))
	assert.Equal(t, 403, mod.code("PATCH", "/api/v1/indexes/"+index+"/settings", map[string]any{"stopWords": []string{"a"}}))

	// Saved connections can be re-tested by moderators; unsaved values (which
	// may reuse stored secrets) only by admins.
	var dests []map[string]any
	admin.do("GET", "/api/v1/destinations", nil, &dests)
	require.NotEmpty(t, dests)
	destID := dests[0]["id"].(string)
	assert.Equal(t, 200, mod.code("POST", "/api/v1/destinations/"+destID+"/test", nil))
	assert.Equal(t, 403, viewer.code("POST", "/api/v1/destinations/"+destID+"/test", nil))
	assert.Equal(t, 403, mod.code("POST", "/api/v1/destinations/test", map[string]any{"id": destID, "url": "http://127.0.0.1:1", "apiKey": model.SecretMask}))
	assert.Equal(t, 403, mod.code("POST", "/api/v1/connectors/test", map[string]any{"type": "postgres", "config": map[string]any{"dsn": "postgres://x@127.0.0.1:1/x"}}))

	// A stored key is never sent to another URL, even by an admin's test.
	leak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the stored API key reached another server: %q", r.Header.Get("Authorization"))
	}))
	defer leak.Close()
	var res map[string]any
	code, _ = admin.do("POST", "/api/v1/destinations/test", map[string]any{"id": destID, "name": "x", "url": leak.URL, "apiKey": model.SecretMask}, &res)
	assert.Equal(t, 200, code)
	assert.Contains(t, res["error"], "enter the API key again")

	// Self service password change keeps the current session, revokes others.
	other := login("mod@example.com")
	assert.Equal(t, 422, mod.code("POST", "/api/v1/auth/me/password", map[string]any{"currentPassword": "wrong-one", "newPassword": "newpassword1"}))
	assert.Equal(t, 204, mod.code("POST", "/api/v1/auth/me/password", map[string]any{"currentPassword": "password123", "newPassword": "newpassword1"}))
	assert.Equal(t, 200, mod.code("GET", "/api/v1/auth/me", nil))
	assert.Equal(t, 401, other.code("GET", "/api/v1/auth/me", nil))

	// Admin guards: no self delete, no changing your own role.
	var users []map[string]any
	admin.do("GET", "/api/v1/users", nil, &users)
	ids := map[string]string{}
	for _, u := range users {
		ids[u["email"].(string)] = u["id"].(string)
	}
	assert.Equal(t, 409, admin.code("DELETE", "/api/v1/users/"+ids["ada@example.com"], nil))
	assert.Equal(t, 403, admin.code("PATCH", "/api/v1/users/"+ids["ada@example.com"], map[string]any{"role": "viewer"}), "no one changes their own role")
	assert.Equal(t, 204, admin.code("POST", "/api/v1/users/"+ids["view@example.com"]+"/password", map[string]any{"password": "resetbyadmin"}))
	assert.Equal(t, 401, viewer.code("GET", "/api/v1/auth/me", nil), "an admin password reset signs the user out")
	assert.Equal(t, 204, admin.code("DELETE", "/api/v1/users/"+ids["view@example.com"], nil))
	assert.Equal(t, 401, newBrowser(t, e).code("POST", "/api/v1/auth/login", map[string]any{"email": "view@example.com", "password": "resetbyadmin"}))

	// Forgot password needs SMTP.
	anon := newBrowser(t, e)
	assert.Equal(t, 409, anon.code("POST", "/api/v1/auth/forgot", map[string]any{"email": "mod@example.com"}))
	var smtp map[string]any
	code, _ = admin.do("PUT", "/api/v1/settings/smtp", map[string]any{"host": mp.smtpHost, "port": mp.smtpPort, "from": "Meili CDC <cdc@example.com>", "tls": "none", "publicUrl": "https://cdc.example.com"}, &smtp)
	require.Equal(t, 200, code, "%v", smtp)
	assert.Equal(t, true, smtp["configured"])
	var test map[string]any
	admin.do("POST", "/api/v1/settings/smtp/test", map[string]any{"to": "ops@example.com"}, &test)
	assert.Equal(t, true, test["ok"], "%v", test)
	mp.waitMail(t, "ops@example.com", "test email")
	anon.do("GET", "/api/v1/auth/config", nil, &cfg)
	assert.Equal(t, true, cfg["smtpConfigured"])

	assert.Equal(t, 202, anon.code("POST", "/api/v1/auth/forgot", map[string]any{"email": "nobody@example.com"}), "no account enumeration")
	assert.Equal(t, 202, anon.code("POST", "/api/v1/auth/forgot", map[string]any{"email": "mod@example.com"}))
	body := mp.waitMail(t, "mod@example.com", "Reset your Meili CDC password")
	assert.Contains(t, body, "https://cdc.example.com/#/reset/")
	tok := tokenFrom(t, body)
	assert.Equal(t, 422, anon.code("POST", "/api/v1/auth/reset", map[string]any{"token": tok, "password": "short"}))
	assert.Equal(t, 204, anon.code("POST", "/api/v1/auth/reset", map[string]any{"token": tok, "password": "fromemail123"}))
	assert.Equal(t, 400, anon.code("POST", "/api/v1/auth/reset", map[string]any{"token": tok, "password": "fromemail123"}), "single use")
	assert.Equal(t, 401, mod.code("GET", "/api/v1/auth/me", nil), "reset revokes sessions")
	assert.Equal(t, 200, anon.code("POST", "/api/v1/auth/login", map[string]any{"email": "mod@example.com", "password": "fromemail123"}))

	// Invitation by email, accepted in a fresh browser.
	var inv map[string]any
	code, _ = admin.do("POST", "/api/v1/invitations", map[string]any{"email": "newbie@example.com", "role": "viewer"}, &inv)
	require.Equal(t, 201, code)
	assert.Equal(t, true, inv["emailed"], "%v", inv)
	assert.Contains(t, inv["link"], "https://cdc.example.com/#/invite/")
	body = mp.waitMail(t, "newbie@example.com", "invited")
	itok := tokenFrom(t, body)
	guest := newBrowser(t, e)
	var info map[string]any
	guest.do("GET", "/api/v1/auth/invite/"+itok, nil, &info)
	assert.Equal(t, "newbie@example.com", info["email"])
	assert.Equal(t, "viewer", info["role"])
	code, _ = guest.do("POST", "/api/v1/auth/invite/accept", map[string]any{"token": itok, "name": "Newbie", "password": "password123"}, &me)
	require.Equal(t, 200, code)
	assert.Equal(t, "viewer", me.User["role"])
	assert.Equal(t, 200, guest.code("GET", "/api/v1/tasks", nil), "signed in")
	assert.Equal(t, 404, anon.code("GET", "/api/v1/auth/invite/"+itok, nil), "single use")

	// Logout.
	assert.Equal(t, 204, guest.code("POST", "/api/v1/auth/logout", nil))
	assert.Equal(t, 401, guest.code("GET", "/api/v1/tasks", nil))

	// Brute force protection.
	bad := newBrowser(t, e)
	var last int
	var hdrs http.Header
	for i := 0; i < 12; i++ {
		last, hdrs = bad.do("POST", "/api/v1/auth/login", map[string]any{"email": "ada@example.com", "password": fmt.Sprint("guess", i)}, nil)
	}
	assert.Equal(t, 429, last)
	assert.NotEmpty(t, hdrs.Get("Retry-After"))

	// The audit log saw it all.
	var entries []map[string]any
	admin.do("GET", "/api/v1/audit?limit=500", nil, &entries)
	actions := map[string]bool{}
	for _, en := range entries {
		actions[en["action"].(string)] = true
	}
	for _, a := range []string{"auth.setup", "auth.login", "auth.login_failed", "auth.password_reset", "auth.invitation_accepted", "POST /api/v1/users", "POST /api/v1/tasks", "DELETE /api/v1/users/{id}"} {
		assert.True(t, actions[a], "audit has %s", a)
	}
}

func TestAlertsDeliverToTelegramAndWebhook(t *testing.T) {
	var mu sync.Mutex
	var tgMessages, hooks []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"username":"cdc_alerts_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			tgMessages = append(tgMessages, fmt.Sprint(b["chat_id"], "|", b["text"]))
			mu.Unlock()
			_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
		}
	}))
	defer tg.Close()
	const hookSecret = "whsec_alert_hook_secret_01"
	var badSignatures atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if signature.Verify(r.Header, hookSecret, time.Now(), raw) != nil {
			badSignatures.Add(1)
		}
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		mu.Lock()
		hooks = append(hooks, fmt.Sprint(b["text"]))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer hook.Close()

	e := newEnv(t, func(c *config.Config) { c.AlertInterval = 200 * time.Millisecond })
	taskFailed := map[string]any{"name": "Task failed", "enabled": true, "condition": map[string]any{"type": "task_state"},
		"channels": map[string]any{"webhookUrl": hook.URL}}

	// Without SMTP or Telegram, rules cannot be created yet.
	var ch alerts.Availability
	e.mustDo("GET", "/api/v1/alerts/channels", nil, &ch, 200)
	assert.Equal(t, alerts.Availability{Webhook: true}, ch)
	assert.Equal(t, 409, e.do("POST", "/api/v1/alerts/rules", taskFailed, nil))

	var tgView map[string]any
	e.mustDo("PUT", "/api/v1/settings/telegram", map[string]any{"botToken": "123:abc", "apiUrl": tg.URL}, &tgView, 200)
	assert.Equal(t, "cdc_alerts_bot", tgView["botName"])
	assert.Equal(t, "••••••", tgView["botToken"])
	e.mustDo("GET", "/api/v1/alerts/channels", nil, &ch, 200)
	assert.Equal(t, alerts.Availability{Telegram: true, Webhook: true}, ch)
	var cfg map[string]any
	e.mustDo("GET", "/api/v1/auth/config", nil, &cfg, 200)
	assert.Equal(t, true, cfg["telegramConfigured"])
	assert.Equal(t, false, cfg["smtpConfigured"])

	// Email recipients still need SMTP; no recipient at all is refused.
	emailOnly := map[string]any{"name": "Mail", "enabled": true, "condition": map[string]any{"type": "task_state"},
		"channels": map[string]any{"emails": []string{"ops@example.com"}}}
	var body map[string]any
	assert.Equal(t, 422, e.do("POST", "/api/v1/alerts/rules", emailOnly, &body))
	assert.Contains(t, body["error"], "SMTP")
	assert.Equal(t, 422, e.do("POST", "/api/v1/alerts/rules", map[string]any{"name": "Nobody", "condition": map[string]any{"type": "task_state"}}, nil))
	// A disabled rule can be saved whatever is configured.
	var off alerts.Rule
	e.mustDo("POST", "/api/v1/alerts/rules", taskFailed, &off, 201)
	emailOnly["enabled"] = false
	e.mustDo("PUT", "/api/v1/alerts/rules/"+off.ID, emailOnly, nil, 200)
	e.mustDo("DELETE", "/api/v1/alerts/rules/"+off.ID, nil, nil, 204)

	var rule alerts.Rule
	e.mustDo("POST", "/api/v1/alerts/rules", map[string]any{
		"name": "Task failed", "enabled": true, "severity": "critical", "notifyResolved": true,
		"condition": map[string]any{"type": "task_state", "states": []string{"failed"}},
		"channels":  map[string]any{"telegramChatIds": []string{"-1001"}, "webhookUrl": hook.URL, "webhookSecret": hookSecret},
	}, &rule, 201)
	assert.Equal(t, alerts.SecretMask, rule.Channels.WebhookSecret, "the webhook secret is never returned")
	var listed []alerts.Rule
	e.mustDo("GET", "/api/v1/alerts/rules", nil, &listed, 200)
	assert.Equal(t, alerts.SecretMask, listed[0].Channels.WebhookSecret)
	e.mustDo("PUT", "/api/v1/alerts/rules/"+rule.ID, listed[0], nil, 200) // the mask keeps the secret
	assert.Equal(t, 422, e.do("POST", "/api/v1/alerts/rules", map[string]any{"name": "", "condition": map[string]any{"type": "nope"}}, nil))

	var test map[string]any
	e.mustDo("POST", "/api/v1/alerts/rules/"+rule.ID+"/test", nil, &test, 200)
	assert.Len(t, test["results"], 2)
	assert.Zero(t, badSignatures.Load(), "webhook deliveries are signed with the rule's secret")

	// A task that fails because the index uses another primary key.
	table, index := uniq("alert"), uniq("idx")
	seed(table, 2)
	info, err := e.meili.CreateIndex(context.Background(), &meilisearch.IndexConfig{Uid: index, PrimaryKey: "sku"})
	require.NoError(t, err)
	_, _ = e.meili.WaitForTask(context.Background(), info.TaskUID, 50*time.Millisecond)
	info, err = e.meili.Index(index).AddDocuments(context.Background(), []map[string]any{{"sku": "x"}}, nil)
	require.NoError(t, err)
	_, _ = e.meili.WaitForTask(context.Background(), info.TaskUID, 50*time.Millisecond)
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", memTask(table, index), &v, 201)
	e.waitState(v.ID, model.StateFailed)

	var active []alerts.Event
	eventually(t, 30*time.Second, func() bool {
		e.mustDo("GET", "/api/v1/alerts/active", nil, &active, 200)
		return len(active) == 1
	}, func() string { return "alert did not fire" })
	assert.Equal(t, v.ID, active[0].TaskID)
	eventually(t, 30*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range tgMessages {
			if strings.Contains(m, "-1001|") && strings.Contains(m, "primary key") {
				return len(hooks) >= 2 // test + firing
			}
		}
		return false
	}, func() string {
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("telegram=%v hooks=%v", tgMessages, hooks)
	})

	// Fixing the task resolves the alert.
	e.mustDo("POST", "/api/v1/tasks/"+v.ID+"/stop", nil, nil, 200)
	eventually(t, 30*time.Second, func() bool {
		e.mustDo("GET", "/api/v1/alerts/active", nil, &active, 200)
		return len(active) == 0
	}, func() string { return "alert did not resolve" })
	var evs []alerts.Event
	e.mustDo("GET", "/api/v1/alerts/events?ruleId="+rule.ID, nil, &evs, 200)
	require.GreaterOrEqual(t, len(evs), 2)
	assert.Equal(t, alerts.Resolved, evs[0].State)
}
