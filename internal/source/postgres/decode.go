package postgres

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// decodeText converts a value in Postgres text format to the types documented
// on model.Event. Both the snapshot (queried in text format) and pgoutput
// (which sends text) go through it, so a row reads the same in both phases.
// Unknown types (enums, domains, geometric types...) become strings.
func decodeText(m *pgtype.Map, oid uint32, data []byte) (any, error) {
	if data == nil {
		return nil, nil
	}
	s := string(data)
	switch oid {
	case pgtype.BoolOID:
		return s == "t" || s == "true", nil
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.Int8OID, pgtype.OIDOID, pgtype.XIDOID, pgtype.CIDOID:
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse integer %q: %w", s, err)
		}
		return i, nil
	case pgtype.XID8OID:
		u, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse xid8 %q: %w", s, err)
		}
		return u, nil
	case pgtype.Float4OID, pgtype.Float8OID:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("parse float %q: %w", s, err)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return s, nil // JSON cannot carry NaN or Infinity
		}
		return f, nil
	case pgtype.NumericOID:
		return decimalValue(s), nil
	case pgtype.JSONOID, pgtype.JSONBOID:
		return parseJSON(data)
	case pgtype.ByteaOID:
		return decodeWith(m, oid, data)
	case pgtype.DateOID, pgtype.TimestampOID, pgtype.TimestamptzOID:
		v, err := decodeWith(m, oid, data)
		if err != nil {
			return nil, err
		}
		if t, ok := v.(time.Time); ok {
			return t.UTC(), nil
		}
		return s, nil // infinity / -infinity
	case pgtype.TextOID, pgtype.VarcharOID, pgtype.BPCharOID, pgtype.NameOID, pgtype.QCharOID,
		pgtype.UUIDOID, pgtype.XMLOID, pgtype.TimeOID, pgtype.TimetzOID, pgtype.IntervalOID,
		pgtype.InetOID, pgtype.CIDROID, pgtype.MacaddrOID, pgtype.Macaddr8OID:
		return s, nil
	}
	if t, ok := m.TypeForOID(oid); ok {
		if _, isArray := t.Codec.(*pgtype.ArrayCodec); isArray {
			v, err := decodeWith(m, oid, data)
			if err != nil {
				return nil, err
			}
			return normalizeGo(v), nil
		}
	}
	return s, nil
}

func decodeWith(m *pgtype.Map, oid uint32, data []byte) (any, error) {
	t, ok := m.TypeForOID(oid)
	if !ok {
		return string(data), nil
	}
	v, err := t.Codec.DecodeValue(m, oid, pgtype.TextFormatCode, data)
	if err != nil {
		return nil, fmt.Errorf("decode %s value: %w", t.Name, err)
	}
	return v, nil
}

// normalizeGo converts values produced by pgtype codecs (array elements) to
// the documented event types.
func normalizeGo(v any) any {
	switch t := v.(type) {
	case nil, bool, int64, uint64, string, []byte:
		return t
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return strconv.FormatFloat(t, 'g', -1, 64)
		}
		return t
	case float32:
		f, _ := strconv.ParseFloat(strconv.FormatFloat(float64(t), 'g', -1, 32), 64)
		return normalizeGo(f)
	case time.Time:
		return t.UTC()
	case [16]byte:
		return formatUUID(t)
	case pgtype.UUID:
		if !t.Valid {
			return nil
		}
		return formatUUID(t.Bytes)
	case pgtype.Numeric:
		if !t.Valid {
			return nil
		}
		b, err := t.MarshalJSON()
		if err != nil {
			return nil
		}
		return decimalValue(strings.Trim(string(b), `"`))
	case netip.Prefix:
		return t.String()
	case netip.Addr:
		return t.String()
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeGo(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = normalizeGo(e)
		}
		return out
	case driver.Valuer:
		dv, err := t.Value()
		if err != nil {
			return fmt.Sprint(v)
		}
		return normalizeGo(dv)
	case fmt.Stringer:
		return t.String()
	}
	n := source.Normalize(v)
	switch n.(type) {
	case nil, bool, int64, uint64, float64, string, []byte, time.Time, []any, map[string]any:
		return n
	}
	return fmt.Sprint(v)
}

func formatUUID(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// parseJSON decodes a JSON document keeping integers exact.
func parseJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	return source.Normalize(v), nil
}

// decimalValue returns a numeric literal as float64 when the float prints back
// to the same decimal, and as the original string otherwise (huge or very
// precise values, NaN, Infinity).
func decimalValue(s string) any {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return s
	}
	if strconv.FormatFloat(f, 'f', -1, 64) == canonicalDecimal(s) {
		return f
	}
	return s
}

// canonicalDecimal strips a leading "+", redundant leading zeros and
// trailing fractional zeros: "+007.500" -> "7.5", "-0.0" -> "0".
func canonicalDecimal(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(s, "+-")
	intPart, frac, _ := strings.Cut(s, ".")
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}
	frac = strings.TrimRight(frac, "0")
	out := intPart
	if frac != "" {
		out += "." + frac
	}
	if neg && out != "0" {
		out = "-" + out
	}
	return out
}
