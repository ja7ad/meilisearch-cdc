// Package adminops implements the operator commands of
// `meilisearch-cdc admin`. A running service answers them on a unix socket
// in its data directory; while it is stopped the CLI opens the store and
// runs them itself. Both paths call Ops.Do, so they behave the same.
package adminops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/ja7ad/meilisearch-cdc/internal/alerts"
	"github.com/ja7ad/meilisearch-cdc/internal/audit"
	"github.com/ja7ad/meilisearch-cdc/internal/auth"
	"github.com/ja7ad/meilisearch-cdc/internal/engine"
	"github.com/ja7ad/meilisearch-cdc/internal/mcpserver"
	"github.com/ja7ad/meilisearch-cdc/internal/notify"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// Actor is the user recorded in the audit log for every CLI action.
const Actor = "cli"

// Commands.
const (
	UsersList          = "users.list"
	UsersCreate        = "users.create"
	UsersResetPassword = "users.reset-password"
	UsersSetRole       = "users.set-role"
	UsersSetOwner      = "users.set-owner"
	UsersDelete        = "users.delete"
	SessionsRevoke     = "sessions.revoke"
	MCPDisable         = "mcp.disable"
	MCPRevokeTokens    = "mcp.revoke-tokens" // #nosec G101 -- operation name
	Purge              = "purge"
)

// Request is one command with its arguments.
type Request struct {
	Command  string   `json:"command"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	Role     string   `json:"role,omitempty"`
	Password string   `json:"password,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	// Indexes also deletes each task's Meilisearch index when purging tasks.
	Indexes bool `json:"indexes,omitempty"`
}

// Result is the outcome of a command.
type Result struct {
	Message string            `json:"message,omitempty"`
	Users   []auth.PublicUser `json:"users,omitempty"`
	User    *auth.PublicUser  `json:"user,omitempty"`
	// Count is the number of sessions or tokens revoked.
	Count  int      `json:"count,omitempty"`
	Purged []Purged `json:"purged,omitempty"`
}

// Deps are the services the commands act on.
type Deps struct {
	Auth   *auth.Service
	Engine *engine.Engine
	Notify *notify.Service
	Alerts *alerts.Manager
	Audit  *audit.Log
	MCP    *mcpserver.Service
	DB     *store.DB
	// RestoreEnv recreates the MEILI_URL destination after destinations
	// are purged; nil when MEILI_URL is not set.
	RestoreEnv func() error
}

// Ops runs commands one at a time.
type Ops struct {
	d  Deps
	mu sync.Mutex
}

// New returns the commands over d.
func New(d Deps) *Ops { return &Ops{d: d} }

// Do runs a command and records it in the audit log.
func (o *Ops) Do(ctx context.Context, req Request) (Result, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	res, target, err := o.do(ctx, req)
	e := audit.Entry{UserID: Actor, UserEmail: Actor, Action: "cli." + req.Command, Target: target, Details: res.Message}
	if err != nil {
		e.Details = "failed: " + err.Error()
	}
	o.d.Audit.Record(e)
	return res, err
}

func (o *Ops) do(ctx context.Context, req Request) (Result, string, error) {
	switch req.Command {
	case UsersList:
		users, err := o.d.Auth.Users()
		slices.SortStableFunc(users, func(a, b auth.PublicUser) int { return a.CreatedAt.Compare(b.CreatedAt) })
		return Result{Users: users}, "", err
	case UsersCreate:
		res, err := o.createUser(req)
		return res, req.Email, err
	case UsersResetPassword:
		res, err := o.resetPassword(req)
		return res, req.Email, err
	case UsersSetRole:
		res, err := o.setRole(req)
		return res, req.Email, err
	case UsersSetOwner:
		res, err := o.setOwner(req)
		return res, req.Email, err
	case UsersDelete:
		res, err := o.deleteUser(req)
		return res, req.Email, err
	case SessionsRevoke:
		res, err := o.revokeSessions(req)
		return res, req.Email, err
	case MCPDisable:
		if _, err := o.d.MCP.Disable(); err != nil {
			return Result{}, "", err
		}
		return Result{Message: "the MCP server is disabled; tokens and the policy are kept"}, "", nil
	case MCPRevokeTokens:
		n, err := o.d.MCP.RevokeAllTokens()
		if err != nil {
			return Result{}, "", err
		}
		return Result{Count: n, Message: fmt.Sprintf("revoked %d MCP %s", n, plural(n, "token", "tokens"))}, "", nil
	case Purge:
		res, err := o.purge(ctx, req.Scopes, req.Indexes)
		return res, strings.Join(req.Scopes, ","), err
	}
	return Result{}, "", fmt.Errorf("unknown command %q", req.Command)
}

// user finds an account by email.
func (o *Ops) user(email string) (auth.User, error) {
	if strings.TrimSpace(email) == "" {
		return auth.User{}, errors.New("--email is required")
	}
	u, ok, err := o.d.Auth.UserByEmail(email)
	if err == nil && !ok {
		err = fmt.Errorf("no user with email %s", strings.ToLower(strings.TrimSpace(email)))
	}
	return u, err
}

func publicPtr(u auth.User) *auth.PublicUser {
	p := u.Public()
	return &p
}

func (o *Ops) createUser(req Request) (Result, error) {
	role, err := auth.ParseRole(req.Role)
	if err != nil {
		return Result{}, err
	}
	u, err := o.d.Auth.CreateUser(req.Email, req.Name, role, req.Password)
	if err != nil {
		return Result{}, err
	}
	msg := fmt.Sprintf("created %s (%s)", u.Email, u.Role)
	if u.Owner {
		msg += " as the primary administrator"
	}
	return Result{User: publicPtr(u), Message: msg}, nil
}

func (o *Ops) resetPassword(req Request) (Result, error) {
	u, err := o.user(req.Email)
	if err != nil {
		return Result{}, err
	}
	// The operator may reset anyone, the primary administrator included.
	if err := o.d.Auth.SetPassword(u.ID, req.Password, ""); err != nil {
		return Result{}, err
	}
	return Result{User: publicPtr(u), Message: fmt.Sprintf("reset the password of %s and signed them out everywhere", u.Email)}, nil
}

func (o *Ops) setRole(req Request) (Result, error) {
	u, err := o.user(req.Email)
	if err != nil {
		return Result{}, err
	}
	role, err := auth.ParseRole(req.Role)
	if err != nil {
		return Result{}, err
	}
	if u, err = o.d.Auth.UpdateUser("", u.ID, nil, &role); err != nil {
		return Result{}, err
	}
	return Result{User: publicPtr(u), Message: fmt.Sprintf("%s is now %s", u.Email, u.Role)}, nil
}

func (o *Ops) setOwner(req Request) (Result, error) {
	u, err := o.user(req.Email)
	if err != nil {
		return Result{}, err
	}
	if u, err = o.d.Auth.SetOwner(u.ID); err != nil {
		return Result{}, err
	}
	return Result{User: publicPtr(u), Message: fmt.Sprintf("%s is now the primary administrator", u.Email)}, nil
}

func (o *Ops) deleteUser(req Request) (Result, error) {
	u, err := o.user(req.Email)
	if err != nil {
		return Result{}, err
	}
	if err := o.d.Auth.DeleteUser(u.ID, ""); err != nil {
		return Result{}, err
	}
	return Result{Message: fmt.Sprintf("deleted %s and signed them out", u.Email)}, nil
}

func (o *Ops) revokeSessions(req Request) (Result, error) {
	if strings.TrimSpace(req.Email) == "" {
		n, err := o.d.Auth.SignOutEveryone()
		if err != nil {
			return Result{}, err
		}
		return Result{Count: n, Message: fmt.Sprintf("revoked %d %s: everyone is signed out", n, plural(n, "session", "sessions"))}, nil
	}
	u, err := o.user(req.Email)
	if err != nil {
		return Result{}, err
	}
	n, err := o.d.Auth.SignOut(u.ID)
	if err != nil {
		return Result{}, err
	}
	return Result{Count: n, Message: fmt.Sprintf("revoked %d %s of %s", n, plural(n, "session", "sessions"), u.Email)}, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// GeneratePassword returns a random password (144 bits).
func GeneratePassword() string { return auth.NewToken("", 18) }
