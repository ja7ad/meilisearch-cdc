package sqlpoll

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

// position is the sqlpoll checkpoint, serialised as JSON:
//
//	{"cursor": <value>, "pk": [<value>, ...]}
//
// cursor and pk hold the cursor column and primary key values of the last
// row delivered. Numbers are kept as JSON numbers, strings as strings, times
// as RFC 3339 strings with nanoseconds and bytes as strings. A null cursor
// means "from the beginning of the table" (the table was empty when the
// position was taken).
type position struct {
	Cursor any   `json:"cursor"`
	PK     []any `json:"pk"`
}

func encodePosition(p position) model.Position {
	out := position{Cursor: jsonValue(p.Cursor), PK: make([]any, len(p.PK))}
	for i, v := range p.PK {
		out.PK[i] = jsonValue(v)
	}
	b, _ := json.Marshal(out)
	return model.Position(b)
}

// decodePosition parses pos. isTime reports, per column index (0 is the
// cursor, then the primary key columns), whether strings must be parsed as
// times so they compare correctly against the column.
func decodePosition(pos model.Position, isTime func(i int) bool) (position, error) {
	var raw position
	dec := json.NewDecoder(bytes.NewReader([]byte(pos)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return position{}, fmt.Errorf("sqlpoll: invalid position %q: %w", pos, err)
	}
	out := position{Cursor: fromJSON(raw.Cursor, isTime(0))}
	if raw.PK != nil {
		out.PK = make([]any, len(raw.PK))
		for i, v := range raw.PK {
			out.PK[i] = fromJSON(v, isTime(i+1))
		}
	}
	return out, nil
}

func jsonValue(v any) any {
	switch t := v.(type) {
	case time.Time:
		return t.Format(time.RFC3339Nano)
	case []byte:
		return string(t)
	}
	return v
}

func fromJSON(v any, isTime bool) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if u, err := strconv.ParseUint(t.String(), 10, 64); err == nil {
			return u
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case string:
		if isTime {
			if tm, err := time.Parse(time.RFC3339Nano, t); err == nil {
				return tm
			}
		}
		return t
	}
	return v
}
