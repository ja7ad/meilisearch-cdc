package mysql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// kind groups MySQL column types by how their values are converted.
type kind uint8

const (
	kindOther kind = iota
	kindInt
	kindFloat32
	kindFloat64
	kindDecimal
	kindBit
	kindString
	kindBinary
	kindEnum
	kindSet
	kindJSON
	kindDate
	kindDateTime
	kindTime
	kindYear
)

// column is a table column as described by information_schema.
type column struct {
	name       string
	dataType   string // DATA_TYPE, lowercase
	columnType string // COLUMN_TYPE, e.g. "int unsigned", "enum('a','b')"
	nullable   bool
	pk         bool
	kind       kind
	unsigned   bool
	values     []string // enum and set members, in definition order
}

func newColumn(name, dataType, columnType string, nullable, pk, json bool) column {
	c := column{name: name, dataType: strings.ToLower(dataType), columnType: columnType, nullable: nullable, pk: pk}
	c.unsigned = strings.Contains(strings.ToLower(columnType), "unsigned")
	switch c.dataType {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint":
		c.kind = kindInt
	case "float":
		c.kind = kindFloat32
	case "double", "real", "double precision":
		c.kind = kindFloat64
	case "decimal", "numeric", "dec", "fixed":
		c.kind = kindDecimal
	case "bit":
		c.kind = kindBit
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext", "uuid", "inet4", "inet6":
		c.kind = kindString
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob",
		"geometry", "point", "linestring", "polygon", "multipoint", "multilinestring",
		"multipolygon", "geometrycollection", "geomcollection", "vector":
		c.kind = kindBinary
	case "enum":
		c.kind = kindEnum
		c.values = parseEnumValues(columnType)
	case "set":
		c.kind = kindSet
		c.values = parseEnumValues(columnType)
	case "json":
		c.kind = kindJSON
	case "date":
		c.kind = kindDate
	case "datetime", "timestamp":
		c.kind = kindDateTime
	case "time":
		c.kind = kindTime
	case "year":
		c.kind = kindYear
	}
	// MariaDB stores JSON as LONGTEXT guarded by a json_valid check.
	if json {
		c.kind = kindJSON
	}
	return c
}

// parseEnumValues extracts the members of "enum('a','b”c')" or "set(...)".
func parseEnumValues(columnType string) []string {
	open := strings.IndexByte(columnType, '(')
	end := strings.LastIndexByte(columnType, ')')
	if open < 0 || end <= open {
		return nil
	}
	body := columnType[open+1 : end]
	var out []string
	var cur strings.Builder
	in := false
	for i := 0; i < len(body); i++ {
		ch := body[i]
		switch {
		case !in && ch == '\'':
			in = true
			cur.Reset()
		case in && ch == '\'' && i+1 < len(body) && body[i+1] == '\'':
			cur.WriteByte('\'')
			i++
		case in && ch == '\\' && i+1 < len(body):
			cur.WriteByte(body[i+1])
			i++
		case in && ch == '\'':
			in = false
			out = append(out, cur.String())
		case in:
			cur.WriteByte(ch)
		}
	}
	return out
}

// convert normalises a value read from the binlog (fromBinlog) or with
// database/sql to the types documented on model.Event, so both phases
// produce identical rows.
func convert(c column, v any, fromBinlog bool) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch c.kind {
	case kindInt:
		return convertInt(c, v, fromBinlog)
	case kindFloat32:
		switch t := v.(type) {
		case float32:
			return float32To64(t), nil
		case float64:
			return float32To64(float32(t)), nil
		}
		return parseFloat(text(v))
	case kindFloat64:
		if f, ok := v.(float64); ok {
			return f, nil
		}
		if f, ok := v.(float32); ok {
			return float64(f), nil
		}
		return parseFloat(text(v))
	case kindDecimal:
		return decimalValue(text(v)), nil
	case kindBit:
		switch t := v.(type) {
		case int64:
			return t, nil
		case []byte:
			var u uint64
			for _, b := range t {
				u = u<<8 | uint64(b)
			}
			return int64(u), nil
		}
		return source.Normalize(v), nil
	case kindString:
		return text(v), nil
	case kindBinary:
		switch t := v.(type) {
		case []byte:
			return bytes.Clone(t), nil
		case string:
			return []byte(t), nil
		}
		return v, nil
	case kindEnum:
		if fromBinlog {
			if i, ok := asInt(v); ok {
				if i <= 0 || int(i) > len(c.values) {
					return "", nil // the "" error member
				}
				return c.values[i-1], nil
			}
		}
		return text(v), nil
	case kindSet:
		if fromBinlog {
			if i, ok := asInt(v); ok {
				var names []string
				for bit, name := range c.values {
					if uint64(i)&(1<<uint(bit)) != 0 {
						names = append(names, name)
					}
				}
				return strings.Join(names, ","), nil
			}
		}
		return text(v), nil
	case kindJSON:
		switch t := v.(type) {
		case []byte, string:
			s := text(t)
			if s == "" {
				return nil, nil
			}
			return parseJSON([]byte(s))
		}
		return nil, fmt.Errorf("unsupported json value %T (partial JSON updates are not supported)", v)
	case kindDate, kindDateTime:
		return convertTime(c, v)
	case kindTime:
		return text(v), nil
	case kindYear:
		if i, ok := asInt(v); ok {
			return i, nil
		}
		return strconv.ParseInt(text(v), 10, 64)
	}
	switch t := v.(type) {
	case []byte:
		return string(t), nil
	}
	return source.Normalize(v), nil
}

// convertInt handles integers. The binlog carries signed values unless the
// server logs signedness metadata, so unsigned columns are reinterpreted by
// width. Unsigned values that fit are returned as int64, larger ones as uint64.
func convertInt(c column, v any, fromBinlog bool) (any, error) {
	if !c.unsigned {
		if i, ok := asInt(v); ok {
			return i, nil
		}
		if u, ok := v.(uint64); ok {
			return unsignedValue(u), nil
		}
		i, err := strconv.ParseInt(text(v), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse integer: %w", err)
		}
		return i, nil
	}
	var u uint64
	switch t := v.(type) {
	case uint8:
		u = uint64(t)
	case uint16:
		u = uint64(t)
	case uint32:
		u = uint64(t)
	case uint64:
		u = t
	case uint:
		u = uint64(t)
	case int8:
		u = uint64(uint8(t))
	case int16:
		u = uint64(uint16(t))
	case int32:
		if c.dataType == "mediumint" {
			u = uint64(uint32(t) & 0xFFFFFF)
		} else {
			u = uint64(uint32(t))
		}
	case int64:
		u = uint64(t)
	case int:
		u = uint64(t)
	default:
		p, err := strconv.ParseUint(text(v), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse unsigned integer: %w", err)
		}
		u = p
	}
	return unsignedValue(u), nil
}

func unsignedValue(u uint64) any {
	if u <= math.MaxInt64 {
		return int64(u)
	}
	return u
}

func asInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int8:
		return int64(t), true
	case int16:
		return int64(t), true
	case int32:
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	case uint8:
		return int64(t), true
	case uint16:
		return int64(t), true
	case uint32:
		return int64(t), true
	}
	return 0, false
}

// convertTime returns DATE, DATETIME and TIMESTAMP values as UTC times.
// Zero dates ("0000-00-00") become nil.
func convertTime(c column, v any) (any, error) {
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return nil, nil
		}
		return t.UTC(), nil
	case []byte, string:
		s := text(t)
		if s == "" || strings.HasPrefix(s, "0000-00-00") {
			return nil, nil
		}
		layout := "2006-01-02 15:04:05.999999999"
		if c.kind == kindDate {
			layout = "2006-01-02"
		}
		tm, err := time.ParseInLocation(layout, s, time.UTC)
		if err != nil {
			return s, nil // keep unparsable values rather than failing the row
		}
		return tm, nil
	}
	return source.Normalize(v), nil
}

func text(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	}
	return fmt.Sprint(v)
}

func float32To64(f float32) float64 {
	out, _ := strconv.ParseFloat(strconv.FormatFloat(float64(f), 'g', -1, 32), 64)
	return out
}

func parseFloat(s string) (any, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("parse float: %w", err)
	}
	return f, nil
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

// decimalValue returns a DECIMAL as float64 when the float prints back to
// the same decimal, and as the original string otherwise.
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

// canonicalDecimal strips a sign "+", leading zeros and trailing
// fractional zeros: "+007.500" -> "7.5", "-0.0" -> "0".
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
