package auth

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

func newService(t *testing.T) *Service {
	db, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := NewService(db)
	require.NoError(t, err)
	return s
}

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword("correct horse")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(h, "$argon2id$v=19$"))
	ok, err := VerifyPassword("correct horse", h)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, _ = VerifyPassword("wrong horse", h)
	assert.False(t, ok)
	h2, _ := HashPassword("correct horse")
	assert.NotEqual(t, h, h2, "salted")
	_, err = VerifyPassword("x", "$bcrypt$nope")
	assert.Error(t, err)
	assert.ErrorIs(t, CheckPassword("short"), ErrWeakPassword)
	assert.ErrorIs(t, CheckPassword(strings.Repeat("x", 129)), ErrWeakPassword)
	assert.NoError(t, CheckPassword("long enough"))
}

func TestRoles(t *testing.T) {
	assert.True(t, Admin.AtLeast(Moderator))
	assert.True(t, Moderator.AtLeast(Viewer))
	assert.False(t, Viewer.AtLeast(Moderator))
	assert.False(t, Role("root").AtLeast(Viewer))
	_, err := ParseRole("root")
	assert.Error(t, err)
	var p *Principal
	assert.False(t, p.Can(Viewer))
}

func TestSetupLoginAndSessions(t *testing.T) {
	s := newService(t)
	has, _ := s.HasUsers()
	assert.False(t, has)
	admin, err := s.Setup("Admin@Example.com ", "Ada", "password123")
	require.NoError(t, err)
	assert.Equal(t, "admin@example.com", admin.Email)
	assert.Equal(t, Admin, admin.Role)
	_, err = s.Setup("other@example.com", "x", "password123")
	assert.ErrorIs(t, err, ErrConflict)

	_, err = s.Authenticate("admin@example.com", "nope-nope")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	_, err = s.Authenticate("ghost@example.com", "password123")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	u, err := s.Authenticate("ADMIN@example.com", "password123")
	require.NoError(t, err)

	tok, _, err := s.CreateSession(u.ID, "1.2.3.4", "test")
	require.NoError(t, err)
	p, err := s.ResolveSession(tok)
	require.NoError(t, err)
	assert.Equal(t, Admin, p.Role)
	assert.Equal(t, "session", p.Kind)
	_, err = s.ResolveSession("forged")
	assert.ErrorIs(t, err, ErrInvalidToken)

	// Expiry.
	s.now = func() time.Time { return time.Now().Add(SessionTTL + time.Minute) }
	_, err = s.ResolveSession(tok)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

func TestUserManagementGuards(t *testing.T) {
	s := newService(t)
	admin, err := s.Setup("a@x.io", "A", "password123")
	require.NoError(t, err)
	mod, err := s.CreateUser("m@x.io", "M", Moderator, "password123")
	require.NoError(t, err)
	_, err = s.CreateUser("m@x.io", "dup", Viewer, "password123")
	assert.ErrorIs(t, err, ErrConflict)
	_, err = s.CreateUser("not-an-email", "x", Viewer, "password123")
	assert.Error(t, err)
	_, err = s.CreateUser("v@x.io", "x", "root", "password123")
	assert.Error(t, err)

	viewer := Viewer
	_, err = s.UpdateUser("", admin.ID, nil, &viewer)
	assert.ErrorIs(t, err, ErrForbidden, "the owner keeps the admin role")
	assert.ErrorIs(t, s.DeleteUser(admin.ID, admin.ID), ErrConflict, "no self delete")
	assert.ErrorIs(t, s.DeleteUser(admin.ID, mod.ID), ErrForbidden, "the owner cannot be deleted")

	name := "Mo"
	adminRole := Admin
	up, err := s.UpdateUser(admin.ID, mod.ID, &name, &adminRole)
	require.NoError(t, err)
	assert.Equal(t, "Mo", up.Name)
	assert.Equal(t, Admin, up.Role)
	assert.False(t, up.Owner)
	_, err = s.SetOwner(mod.ID)
	require.NoError(t, err)
	require.NoError(t, s.DeleteUser(admin.ID, mod.ID), "a former owner is an ordinary admin")
	_, ok, _ := s.UserByEmail("a@x.io")
	assert.False(t, ok)
	users, _ := s.Users()
	assert.Len(t, users, 1)
	_, err = s.UpdateUser("", mod.ID, nil, &viewer)
	assert.ErrorIs(t, err, ErrForbidden)
}

func TestOwnerRules(t *testing.T) {
	s := newService(t)
	owner, err := s.Setup("o@x.io", "O", "password123")
	require.NoError(t, err)
	assert.True(t, owner.Owner, "the first admin is the owner")
	assert.True(t, owner.Public().Owner)
	other, err := s.CreateUser("b@x.io", "B", Admin, "password123")
	require.NoError(t, err)
	assert.False(t, other.Owner)
	view, err := s.CreateUser("v@x.io", "V", Viewer, "password123")
	require.NoError(t, err)

	mod := Moderator
	_, err = s.UpdateUser(other.ID, other.ID, nil, &mod)
	assert.ErrorIs(t, err, ErrForbidden, "no admin changes their own role")
	assert.Contains(t, err.Error(), "your own role")
	_, err = s.UpdateUser(owner.ID, owner.ID, nil, &mod)
	assert.ErrorIs(t, err, ErrForbidden)
	_, err = s.UpdateUser(other.ID, owner.ID, nil, &mod)
	assert.ErrorIs(t, err, ErrForbidden, "nobody changes the owner's role")
	assert.Contains(t, err.Error(), "set-owner")
	same := Admin
	_, err = s.UpdateUser(owner.ID, owner.ID, nil, &same)
	assert.NoError(t, err, "an unchanged role is not a change")
	name := "Olivia"
	u, err := s.UpdateUser(owner.ID, owner.ID, &name, nil)
	require.NoError(t, err)
	assert.True(t, u.Owner, "renaming keeps ownership")
	_, err = s.UpdateUser(owner.ID, view.ID, nil, &mod)
	assert.NoError(t, err, "admins change other users' roles")

	assert.ErrorIs(t, s.DeleteUser(owner.ID, other.ID), ErrForbidden)
	assert.ErrorIs(t, s.DeleteUser(owner.ID, ""), ErrForbidden, "not even the CLI deletes the owner")
	assert.ErrorIs(t, s.SetUserPassword(other.ID, owner.ID, "password456", ""), ErrForbidden)
	assert.NoError(t, s.SetUserPassword(owner.ID, owner.ID, "password456", ""), "the owner sets their own")
	assert.NoError(t, s.SetUserPassword(owner.ID, other.ID, "password456", ""))

	_, err = s.SetOwner(view.ID)
	assert.ErrorIs(t, err, ErrConflict, "the owner must be an admin")
	_, err = s.SetOwner(other.ID)
	require.NoError(t, err)
	got, ok, err := s.Owner()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, other.ID, got.ID)
	prev, _ := s.User(owner.ID)
	assert.False(t, prev.Owner, "there is one owner")
	_, err = s.UpdateUser(other.ID, owner.ID, nil, &mod)
	assert.NoError(t, err, "the former owner is an ordinary admin")
}

func TestOwnerMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	users, err := store.NewBucket[User](db, "users")
	require.NoError(t, err)
	now := time.Now().UTC()
	// Data from before owners existed: the oldest admin becomes the owner.
	require.NoError(t, users.PutMany(map[string]User{
		"v": {ID: "v", Email: "v@x.io", Role: Viewer, CreatedAt: now.Add(-3 * time.Hour)},
		"a": {ID: "a", Email: "a@x.io", Role: Admin, CreatedAt: now.Add(-2 * time.Hour)},
		"b": {ID: "b", Email: "b@x.io", Role: Admin, CreatedAt: now.Add(-time.Hour)},
	}))
	s, err := NewService(db)
	require.NoError(t, err)
	o, ok, err := s.Owner()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "a", o.ID)
	list, _ := s.Users()
	owners := 0
	for _, u := range list {
		if u.Owner {
			owners++
		}
	}
	assert.Equal(t, 1, owners)
	require.NoError(t, db.Close())
}

func TestPurgeUsersShowsSetupAgain(t *testing.T) {
	s := newService(t)
	u, _ := s.Setup("a@x.io", "A", "password123")
	tok, _, _ := s.CreateSession(u.ID, "", "")
	_, _, err := s.CreateInvitation("i@x.io", Viewer, "a@x.io")
	require.NoError(t, err)
	n, err := s.PurgeUsers()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	has, _ := s.HasUsers()
	assert.False(t, has)
	_, err = s.ResolveSession(tok)
	assert.ErrorIs(t, err, ErrInvalidToken)
	inv, _ := s.Invitations()
	assert.Empty(t, inv)
	again, err := s.Setup("b@x.io", "B", "password123")
	require.NoError(t, err)
	assert.True(t, again.Owner)
}

func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	s := newService(t)
	u, _ := s.Setup("a@x.io", "A", "password123")
	t1, _, _ := s.CreateSession(u.ID, "", "")
	t2, _, _ := s.CreateSession(u.ID, "", "")
	assert.ErrorIs(t, s.ChangePassword(u.ID, "wrong-pass", "newpassword1", TokenHash(t1)), ErrInvalidCredentials)
	require.NoError(t, s.ChangePassword(u.ID, "password123", "newpassword1", TokenHash(t1)))
	_, err := s.ResolveSession(t1)
	assert.NoError(t, err, "the current session survives")
	_, err = s.ResolveSession(t2)
	assert.ErrorIs(t, err, ErrInvalidToken)
	_, err = s.Authenticate("a@x.io", "newpassword1")
	assert.NoError(t, err)
}

func TestInvitations(t *testing.T) {
	s := newService(t)
	admin, _ := s.Setup("a@x.io", "A", "password123")
	_, _, err := s.CreateInvitation("a@x.io", Viewer, admin.ID)
	assert.ErrorIs(t, err, ErrConflict, "existing account")
	inv, tok, err := s.CreateInvitation("New@X.io", Moderator, admin.ID)
	require.NoError(t, err)
	assert.Equal(t, "new@x.io", inv.Email)
	list, _ := s.Invitations()
	assert.Len(t, list, 1)

	got, err := s.Invitation(tok)
	require.NoError(t, err)
	assert.Equal(t, Moderator, got.Role)
	_, err = s.AcceptInvitation(tok, "Newbie", "short")
	assert.ErrorIs(t, err, ErrWeakPassword)
	u, err := s.AcceptInvitation(tok, "Newbie", "password123")
	require.NoError(t, err)
	assert.Equal(t, Moderator, u.Role)
	_, err = s.AcceptInvitation(tok, "again", "password123")
	assert.ErrorIs(t, err, ErrInvalidToken, "single use")

	inv2, tok2, _ := s.CreateInvitation("late@x.io", Viewer, admin.ID)
	require.NoError(t, s.RevokeInvitation(inv2.ID))
	_, err = s.Invitation(tok2)
	assert.ErrorIs(t, err, ErrInvalidToken)

	_, tok3, _ := s.CreateInvitation("old@x.io", Viewer, admin.ID)
	s.now = func() time.Time { return time.Now().Add(InvitationTTL + time.Hour) }
	_, err = s.Invitation(tok3)
	assert.ErrorIs(t, err, ErrInvalidToken)
	list, _ = s.Invitations()
	assert.Empty(t, list, "expired invitations are dropped")
}

func TestPasswordReset(t *testing.T) {
	s := newService(t)
	u, _ := s.Setup("a@x.io", "A", "password123")
	sess, _, _ := s.CreateSession(u.ID, "", "")
	_, tok, err := s.CreateReset("ghost@x.io")
	require.NoError(t, err)
	assert.Empty(t, tok, "no token for unknown accounts")

	_, tok, err = s.CreateReset("A@x.io")
	require.NoError(t, err)
	require.NotEmpty(t, tok)
	require.NoError(t, s.ResetPassword(tok, "brandnewpass"))
	assert.ErrorIs(t, s.ResetPassword(tok, "brandnewpass"), ErrInvalidToken, "single use")
	_, err = s.ResolveSession(sess)
	assert.ErrorIs(t, err, ErrInvalidToken, "sessions revoked")
	_, err = s.Authenticate("a@x.io", "brandnewpass")
	assert.NoError(t, err)

	_, tok, _ = s.CreateReset("a@x.io")
	s.now = func() time.Time { return time.Now().Add(2 * ResetTTL) }
	assert.ErrorIs(t, s.ResetPassword(tok, "anotherpass1"), ErrInvalidToken)
}

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(3, time.Minute)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		blocked, _ := l.Blocked("k")
		assert.False(t, blocked)
		l.Fail("k")
	}
	blocked, wait := l.Blocked("k")
	assert.True(t, blocked)
	assert.Equal(t, time.Minute, wait)
	now = now.Add(time.Minute)
	blocked, _ = l.Blocked("k")
	assert.False(t, blocked)
	l.Fail("k")
	l.Reset("k")
	blocked, _ = l.Blocked("k")
	assert.False(t, blocked)
}
