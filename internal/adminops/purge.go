package adminops

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Scope is a kind of stored data `admin purge` clears.
type Scope struct {
	Name        string
	Description string
}

// All purges every scope: the data directory is as good as new, except
// that the secret key file stays.
const All = "all"

// Scopes lists every purge scope in the order they are cleared: tasks go
// before the connectors and destinations they use.
var Scopes = []Scope{
	{"tasks", "stop and delete every task with its checkpoint, status, dead letters and diff report; --indexes also deletes each task's Meilisearch index"},
	{"checkpoints", "forget sync progress: every task runs a full snapshot on its next start (running tasks restart now)"},
	{"dlq", "dead letters of every task"},
	{"diffs", "diff reports (running diffs are cancelled)"},
	{"logs", "the task activity log (kept in memory, so only a running service has one)"},
	{"connectors", "saved source connections; refused while tasks exist unless tasks are purged too"},
	{"destinations", "Meilisearch destinations; refused while tasks exist unless tasks are purged too (MEILI_URL's is recreated)"},
	{"alert-rules", "alert rules and their alert state; the history stays"},
	{"alert-events", "alert history and state; rules stay"},
	{"mcp-tokens", "MCP bearer tokens"},
	{"settings", "SMTP, Telegram and the MCP settings (MCP is disabled, default policy)"},
	{"sessions", "every sign-in session: everyone is signed out"},
	{"invitations", "pending invitations"},
	{"users", "every account with its sessions, invitations and password resets; the next visit shows the setup page"},
	{"audit", "the audit log (the purge itself is recorded afterwards)"},
}

// Purged is how many records one scope removed.
type Purged struct {
	Scope string `json:"scope"`
	Count int    `json:"count"`
}

// ParseScopes validates scope names and returns them in purge order.
func ParseScopes(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("name at least one scope: %s or %s", scopeNames(), All)
	}
	want := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == All {
			for _, s := range Scopes {
				want[s.Name] = true
			}
			continue
		}
		if !slices.ContainsFunc(Scopes, func(s Scope) bool { return s.Name == n }) {
			return nil, fmt.Errorf("unknown scope %q (%s or %s)", n, scopeNames(), All)
		}
		want[n] = true
	}
	var out []string
	for _, s := range Scopes {
		if want[s.Name] {
			out = append(out, s.Name)
		}
	}
	return out, nil
}

func scopeNames() string {
	names := make([]string, len(Scopes))
	for i, s := range Scopes {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

func (o *Ops) purge(ctx context.Context, names []string, indexes bool) (Result, error) {
	scopes, err := ParseScopes(names)
	if err != nil {
		return Result{}, err
	}
	all := slices.Contains(names, All)
	tasks := slices.Contains(scopes, "tasks")
	if !tasks && (slices.Contains(scopes, "connectors") || slices.Contains(scopes, "destinations")) {
		if n, err := o.d.Engine.TaskCount(); err != nil {
			return Result{}, err
		} else if n > 0 {
			return Result{}, fmt.Errorf("%d %s still use connectors and destinations: purge tasks in the same command", n, plural(n, "task", "tasks"))
		}
	}
	if indexes && !tasks {
		return Result{}, fmt.Errorf("--indexes applies to the tasks scope only")
	}
	var res Result
	for _, s := range scopes {
		n, err := o.purgeScope(ctx, s, indexes)
		res.Purged = append(res.Purged, Purged{Scope: s, Count: n})
		if err != nil {
			res.Message = summary(res.Purged)
			return res, fmt.Errorf("purge %s: %w", s, err)
		}
	}
	if all {
		// Whatever no scope covers (connection tests, password resets...).
		if _, err := o.d.DB.Clear(); err != nil {
			return res, err
		}
		if err := o.restoreEnv(); err != nil {
			return res, err
		}
	}
	res.Message = summary(res.Purged)
	return res, nil
}

func (o *Ops) purgeScope(ctx context.Context, scope string, indexes bool) (int, error) {
	e := o.d.Engine
	switch scope {
	case "tasks":
		return e.PurgeTasks(ctx, indexes)
	case "checkpoints":
		return e.PurgeCheckpoints(ctx)
	case "dlq":
		return e.PurgeDLQ()
	case "diffs":
		return e.PurgeDiffs()
	case "logs":
		return e.PurgeLogs(), nil
	case "connectors":
		return e.PurgeConnectors()
	case "destinations":
		n, err := e.PurgeDestinations()
		if err != nil {
			return n, err
		}
		return n, o.restoreEnv()
	case "alert-rules":
		return o.d.Alerts.PurgeRules()
	case "alert-events":
		return o.d.Alerts.PurgeEvents()
	case "mcp-tokens":
		return o.d.MCP.RevokeAllTokens()
	case "settings":
		n, err := o.d.Notify.Purge()
		if err != nil {
			return n, err
		}
		m, err := o.d.MCP.ResetConfig()
		return n + m, err
	case "sessions":
		return o.d.Auth.SignOutEveryone()
	case "invitations":
		return o.d.Auth.PurgeInvitations()
	case "users":
		return o.d.Auth.PurgeUsers()
	case "audit":
		return o.d.Audit.Purge()
	}
	return 0, fmt.Errorf("unknown scope %q", scope)
}

func (o *Ops) restoreEnv() error {
	if o.d.RestoreEnv == nil {
		return nil
	}
	return o.d.RestoreEnv()
}

func summary(p []Purged) string {
	parts := make([]string, len(p))
	for i, x := range p {
		parts[i] = fmt.Sprintf("%s: %d", x.Scope, x.Count)
	}
	return "purged " + strings.Join(parts, ", ")
}
