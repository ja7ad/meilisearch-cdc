// Package secret encrypts credentials at rest with AES-256-GCM.
//
// Keys come from CDC_SECRET_KEY: either a base64 encoded 32 byte key, or a
// passphrase stretched with argon2id. Several keys form a ring: the first
// encrypts, every key decrypts, so a key can be rotated by moving the old
// one to CDC_SECRET_KEY_PREVIOUS and re-encrypting (NeedsRewrap reports
// which values still use an old key).
//
// Ciphertexts are bound to their owner and field with associated data
// (for example "task:abc:dsn"), so a stored value cannot be copied into
// another record and decrypted there.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	prefixV2 = "enc:v2:" // enc:v2:<key id>:<base64(nonce|ciphertext)>
	prefixV1 = "enc:v1:" // legacy: no key id, no associated data
)

// kdfSalt is fixed: the passphrase is the only secret input, and a
// deployment specific salt would make the key unrecoverable from it.
var kdfSalt = []byte("meilisearch-cdc/secret-key/v2")

type key struct {
	id   string
	aead cipher.AEAD
}

// Cipher encrypts and decrypts strings. A nil Cipher stores plaintext,
// which is only acceptable for development.
type Cipher struct {
	keys []key // keys[0] encrypts
}

// DeriveKey turns CDC_SECRET_KEY into 32 key bytes: base64 of exactly 32
// bytes is used as is, anything else is a passphrase for argon2id.
func DeriveKey(material string) []byte {
	if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(material)); err == nil && len(raw) == 32 {
		return raw
	}
	pw := []byte(material)
	defer clear(pw)
	// OWASP argon2id profile with the lowest memory: 19 MiB, 2 passes.
	return argon2.IDKey(pw, kdfSalt, 2, 19*1024, 1, 32)
}

func newKey(material string) (key, error) {
	k := DeriveKey(material)
	defer clear(k)
	sum := sha256.Sum256(k)
	block, err := aes.NewCipher(k)
	if err != nil {
		return key{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return key{}, err
	}
	return key{id: hex.EncodeToString(sum[:4]), aead: aead}, nil
}

// New builds a key ring from the current key and optional previous keys.
// An empty current key returns a nil Cipher that passes values through.
func New(current string, previous ...string) (*Cipher, error) {
	if current == "" {
		return nil, nil
	}
	c := &Cipher{}
	for _, m := range append([]string{current}, previous...) {
		if m == "" {
			continue
		}
		k, err := newKey(m)
		if err != nil {
			return nil, err
		}
		c.keys = append(c.keys, k)
	}
	return c, nil
}

// Enabled reports whether values are encrypted.
func (c *Cipher) Enabled() bool { return c != nil && len(c.keys) > 0 }

// IsEncrypted reports whether v is a ciphertext produced by this package.
func IsEncrypted(v string) bool {
	return strings.HasPrefix(v, prefixV2) || strings.HasPrefix(v, prefixV1)
}

// Encrypt seals plain, bound to aad. Empty and already encrypted values
// are returned unchanged.
func (c *Cipher) Encrypt(plain, aad string) (string, error) {
	if !c.Enabled() || plain == "" || IsEncrypted(plain) {
		return plain, nil
	}
	k := c.keys[0]
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	buf := []byte(plain)
	sealed := k.aead.Seal(nonce, nonce, buf, []byte(aad))
	clear(buf)
	return prefixV2 + k.id + ":" + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// ErrNoKey is returned when an encrypted value is read without a key.
var ErrNoKey = errors.New("value is encrypted but CDC_SECRET_KEY is not set")

// ErrUnknownKey is returned when no key of the ring matches the value.
var ErrUnknownKey = errors.New("value was encrypted with a key that is not configured (set CDC_SECRET_KEY_PREVIOUS)")

// Decrypt reverses Encrypt with the same aad. Values without the prefix
// are returned as is.
func (c *Cipher) Decrypt(v, aad string) (string, error) {
	switch {
	case strings.HasPrefix(v, prefixV2):
		if !c.Enabled() {
			return "", ErrNoKey
		}
		id, body, ok := strings.Cut(strings.TrimPrefix(v, prefixV2), ":")
		if !ok {
			return "", errors.New("malformed ciphertext")
		}
		for _, k := range c.keys {
			if k.id == id {
				return open(k.aead, body, []byte(aad))
			}
		}
		return "", ErrUnknownKey
	case strings.HasPrefix(v, prefixV1):
		if !c.Enabled() {
			return "", ErrNoKey
		}
		for _, k := range c.keys {
			if p, err := open(k.aead, strings.TrimPrefix(v, prefixV1), nil); err == nil {
				return p, nil
			}
		}
		return "", ErrUnknownKey
	}
	return v, nil
}

func open(aead cipher.AEAD, body string, aad []byte) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("malformed ciphertext: %w", err)
	}
	n := aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("ciphertext too short")
	}
	plain, err := aead.Open(nil, raw[:n], raw[n:], aad)
	if err != nil {
		return "", errors.New("cannot decrypt secret: wrong key or the value belongs to another record")
	}
	out := string(plain)
	clear(plain)
	return out, nil
}

// NeedsRewrap reports whether v should be re-encrypted with the current
// key (older key, legacy format, or plaintext while a key is configured).
func (c *Cipher) NeedsRewrap(v string) bool {
	if !c.Enabled() || v == "" {
		return false
	}
	if !strings.HasPrefix(v, prefixV2) {
		return true
	}
	id, _, _ := strings.Cut(strings.TrimPrefix(v, prefixV2), ":")
	return id != c.keys[0].id
}

// Rewrap decrypts v with any key of the ring and encrypts it with the
// current one.
func (c *Cipher) Rewrap(v, aad string) (string, error) {
	if !c.NeedsRewrap(v) {
		return v, nil
	}
	plain, err := c.Decrypt(v, aad)
	if err != nil {
		return "", err
	}
	return c.Encrypt(plain, aad)
}

// LoadOrCreateKeyFile returns the key stored at path, creating a random
// 256 bit key (mode 0600) the first time. It lets the service encrypt
// credentials without any configuration; back the file up with the data.
func LoadOrCreateKeyFile(path string) (string, error) {
	if raw, err := os.ReadFile(path); err == nil { // #nosec G304 -- operator-supplied key file path
		key := strings.TrimSpace(string(raw))
		if key == "" {
			return "", fmt.Errorf("%s is empty", path)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := base64.StdEncoding.EncodeToString(b)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- operator-supplied key file path
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(key + "\n"); err != nil {
		f.Close()
		return "", err
	}
	return key, f.Close()
}
