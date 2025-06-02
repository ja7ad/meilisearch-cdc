package sink

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/meilisearch/meilisearch-go"
)

// ErrHold is returned when a safety guard stops the pipeline. The batch is
// not applied and not acknowledged, so nothing is lost.
type ErrHold struct{ Reason string }

func (e *ErrHold) Error() string { return "safety hold: " + e.Reason }

// IsHold reports whether err is a safety hold.
func IsHold(err error) bool {
	var h *ErrHold
	return errors.As(err, &h)
}

// errorKind classifies a failed request or task.
type errorKind int

const (
	kindRetry    errorKind = iota // transient: network, 5xx, 429, full queue
	kindDocument                  // a document in the payload is invalid
	kindTooLarge                  // payload too large: split the batch
	kindFatal                     // configuration problem: stop the task
)

// documentCodes are task error codes caused by the content of a document,
// so bisecting the batch can isolate the offender.
func isDocumentCode(code string) bool {
	switch meilisearch.APIErrCode(code) {
	case meilisearch.APIErrCodeInvalidDocumentID,
		meilisearch.APIErrCodeMissingDocumentID,
		meilisearch.APIErrCodeInvalidDocumentGeoField,
		meilisearch.APIErrCodeInvalidDocumentGeoJSONField,
		meilisearch.APIErrCodeDocumentFieldsLimitReached,
		meilisearch.APIErrCodeIndexPrimaryKeyMultipleCandidatesFound:
		return true
	}
	return strings.HasPrefix(code, "invalid_document") || strings.Contains(code, "vector") ||
		code == "malformed_payload"
}

var fatalCodes = map[string]bool{
	"invalid_api_key":                      true,
	"missing_authorization_header":         true,
	"invalid_index_primary_key":            true,
	"index_primary_key_already_exists":     true,
	"index_primary_key_no_candidate_found": true,
	"invalid_index_uid":                    true,
	"index_not_accessible":                 true,
}

// documentParseError matches the deserialization errors Meilisearch reports
// with the generic "internal" code when a document field has the wrong
// shape, e.g. `invalid type: string "x", expected a map at line 1 column 5`
// for a malformed _vectors. Retrying such a batch can never succeed.
var documentParseError = regexp.MustCompile(`invalid (type|value|length): .*expected|missing field ` + "`" + `|unknown (field|variant) ` + "`")

// classifyTaskError classifies a failed Meilisearch task from its error
// code and message.
func classifyTaskError(code, message string) errorKind {
	switch {
	case code == string(meilisearch.APIErrCodePayloadTooLarge):
		return kindTooLarge
	case isDocumentCode(code):
		return kindDocument
	case fatalCodes[code]:
		return kindFatal
	case code == "internal" && documentParseError.MatchString(message):
		return kindDocument
	}
	return kindRetry
}

// classifyRequestError classifies an error returned while enqueuing.
func classifyRequestError(err error) errorKind {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return kindFatal
	}
	var me *meilisearch.Error
	if !errors.As(err, &me) {
		return kindRetry
	}
	switch {
	case me.StatusCode == 0:
		return kindRetry // communication error
	case me.StatusCode == http.StatusRequestEntityTooLarge || me.APIError.Code == meilisearch.APIErrCodePayloadTooLarge:
		return kindTooLarge
	case me.StatusCode == http.StatusTooManyRequests || me.StatusCode == http.StatusRequestTimeout || me.StatusCode >= 500:
		return kindRetry
	case me.StatusCode == http.StatusUnauthorized || me.StatusCode == http.StatusForbidden:
		return kindFatal
	case isDocumentCode(string(me.APIError.Code)):
		return kindDocument
	}
	return kindFatal
}

// errCode extracts the Meilisearch error code from err, if any.
func errCode(err error) string {
	var me *meilisearch.Error
	if errors.As(err, &me) {
		return string(me.APIError.Code)
	}
	return ""
}
