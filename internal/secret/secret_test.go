package secret

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoundTripWithAAD(t *testing.T) {
	c, err := New("passphrase")
	require.NoError(t, err)
	enc, err := c.Encrypt("postgres://u:p@h/db", "task:a:dsn")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(enc, "enc:v2:"))
	assert.NotContains(t, enc, "u:p")
	again, _ := c.Encrypt(enc, "task:a:dsn")
	assert.Equal(t, enc, again, "already encrypted values are kept")

	dec, err := c.Decrypt(enc, "task:a:dsn")
	require.NoError(t, err)
	assert.Equal(t, "postgres://u:p@h/db", dec)

	_, err = c.Decrypt(enc, "task:b:dsn")
	assert.Error(t, err, "a ciphertext moved to another record does not decrypt")

	other, _ := New("other")
	_, err = other.Decrypt(enc, "task:a:dsn")
	assert.ErrorIs(t, err, ErrUnknownKey)

	var none *Cipher
	_, err = none.Decrypt(enc, "task:a:dsn")
	assert.ErrorIs(t, err, ErrNoKey)
	plain, _ := none.Encrypt("x", "")
	assert.Equal(t, "x", plain)
	empty, _ := c.Encrypt("", "")
	assert.Equal(t, "", empty)
	passthrough, err := c.Decrypt("not encrypted", "")
	require.NoError(t, err)
	assert.Equal(t, "not encrypted", passthrough)
}

func TestRawKeyAndDerivation(t *testing.T) {
	raw := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	assert.Equal(t, []byte("0123456789abcdef0123456789abcdef"), DeriveKey(raw))
	k1, k2 := DeriveKey("pass"), DeriveKey("pass")
	assert.Equal(t, k1, k2, "deterministic")
	assert.Len(t, k1, 32)
	assert.NotEqual(t, k1, DeriveKey("pass2"))
}

func TestRotation(t *testing.T) {
	old, _ := New("old-key")
	enc, _ := old.Encrypt("secret", "x")

	ring, err := New("new-key", "old-key")
	require.NoError(t, err)
	dec, err := ring.Decrypt(enc, "x")
	require.NoError(t, err, "previous keys still decrypt")
	assert.Equal(t, "secret", dec)
	assert.True(t, ring.NeedsRewrap(enc))
	assert.True(t, ring.NeedsRewrap("plaintext"))
	assert.False(t, ring.NeedsRewrap(""))

	re, err := ring.Rewrap(enc, "x")
	require.NoError(t, err)
	assert.False(t, ring.NeedsRewrap(re))
	only, _ := New("new-key")
	dec, err = only.Decrypt(re, "x")
	require.NoError(t, err)
	assert.Equal(t, "secret", dec)
}
