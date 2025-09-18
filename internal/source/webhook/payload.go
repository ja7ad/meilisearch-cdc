package webhook

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// ErrInvalidPayload wraps every ParsePayload error caused by the body (as
// opposed to reading it), so the HTTP layer can answer 400.
var ErrInvalidPayload = errors.New("webhook: invalid payload")

// change is the wire format of one pushed change.
type change struct {
	Op  string          `json:"op"`
	Row json.RawMessage `json:"row"`
}

// ParsePayload decodes a request body into events. The body is a single
// change, a JSON array of changes, or newline delimited changes (NDJSON):
//
//	{"op": "upsert", "row": {"id": 1, "name": "shoe"}}
//	{"op": "delete", "row": {"id": 2}}
//	{"op": "truncate"}
//
// op is upsert (default; "insert", "update" and "replace" are accepted as
// aliases), delete or truncate. Upserts and deletes need a non-empty row.
// Numbers become int64 when integral and float64 otherwise.
func ParsePayload(r io.Reader) ([]model.Event, error) {
	br := bufio.NewReader(r)
	first, err := firstByte(br)
	if err == io.EOF {
		return nil, fmt.Errorf("%w: empty body", ErrInvalidPayload)
	}
	if err != nil {
		return nil, fmt.Errorf("webhook: read payload: %w", err)
	}
	dec := json.NewDecoder(br)
	dec.UseNumber()

	var changes []change
	if first == '[' {
		if err := dec.Decode(&changes); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
		if dec.More() {
			return nil, fmt.Errorf("%w: unexpected data after the array", ErrInvalidPayload)
		}
		if _, err := dec.Token(); err != io.EOF {
			return nil, fmt.Errorf("%w: unexpected data after the array", ErrInvalidPayload)
		}
	} else {
		// A single object, or several objects one per line (NDJSON); the
		// decoder accepts any whitespace between values.
		for {
			var c change
			err := dec.Decode(&c)
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("%w: change %d: %v", ErrInvalidPayload, len(changes)+1, err)
			}
			changes = append(changes, c)
		}
	}
	if len(changes) == 0 {
		return nil, fmt.Errorf("%w: no changes", ErrInvalidPayload)
	}
	events := make([]model.Event, 0, len(changes))
	for i, c := range changes {
		ev, err := c.event()
		if err != nil {
			return nil, fmt.Errorf("%w: change %d: %v", ErrInvalidPayload, i+1, err)
		}
		events = append(events, ev)
	}
	return events, nil
}

func firstByte(br *bufio.Reader) (byte, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, err
		}
		if b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == 0xEF || b == 0xBB || b == 0xBF { // whitespace, UTF-8 BOM
			continue
		}
		return b, br.UnreadByte()
	}
}

func parseOp(op string) (model.Op, error) {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "", "upsert", "insert", "update", "replace":
		return model.OpUpsert, nil
	case "delete":
		return model.OpDelete, nil
	case "truncate":
		return model.OpTruncate, nil
	}
	return 0, fmt.Errorf("unknown op %q (use upsert, delete or truncate)", op)
}

func (c change) event() (model.Event, error) {
	op, err := parseOp(c.Op)
	if err != nil {
		return model.Event{}, err
	}
	ev := model.Event{Op: op}
	raw := bytes.TrimSpace(c.Row)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		if op != model.OpTruncate {
			return model.Event{}, fmt.Errorf("%s needs a row", op)
		}
		return ev, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var row map[string]any
	if err := dec.Decode(&row); err != nil {
		return model.Event{}, fmt.Errorf("row must be a JSON object: %v", err)
	}
	if len(row) == 0 && op != model.OpTruncate {
		return model.Event{}, fmt.Errorf("%s needs a non-empty row", op)
	}
	if op == model.OpTruncate {
		return ev, nil
	}
	ev.Row = normalizeRow(row)
	return ev, nil
}

func normalizeRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = normalizeJSON(v)
	}
	return out
}

// normalizeJSON converts json.Number values to int64, uint64 or float64.
func normalizeJSON(v any) any {
	switch t := v.(type) {
	case json.Number:
		s := t.String()
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return u
		}
		if f, err := t.Float64(); err == nil {
			return source.IntegralFloat(f)
		}
		return s
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeJSON(e)
		}
		return out
	case map[string]any:
		return normalizeRow(t)
	}
	return source.Normalize(v)
}
