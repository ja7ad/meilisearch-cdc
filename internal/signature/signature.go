// Package signature signs and verifies webhook requests with a shared
// secret, in both directions: applications sign the changes they push to a
// webhook task, and the service signs the alerts it posts to webhooks.
//
// A signed request carries two headers:
//
//	X-CDC-Timestamp: 1767225600
//	X-CDC-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + body)>
//
// Binding the timestamp into the signature lets receivers refuse old
// requests (replays); Verify accepts a clock skew of Tolerance.
package signature

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// HeaderTimestamp holds the unix time (seconds) the request was signed.
	HeaderTimestamp = "X-CDC-Timestamp"
	// HeaderSignature holds "sha256=" and the hex encoded HMAC.
	HeaderSignature = "X-CDC-Signature"
	// Tolerance is the accepted difference between the signing time and now.
	Tolerance = 5 * time.Minute
	// MinSecretLen is the shortest secret accepted.
	MinSecretLen = 16

	scheme = "sha256="
)

var (
	// ErrMissing means the request is not signed.
	ErrMissing = errors.New("the request is not signed: send the X-CDC-Timestamp and X-CDC-Signature headers")
	// ErrExpired means the timestamp is too old or too far in the future.
	ErrExpired = errors.New("the signature timestamp is outside the accepted 5 minute window; check the sender's clock")
	// ErrMismatch means the signature does not match the body.
	ErrMismatch = errors.New("the signature does not match: sign timestamp + \".\" + the raw body with the task's secret")
)

// Sign returns the X-CDC-Signature value for body signed at ts.
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return scheme + hex.EncodeToString(mac.Sum(nil))
}

// SetHeaders signs body at now and sets both headers on h.
func SetHeaders(h http.Header, secret string, now time.Time, body []byte) {
	ts := now.Unix()
	h.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(HeaderSignature, Sign(secret, ts, body))
}

// Verify checks the signature headers of a request against body. Several
// comma separated signatures are accepted, so senders can rotate secrets.
func Verify(h http.Header, secret string, now time.Time, body []byte) error {
	tsRaw, sigRaw := strings.TrimSpace(h.Get(HeaderTimestamp)), strings.TrimSpace(h.Get(HeaderSignature))
	if tsRaw == "" || sigRaw == "" {
		return ErrMissing
	}
	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return ErrMismatch
	}
	if d := now.Sub(time.Unix(ts, 0)); d > Tolerance || d < -Tolerance {
		return ErrExpired
	}
	want := []byte(Sign(secret, ts, body))
	for _, sig := range strings.Split(sigRaw, ",") {
		if hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(sig))), want) {
			return nil
		}
	}
	return ErrMismatch
}

// Generate returns a new random secret.
func Generate() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b)
}
