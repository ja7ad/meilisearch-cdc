package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (OWASP recommendation: 19 MiB, 2 iterations).
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
)

// Password length bounds.
const (
	MinPassword = 8
	MaxPassword = 128
)

// ErrWeakPassword is returned for passwords outside the length bounds.
var ErrWeakPassword = fmt.Errorf("password must be %d to %d characters", MinPassword, MaxPassword)

// CheckPassword validates a new password.
func CheckPassword(p string) error {
	if n := len([]rune(p)); n < MinPassword || n > MaxPassword {
		return ErrWeakPassword
	}
	return nil
}

// HashPassword returns an encoded argon2id hash.
func HashPassword(p string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	pw := []byte(p)
	key := argon2.IDKey(pw, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	clear(pw)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

var errBadHash = errors.New("malformed password hash")

// VerifyPassword compares p with an encoded hash in constant time.
func VerifyPassword(p, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var m, t uint32
	var th uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &th); err != nil {
		return false, errBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errBadHash
	}
	pw := []byte(p)
	got := argon2.IDKey(pw, salt, t, m, th, uint32(len(want)))
	clear(pw)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against for unknown emails so login timing does
// not reveal which accounts exist.
var dummyHash, _ = HashPassword("not-a-real-password")

// NewToken returns a URL-safe random token with an optional prefix.
func NewToken(prefix string, bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// TokenHash is the stored form of a token: tokens are high entropy, so a
// plain SHA-256 is sufficient and allows lookups by hash.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewID returns a short random identifier.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
