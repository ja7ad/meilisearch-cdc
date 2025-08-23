package kafka

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// change is a decoded Debezium change event.
type change struct {
	Op     model.Op
	Read   bool // op "r": a row read by the Debezium snapshot
	Before map[string]any
	After  map[string]any
	Time   time.Time
}

// errSkip marks messages that carry no row change (heartbeats, schema
// changes, transaction metadata).
var errSkip = errors.New("not a row change")

// connectSchema is the Kafka Connect JSON schema of a struct.
type connectSchema struct {
	Type       string            `json:"type"`
	Name       string            `json:"name"`
	Field      string            `json:"field"`
	Fields     []connectSchema   `json:"fields"`
	Parameters map[string]string `json:"parameters"`
}

// unwrap returns the payload of a {"schema":..., "payload":...} message, or
// the message itself when it has no such wrapper.
func unwrap(data []byte) (payload json.RawMessage, schema *connectSchema, err error) {
	var w struct {
		Schema  json.RawMessage `json:"schema"`
		Payload json.RawMessage `json:"payload"`
		Op      json.RawMessage `json:"op"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, nil, err
	}
	// Some producers send {"payload": ...} without a schema; a bare envelope
	// has "op" at the top level instead.
	if w.Payload == nil || (w.Schema == nil && w.Op != nil) {
		return data, nil, nil
	}
	if len(w.Schema) > 0 && !bytes.Equal(w.Schema, []byte("null")) {
		schema = &connectSchema{}
		if err := json.Unmarshal(w.Schema, schema); err != nil {
			return nil, nil, fmt.Errorf("schema: %w", err)
		}
	}
	return w.Payload, schema, nil
}

func decodeObject(data json.RawMessage) (map[string]any, error) {
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// parseChange decodes a Debezium envelope, with or without the schema
// wrapper. It returns errSkip for messages that are not row changes.
func parseChange(value []byte) (*change, error) {
	payload, schema, err := unwrap(value)
	if err != nil {
		return nil, fmt.Errorf("kafka: invalid debezium message: %w", err)
	}
	var env struct {
		Op     string          `json:"op"`
		Before json.RawMessage `json:"before"`
		After  json.RawMessage `json:"after"`
		TsMs   *int64          `json:"ts_ms"`
	}
	if bytes.Equal(payload, []byte("null")) {
		return nil, errSkip
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("kafka: invalid debezium payload: %w", err)
	}
	c := &change{}
	if env.TsMs != nil {
		c.Time = time.UnixMilli(*env.TsMs).UTC()
	}
	switch env.Op {
	case "c", "u", "r":
		c.Op, c.Read = model.OpUpsert, env.Op == "r"
	case "d":
		c.Op = model.OpDelete
	case "t":
		c.Op = model.OpTruncate
		return c, nil
	default:
		return nil, errSkip
	}
	if c.Before, err = decodeObject(env.Before); err != nil {
		return nil, fmt.Errorf("kafka: invalid before: %w", err)
	}
	if c.After, err = decodeObject(env.After); err != nil {
		return nil, fmt.Errorf("kafka: invalid after: %w", err)
	}
	c.Before = convertRow(c.Before, fieldSchema(schema, "before"))
	c.After = convertRow(c.After, fieldSchema(schema, "after"))
	if c.Op == model.OpUpsert && c.After == nil {
		return nil, fmt.Errorf("kafka: debezium %q event without after", env.Op)
	}
	return c, nil
}

// parseKey decodes a message key into its fields (nil if the key is not a
// JSON object).
func parseKey(key []byte) map[string]any {
	if len(key) == 0 {
		return nil
	}
	payload, schema, err := unwrap(key)
	if err != nil {
		return nil
	}
	m, err := decodeObject(payload)
	if err != nil {
		return nil
	}
	return convertRow(m, schema)
}

// fieldSchema returns the struct schema of the named envelope field.
func fieldSchema(s *connectSchema, name string) *connectSchema {
	if s == nil {
		return nil
	}
	for i := range s.Fields {
		if s.Fields[i].Field == name {
			return &s.Fields[i]
		}
	}
	return nil
}

// convertRow normalises row values, applying Kafka Connect logical types
// (timestamps, dates, decimals, bytes) when the schema is known.
func convertRow(row map[string]any, s *connectSchema) map[string]any {
	if row == nil {
		return nil
	}
	types := map[string]*connectSchema{}
	if s != nil {
		for i := range s.Fields {
			types[s.Fields[i].Field] = &s.Fields[i]
		}
	}
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = convertValue(v, types[k])
	}
	return out
}

func convertValue(v any, s *connectSchema) any {
	if v == nil {
		return nil
	}
	if s != nil {
		if c, ok := logicalValue(v, s); ok {
			return c
		}
		if s.Type == "struct" {
			if m, ok := v.(map[string]any); ok {
				return convertRow(m, s)
			}
		}
	}
	return source.Normalize(v)
}

func logicalValue(v any, s *connectSchema) (any, bool) {
	num := func() (int64, bool) {
		n, ok := v.(json.Number)
		if !ok {
			return 0, false
		}
		i, err := n.Int64()
		return i, err == nil
	}
	switch s.Name {
	case "io.debezium.time.Timestamp", "org.apache.kafka.connect.data.Timestamp":
		if i, ok := num(); ok {
			return time.UnixMilli(i).UTC(), true
		}
	case "io.debezium.time.MicroTimestamp":
		if i, ok := num(); ok {
			return time.UnixMicro(i).UTC(), true
		}
	case "io.debezium.time.NanoTimestamp":
		if i, ok := num(); ok {
			return time.Unix(0, i).UTC(), true
		}
	case "io.debezium.time.Date", "org.apache.kafka.connect.data.Date":
		if i, ok := num(); ok {
			if s.Name == "org.apache.kafka.connect.data.Date" && s.Type == "int64" {
				return time.UnixMilli(i).UTC(), true
			}
			return time.Unix(i*86400, 0).UTC(), true
		}
	case "io.debezium.time.ZonedTimestamp":
		if str, ok := v.(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, str); err == nil {
				return t, true
			}
		}
	case "org.apache.kafka.connect.data.Decimal":
		if str, ok := v.(string); ok {
			if f, ok := decodeDecimal(str, s.Parameters["scale"]); ok {
				return f, true
			}
		}
	}
	if s.Type == "bytes" && s.Name == "" {
		if str, ok := v.(string); ok {
			if b, err := base64.StdEncoding.DecodeString(str); err == nil {
				return b, true
			}
		}
	}
	return nil, false
}

// decodeDecimal decodes a Connect decimal: a base64 big-endian two's
// complement unscaled integer.
func decodeDecimal(b64, scale string) (float64, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) == 0 {
		return 0, false
	}
	sc, err := strconv.Atoi(scale)
	if err != nil {
		sc = 0
	}
	n := new(big.Int).SetBytes(raw)
	if raw[0]&0x80 != 0 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(len(raw)*8)))
	}
	r := new(big.Rat).SetFrac(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(sc)), nil))
	f, _ := r.Float64()
	return f, true
}
