package signature

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignVerify(t *testing.T) {
	now := time.Unix(1_767_225_600, 0)
	body := []byte(`{"op":"upsert","row":{"id":1}}`)
	h := http.Header{}
	SetHeaders(h, "s3cret-s3cret-s3cret", now, body)

	assert.Equal(t, "1767225600", h.Get(HeaderTimestamp))
	assert.True(t, strings.HasPrefix(h.Get(HeaderSignature), "sha256="))
	require.NoError(t, Verify(h, "s3cret-s3cret-s3cret", now.Add(time.Minute), body))

	assert.ErrorIs(t, Verify(h, "other-secret-value", now, body), ErrMismatch)
	assert.ErrorIs(t, Verify(h, "s3cret-s3cret-s3cret", now, []byte(`{"op":"delete"}`)), ErrMismatch)
	assert.ErrorIs(t, Verify(h, "s3cret-s3cret-s3cret", now.Add(6*time.Minute), body), ErrExpired)
	assert.ErrorIs(t, Verify(h, "s3cret-s3cret-s3cret", now.Add(-6*time.Minute), body), ErrExpired)
	assert.ErrorIs(t, Verify(http.Header{}, "s3cret-s3cret-s3cret", now, body), ErrMissing)

	// A replayed signature with a new timestamp does not match.
	h2 := h.Clone()
	h2.Set(HeaderTimestamp, strconv.FormatInt(now.Unix()+60, 10))
	assert.ErrorIs(t, Verify(h2, "s3cret-s3cret-s3cret", now, body), ErrMismatch)

	// Rotation: one of several signatures matches; case does not matter.
	h3 := h.Clone()
	h3.Set(HeaderSignature, "sha256=deadbeef, "+strings.ToUpper(h.Get(HeaderSignature)[7:]))
	assert.ErrorIs(t, Verify(h3, "s3cret-s3cret-s3cret", now, body), ErrMismatch, "the scheme prefix is required")
	h3.Set(HeaderSignature, "sha256=deadbeef, SHA256="+strings.ToUpper(h.Get(HeaderSignature)[7:]))
	assert.NoError(t, Verify(h3, "s3cret-s3cret-s3cret", now, body))
}

func TestGenerate(t *testing.T) {
	a, b := Generate(), Generate()
	assert.NotEqual(t, a, b)
	assert.True(t, strings.HasPrefix(a, "whsec_"))
	assert.GreaterOrEqual(t, len(a), MinSecretLen)
}
