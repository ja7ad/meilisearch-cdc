package source

import (
	"encoding/json"
	"math"
	"math/big"
	"time"
)

// Normalize converts common driver values to the types documented on
// model.Event: nil, bool, int64, uint64, float64, string, []byte, time.Time,
// []any and map[string]any. Unknown types are returned unchanged and later
// serialised with encoding/json.
func Normalize(v any) any {
	switch t := v.(type) {
	case nil, bool, int64, uint64, float64, string, time.Time:
		return t
	case int:
		return int64(t)
	case int8:
		return int64(t)
	case int16:
		return int64(t)
	case int32:
		return int64(t)
	case uint:
		return uint64(t)
	case uint8:
		return uint64(t)
	case uint16:
		return uint64(t)
	case uint32:
		return uint64(t)
	case float32:
		return float64(t)
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case *big.Int:
		if t.IsInt64() {
			return t.Int64()
		}
		return t.String()
	case *big.Float:
		f, _ := t.Float64()
		return f
	case []byte:
		return t
	case *time.Time:
		if t == nil {
			return nil
		}
		return *t
	case *string:
		if t == nil {
			return nil
		}
		return *t
	case *int64:
		if t == nil {
			return nil
		}
		return *t
	case *float64:
		if t == nil {
			return nil
		}
		return *t
	case *bool:
		if t == nil {
			return nil
		}
		return *t
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = Normalize(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = Normalize(e)
		}
		return out
	}
	return v
}

// IntegralFloat returns f as int64 when it has no fractional part and fits,
// which keeps ids read as float64 (DynamoDB, JSON) stable.
func IntegralFloat(f float64) any {
	if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return int64(f)
	}
	return f
}
