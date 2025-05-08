// Package auth manages users, sessions, invitations, password resets and
// role based access.
package auth

import (
	"context"
	"fmt"
	"time"
)

// Role is an access level. Roles are ordered: admin > moderator > viewer.
type Role string

const (
	Viewer    Role = "viewer"
	Moderator Role = "moderator"
	Admin     Role = "admin"
)

// Roles lists every role, lowest first.
var Roles = []Role{Viewer, Moderator, Admin}

func (r Role) rank() int {
	switch r {
	case Viewer:
		return 1
	case Moderator:
		return 2
	case Admin:
		return 3
	}
	return 0
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r.rank() > 0 }

// AtLeast reports whether r grants min.
func (r Role) AtLeast(min Role) bool { return r.rank() >= min.rank() && r.Valid() }

// ParseRole validates a role name.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if !r.Valid() {
		return "", fmt.Errorf("unknown role %q (viewer, moderator or admin)", s)
	}
	return r, nil
}

// User is a stored account.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	Role         Role      `json:"role"`
	PasswordHash string    `json:"passwordHash"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	LastLoginAt  time.Time `json:"lastLoginAt,omitzero"`
	// Owner marks the primary administrator: the first admin, whose role
	// and account only the operator CLI can change.
	Owner bool `json:"owner,omitempty"`
}

// PublicUser is a user without secrets, as returned by the API.
type PublicUser struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	Role        Role      `json:"role"`
	CreatedAt   time.Time `json:"createdAt,omitzero"`
	UpdatedAt   time.Time `json:"updatedAt,omitzero"`
	LastLoginAt time.Time `json:"lastLoginAt,omitzero"`
	Owner       bool      `json:"owner,omitempty"`
}

// Public strips secrets.
func (u User) Public() PublicUser {
	return PublicUser{ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
		LastLoginAt: u.LastLoginAt, Owner: u.Owner}
}

// Session is a logged in browser. It is stored under the hash of its token.
type Session struct {
	UserID    string    `json:"userId"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	SeenAt    time.Time `json:"seenAt"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent"`
}

// Invitation lets someone create an account with a preset role.
type Invitation struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	InvitedBy string    `json:"invitedBy"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Emailed   bool      `json:"emailed"`
}

// reset is a pending password reset.
type reset struct {
	UserID    string    `json:"userId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Principal is the authenticated caller of a request.
type Principal struct {
	ID    string // user id, "api-token" or "mcp:<token id>"
	Email string
	Name  string
	Role  Role
	// Kind is "session", "token" or "mcp".
	Kind      string
	SessionID string // hash of the session token
}

// Can reports whether the principal has at least min.
func (p *Principal) Can(min Role) bool { return p != nil && p.Role.AtLeast(min) }

// Label identifies the principal in logs and the audit trail.
func (p *Principal) Label() string {
	if p == nil {
		return "anonymous"
	}
	if p.Email != "" {
		return p.Email
	}
	return p.Name
}

type ctxKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal, or nil.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}
