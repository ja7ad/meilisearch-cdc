package auth

import (
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// Lifetimes.
const (
	SessionTTL    = 7 * 24 * time.Hour
	InvitationTTL = 7 * 24 * time.Hour
	ResetTTL      = time.Hour
	// sessions are extended at most this often (sliding expiry).
	sessionTouch = time.Hour
)

// Errors returned by the service.
var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("conflict")
	ErrInvalidToken       = errors.New("the link is invalid or has expired")
	ErrSetupDone          = errors.New("setup is already done")
	// ErrForbidden refuses changes no API caller may make, whatever its role.
	ErrForbidden = errors.New("forbidden")
)

// ownerHint tells how ownership moves; only the operator CLI can do it.
const ownerHint = "transfer ownership first with `meilisearch-cdc admin users set-owner`"

// Service owns accounts. It is safe for concurrent use.
type Service struct {
	mu       sync.Mutex // serialises writes that must check uniqueness
	users    *store.Bucket[User]
	emails   *store.Bucket[string] // email -> user id
	sessions *store.Bucket[Session]
	invites  *store.Bucket[Invitation]
	resets   *store.Bucket[reset]
	now      func() time.Time
}

// NewService opens the auth buckets.
func NewService(db *store.DB) (*Service, error) {
	s := &Service{now: time.Now}
	var err error
	if s.users, err = store.NewBucket[User](db, "users"); err != nil {
		return nil, err
	}
	if s.emails, err = store.NewBucket[string](db, "user_emails"); err != nil {
		return nil, err
	}
	if s.sessions, err = store.NewBucket[Session](db, "sessions"); err != nil {
		return nil, err
	}
	if s.invites, err = store.NewBucket[Invitation](db, "invitations"); err != nil {
		return nil, err
	}
	if s.resets, err = store.NewBucket[reset](db, "password_resets"); err != nil {
		return nil, err
	}
	// Data written before owners existed gets one: the oldest admin.
	if err := s.ensureOwner(); err != nil {
		return nil, err
	}
	return s, nil
}

// ensureOwner records the primary administrator when there is none (the
// oldest admin) and drops extra owner flags. Callers hold s.mu.
func (s *Service) ensureOwner() error {
	var owners, admins []User
	if err := s.users.ForEach("", func(_ string, u User) bool {
		if u.Role == Admin {
			admins = append(admins, u)
		}
		if u.Owner {
			owners = append(owners, u)
		}
		return true
	}); err != nil {
		return err
	}
	oldest := func(l []User) User {
		return slices.MinFunc(l, func(a, b User) int {
			if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
	}
	keep := ""
	if valid := slices.DeleteFunc(slices.Clone(owners), func(u User) bool { return u.Role != Admin }); len(valid) > 0 {
		keep = oldest(valid).ID
	} else if len(admins) > 0 {
		keep = oldest(admins).ID
	}
	changes := map[string]User{}
	for _, u := range owners {
		if u.ID != keep {
			u.Owner = false
			changes[u.ID] = u
		}
	}
	if keep != "" && !slices.ContainsFunc(owners, func(u User) bool { return u.ID == keep }) {
		u, _, err := s.users.Get(keep)
		if err != nil {
			return err
		}
		u.Owner = true
		changes[u.ID] = u
	}
	if len(changes) == 0 {
		return nil
	}
	return s.users.PutMany(changes)
}

// Owner returns the primary administrator, if any.
func (s *Service) Owner() (User, bool, error) {
	var owner User
	found := false
	err := s.users.ForEach("", func(_ string, u User) bool {
		if u.Owner {
			owner, found = u, true
		}
		return !found
	})
	return owner, found, err
}

// SetOwner makes an admin the primary administrator. Only the operator CLI
// offers it.
func (s *Service) SetOwner(id string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.User(id)
	if err != nil {
		return u, err
	}
	if u.Role != Admin {
		return u, fmt.Errorf("%w: the primary administrator must be an admin; %s is a %s", ErrConflict, u.Email, u.Role)
	}
	now := s.now().UTC()
	changes := map[string]User{}
	if err := s.users.ForEach("", func(_ string, v User) bool {
		if v.Owner && v.ID != id {
			v.Owner, v.UpdatedAt = false, now
			changes[v.ID] = v
		}
		return true
	}); err != nil {
		return u, err
	}
	if !u.Owner {
		u.Owner, u.UpdatedAt = true, now
		changes[u.ID] = u
	}
	return u, s.users.PutMany(changes)
}

// NormalizeEmail validates and lower-cases an address.
func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email {
		return "", fmt.Errorf("invalid email address %q", email)
	}
	return email, nil
}

// HasUsers reports whether at least one account exists.
func (s *Service) HasUsers() (bool, error) {
	n, err := s.users.Count("")
	return n > 0, err
}

// Users lists every account.
func (s *Service) Users() ([]PublicUser, error) {
	all, err := s.users.List("", 0)
	if err != nil {
		return nil, err
	}
	out := make([]PublicUser, 0, len(all))
	for _, u := range all {
		out = append(out, u.Public())
	}
	return out, nil
}

// User returns one account.
func (s *Service) User(id string) (User, error) {
	u, ok, err := s.users.Get(id)
	if err != nil {
		return u, err
	}
	if !ok {
		return u, fmt.Errorf("user %w", ErrNotFound)
	}
	return u, nil
}

// UserByEmail looks an account up by email.
func (s *Service) UserByEmail(email string) (User, bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	id, ok, err := s.emails.Get(email)
	if err != nil || !ok {
		return User{}, false, err
	}
	u, ok, err := s.users.Get(id)
	return u, ok, err
}

// CreateUser adds an account.
func (s *Service) CreateUser(email, name string, role Role, password string) (User, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if !role.Valid() {
		return User{}, fmt.Errorf("unknown role %q", role)
	}
	if err := CheckPassword(password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists, err := s.emails.Get(email); err != nil {
		return User{}, err
	} else if exists {
		return User{}, fmt.Errorf("%w: an account with %s already exists", ErrConflict, email)
	}
	now := s.now().UTC()
	if strings.TrimSpace(name) == "" {
		name = strings.SplitN(email, "@", 2)[0]
	}
	u := User{ID: NewID(), Email: email, Name: strings.TrimSpace(name), Role: role, PasswordHash: hash, CreatedAt: now, UpdatedAt: now}
	if err := s.users.Put(u.ID, u); err != nil {
		return User{}, err
	}
	if err := s.emails.Put(email, u.ID); err != nil {
		return User{}, err
	}
	if err := s.ensureOwner(); err != nil {
		return User{}, err
	}
	return s.User(u.ID)
}

// Setup creates the first admin; it fails once any account exists.
func (s *Service) Setup(email, name, password string) (User, error) {
	if has, err := s.HasUsers(); err != nil {
		return User{}, err
	} else if has {
		return User{}, fmt.Errorf("%w: %w", ErrConflict, ErrSetupDone)
	}
	return s.CreateUser(email, name, Admin, password)
}

func (s *Service) admins() (int, error) {
	n := 0
	err := s.users.ForEach("", func(_ string, u User) bool {
		if u.Role == Admin {
			n++
		}
		return true
	})
	return n, err
}

// UpdateUser changes the name and/or role on behalf of actorID ("" for the
// operator CLI). Nobody changes their own role, the primary administrator's
// role never changes and the last admin cannot be demoted.
func (s *Service) UpdateUser(actorID, id string, name *string, role *Role) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.User(id)
	if err != nil {
		return u, err
	}
	if role != nil && !role.Valid() {
		return u, fmt.Errorf("unknown role %q", *role)
	}
	if role != nil && *role != u.Role {
		switch {
		case id == actorID:
			return u, fmt.Errorf("%w: you cannot change your own role", ErrForbidden)
		case u.Owner:
			return u, fmt.Errorf("%w: the primary administrator's role cannot be changed; %s", ErrForbidden, ownerHint)
		}
		if u.Role == Admin && *role != Admin {
			if n, err := s.admins(); err != nil {
				return u, err
			} else if n <= 1 {
				return u, fmt.Errorf("%w: the last admin cannot be demoted", ErrConflict)
			}
		}
		u.Role = *role
	}
	if name != nil && strings.TrimSpace(*name) != "" {
		u.Name = strings.TrimSpace(*name)
	}
	u.UpdatedAt = s.now().UTC()
	if err := s.users.Put(u.ID, u); err != nil {
		return u, err
	}
	if role != nil {
		if err := s.ensureOwner(); err != nil {
			return u, err
		}
		return s.User(u.ID)
	}
	return u, nil
}

// SetPassword replaces a password and signs the user out everywhere
// (except keepSession, the caller's own session when changing their own).
func (s *Service) SetPassword(id, password, keepSession string) error {
	if err := CheckPassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	if err := s.users.Update(id, func(u *User, exists bool) error {
		if !exists {
			return fmt.Errorf("user %w", ErrNotFound)
		}
		u.PasswordHash = hash
		u.UpdatedAt = s.now().UTC()
		return nil
	}); err != nil {
		return err
	}
	return s.RevokeUserSessions(id, keepSession)
}

// SetUserPassword is SetPassword on behalf of actorID: only the primary
// administrator sets their own password (the operator CLI can reset it).
func (s *Service) SetUserPassword(actorID, id, password, keepSession string) error {
	u, err := s.User(id)
	if err != nil {
		return err
	}
	if u.Owner && actorID != id {
		return fmt.Errorf("%w: only the primary administrator can change their password; an operator can reset it with `meilisearch-cdc admin users reset-password`", ErrForbidden)
	}
	return s.SetPassword(id, password, keepSession)
}

// ChangePassword verifies the current password first.
func (s *Service) ChangePassword(id, current, next, keepSession string) error {
	u, err := s.User(id)
	if err != nil {
		return err
	}
	if ok, _ := VerifyPassword(current, u.PasswordHash); !ok {
		return ErrInvalidCredentials
	}
	return s.SetPassword(id, next, keepSession)
}

// DeleteUser removes an account and its sessions. Deleting yourself, the
// primary administrator or the last admin is refused.
func (s *Service) DeleteUser(id, actorID string) error {
	if id == actorID {
		return fmt.Errorf("%w: you cannot delete your own account", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.User(id)
	if err != nil {
		return err
	}
	if u.Owner {
		return fmt.Errorf("%w: the primary administrator cannot be deleted; %s", ErrForbidden, ownerHint)
	}
	if u.Role == Admin {
		if n, err := s.admins(); err != nil {
			return err
		} else if n <= 1 {
			return fmt.Errorf("%w: the last admin cannot be deleted", ErrConflict)
		}
	}
	if err := s.users.Delete(id); err != nil {
		return err
	}
	if err := s.emails.Delete(u.Email); err != nil {
		return err
	}
	return s.RevokeUserSessions(id, "")
}

// Authenticate checks credentials. It spends the same time whether or not
// the email exists.
func (s *Service) Authenticate(email, password string) (User, error) {
	u, ok, err := s.UserByEmail(email)
	if err != nil {
		return User{}, err
	}
	if !ok {
		_, _ = VerifyPassword(password, dummyHash)
		return User{}, ErrInvalidCredentials
	}
	if good, _ := VerifyPassword(password, u.PasswordHash); !good {
		return User{}, ErrInvalidCredentials
	}
	_, _ = s.users.UpdateExisting(u.ID, func(v *User) error {
		v.LastLoginAt = s.now().UTC()
		return nil
	})
	return u, nil
}

// CreateSession returns a new session token for the user.
func (s *Service) CreateSession(userID, ip, userAgent string) (string, Session, error) {
	token := NewToken("", 32)
	now := s.now().UTC()
	sess := Session{UserID: userID, CreatedAt: now, ExpiresAt: now.Add(SessionTTL), SeenAt: now, IP: ip, UserAgent: truncate(userAgent, 200)}
	return token, sess, s.sessions.Put(TokenHash(token), sess)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ResolveSession returns the principal for a session token, extending the
// session when it is used.
func (s *Service) ResolveSession(token string) (*Principal, error) {
	if token == "" {
		return nil, ErrInvalidToken
	}
	key := TokenHash(token)
	sess, ok, err := s.sessions.Get(key)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if !ok || now.After(sess.ExpiresAt) {
		if ok {
			_ = s.sessions.Delete(key)
		}
		return nil, ErrInvalidToken
	}
	u, ok, err := s.users.Get(sess.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		_ = s.sessions.Delete(key)
		return nil, ErrInvalidToken
	}
	if now.Sub(sess.SeenAt) > sessionTouch {
		sess.SeenAt, sess.ExpiresAt = now, now.Add(SessionTTL)
		_ = s.sessions.Put(key, sess)
	}
	return &Principal{ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role, Kind: "session", SessionID: key}, nil
}

// RevokeSession deletes one session by token.
func (s *Service) RevokeSession(token string) error { return s.sessions.Delete(TokenHash(token)) }

// RevokeUserSessions deletes every session of a user except keep (a session hash).
func (s *Service) RevokeUserSessions(userID, keep string) error {
	_, err := s.revokeSessions(userID, keep)
	return err
}

// SignOut deletes every session of a user and returns how many there were.
func (s *Service) SignOut(userID string) (int, error) { return s.revokeSessions(userID, "") }

func (s *Service) revokeSessions(userID, keep string) (int, error) {
	var keys []string
	if err := s.sessions.ForEach("", func(k string, v Session) bool {
		if v.UserID == userID && k != keep {
			keys = append(keys, k)
		}
		return true
	}); err != nil {
		return 0, err
	}
	for _, k := range keys {
		if err := s.sessions.Delete(k); err != nil {
			return 0, err
		}
	}
	return len(keys), nil
}

// SignOutEveryone deletes every session and returns how many there were.
func (s *Service) SignOutEveryone() (int, error) { return s.sessions.Clear() }

// PurgeInvitations deletes every pending invitation.
func (s *Service) PurgeInvitations() (int, error) { return s.invites.Clear() }

// PurgeUsers deletes every account with its sessions, invitations and
// password resets; the next visit shows the setup page again.
func (s *Service) PurgeUsers() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.users.Clear()
	if err != nil {
		return n, err
	}
	for _, clear := range []func() (int, error){s.emails.Clear, s.sessions.Clear, s.invites.Clear, s.resets.Clear} {
		if _, err := clear(); err != nil {
			return n, err
		}
	}
	return n, nil
}

// CreateInvitation returns the invitation and its one-time token.
func (s *Service) CreateInvitation(email string, role Role, invitedBy string) (Invitation, string, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return Invitation{}, "", err
	}
	if !role.Valid() {
		return Invitation{}, "", fmt.Errorf("unknown role %q", role)
	}
	if _, exists, err := s.emails.Get(email); err != nil {
		return Invitation{}, "", err
	} else if exists {
		return Invitation{}, "", fmt.Errorf("%w: an account with %s already exists", ErrConflict, email)
	}
	token := NewToken("", 32)
	now := s.now().UTC()
	inv := Invitation{ID: NewID(), Email: email, Role: role, InvitedBy: invitedBy, CreatedAt: now, ExpiresAt: now.Add(InvitationTTL)}
	return inv, token, s.invites.Put(TokenHash(token), inv)
}

// MarkInvitationEmailed records that the invitation was sent by email.
func (s *Service) MarkInvitationEmailed(id string) {
	key, _ := s.invitationKey(id)
	if key != "" {
		_, _ = s.invites.UpdateExisting(key, func(v *Invitation) error { v.Emailed = true; return nil })
	}
}

// Invitations lists pending invitations and drops expired ones.
func (s *Service) Invitations() ([]Invitation, error) {
	now := s.now()
	var out []Invitation
	var expired []string
	err := s.invites.ForEach("", func(k string, v Invitation) bool {
		if now.After(v.ExpiresAt) {
			expired = append(expired, k)
		} else {
			out = append(out, v)
		}
		return true
	})
	for _, k := range expired {
		_ = s.invites.Delete(k)
	}
	if out == nil {
		out = []Invitation{}
	}
	return out, err
}

func (s *Service) invitationKey(id string) (string, error) {
	key := ""
	err := s.invites.ForEach("", func(k string, v Invitation) bool {
		if v.ID == id {
			key = k
			return false
		}
		return true
	})
	return key, err
}

// RevokeInvitation deletes an invitation by id.
func (s *Service) RevokeInvitation(id string) error {
	key, err := s.invitationKey(id)
	if err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("invitation %w", ErrNotFound)
	}
	return s.invites.Delete(key)
}

// Invitation returns a pending invitation by token.
func (s *Service) Invitation(token string) (Invitation, error) {
	inv, ok, err := s.invites.Get(TokenHash(token))
	if err != nil {
		return inv, err
	}
	if !ok || s.now().After(inv.ExpiresAt) {
		return inv, ErrInvalidToken
	}
	return inv, nil
}

// AcceptInvitation creates the account and consumes the invitation.
func (s *Service) AcceptInvitation(token, name, password string) (User, error) {
	inv, err := s.Invitation(token)
	if err != nil {
		return User{}, err
	}
	u, err := s.CreateUser(inv.Email, name, inv.Role, password)
	if err != nil {
		return User{}, err
	}
	return u, s.invites.Delete(TokenHash(token))
}

// CreateReset returns a password reset token for the email, or "" when no
// such account exists (callers must not reveal the difference).
func (s *Service) CreateReset(email string) (User, string, error) {
	u, ok, err := s.UserByEmail(email)
	if err != nil || !ok {
		return User{}, "", err
	}
	token := NewToken("", 32)
	return u, token, s.resets.Put(TokenHash(token), reset{UserID: u.ID, ExpiresAt: s.now().Add(ResetTTL)})
}

// ResetPassword consumes a reset token.
func (s *Service) ResetPassword(token, password string) error {
	if err := CheckPassword(password); err != nil {
		return err
	}
	key := TokenHash(token)
	r, ok, err := s.resets.Get(key)
	if err != nil {
		return err
	}
	if !ok || s.now().After(r.ExpiresAt) {
		return ErrInvalidToken
	}
	if err := s.resets.Delete(key); err != nil {
		return err
	}
	return s.SetPassword(r.UserID, password, "")
}

// Cleanup removes expired sessions, invitations and resets.
func (s *Service) Cleanup() {
	now := s.now()
	var sess, res []string
	_ = s.sessions.ForEach("", func(k string, v Session) bool {
		if now.After(v.ExpiresAt) {
			sess = append(sess, k)
		}
		return true
	})
	_ = s.resets.ForEach("", func(k string, v reset) bool {
		if now.After(v.ExpiresAt) {
			res = append(res, k)
		}
		return true
	})
	for _, k := range sess {
		_ = s.sessions.Delete(k)
	}
	for _, k := range res {
		_ = s.resets.Delete(k)
	}
	_, _ = s.Invitations()
}
