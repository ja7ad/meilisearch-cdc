package mongodb

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

// docToRow converts a BSON document to a normalised row.
func docToRow(doc bson.Raw) (map[string]any, error) {
	elems, err := doc.Elements()
	if err != nil {
		return nil, err
	}
	row := make(map[string]any, len(elems))
	for _, e := range elems {
		v, err := convertValue(e.Value())
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", e.Key(), err)
		}
		row[e.Key()] = v
	}
	return row, nil
}

// convertValue maps a BSON value to the types documented on model.Event:
// ObjectID becomes its hex string, DateTime a UTC time.Time, Decimal128 a
// float64 (or its string form when it does not fit), embedded documents
// map[string]any, arrays []any and binary data []byte (UUID subtypes become
// their canonical string form so they work as document ids).
func convertValue(rv bson.RawValue) (any, error) {
	switch rv.Type {
	case bson.TypeNull, bson.TypeUndefined, bson.TypeMinKey, bson.TypeMaxKey:
		return nil, nil
	case bson.TypeDouble:
		return rv.Double(), nil
	case bson.TypeString:
		return rv.StringValue(), nil
	case bson.TypeEmbeddedDocument:
		return docToRow(rv.Document())
	case bson.TypeArray:
		vals, err := rv.Array().Values()
		if err != nil {
			return nil, err
		}
		out := make([]any, len(vals))
		for i, v := range vals {
			if out[i], err = convertValue(v); err != nil {
				return nil, err
			}
		}
		return out, nil
	case bson.TypeBinary:
		sub, data := rv.Binary()
		if (sub == 0x03 || sub == 0x04) && len(data) == 16 {
			return formatUUID(data), nil
		}
		return append([]byte(nil), data...), nil
	case bson.TypeObjectID:
		return rv.ObjectID().Hex(), nil
	case bson.TypeBoolean:
		return rv.Boolean(), nil
	case bson.TypeDateTime:
		return time.UnixMilli(rv.DateTime()).UTC(), nil
	case bson.TypeRegex:
		p, o := rv.Regex()
		return "/" + p + "/" + o, nil
	case bson.TypeDBPointer:
		ns, id := rv.DBPointer()
		return ns + "." + id.Hex(), nil
	case bson.TypeJavaScript:
		return rv.JavaScript(), nil
	case bson.TypeSymbol:
		return rv.Symbol(), nil
	case bson.TypeCodeWithScope:
		code, _ := rv.CodeWithScope()
		return code, nil
	case bson.TypeInt32:
		return int64(rv.Int32()), nil
	case bson.TypeTimestamp:
		t, _ := rv.Timestamp()
		return time.Unix(int64(t), 0).UTC(), nil
	case bson.TypeInt64:
		return rv.Int64(), nil
	case bson.TypeDecimal128:
		return decimalValue(rv.Decimal128()), nil
	}
	return nil, fmt.Errorf("unsupported bson type %s", rv.Type)
}

func decimalValue(d bson.Decimal128) any {
	s := d.String()
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return s
	}
	return f
}

func formatUUID(b []byte) string {
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// typeName is the schema type reported for a BSON type.
func typeName(t bson.Type) string {
	switch t {
	case bson.TypeDouble:
		return "double"
	case bson.TypeString, bson.TypeSymbol:
		return "string"
	case bson.TypeEmbeddedDocument:
		return "object"
	case bson.TypeArray:
		return "array"
	case bson.TypeBinary:
		return "binData"
	case bson.TypeObjectID:
		return "objectId"
	case bson.TypeBoolean:
		return "bool"
	case bson.TypeDateTime:
		return "date"
	case bson.TypeNull, bson.TypeUndefined:
		return "null"
	case bson.TypeRegex:
		return "regex"
	case bson.TypeInt32:
		return "int"
	case bson.TypeTimestamp:
		return "timestamp"
	case bson.TypeInt64:
		return "long"
	case bson.TypeDecimal128:
		return "decimal"
	}
	return t.String()
}

// inferSchema builds a schema from sampled documents. A field missing from
// some documents, or null in any, is nullable; mixed types report "mixed".
func inferSchema(docs []bson.Raw) []model.Column {
	type field struct {
		typ      string
		nullable bool
		seen     int
	}
	var order []string
	fields := map[string]*field{}
	for _, doc := range docs {
		elems, err := doc.Elements()
		if err != nil {
			continue
		}
		for _, e := range elems {
			k, t := e.Key(), e.Value().Type
			f, ok := fields[k]
			if !ok {
				f = &field{}
				fields[k] = f
				order = append(order, k)
			}
			f.seen++
			if t == bson.TypeNull || t == bson.TypeUndefined {
				f.nullable = true
				continue
			}
			switch name := typeName(t); {
			case f.typ == "":
				f.typ = name
			case f.typ != name:
				f.typ = "mixed"
			}
		}
	}
	cols := make([]model.Column, 0, len(order))
	for _, k := range order {
		f := fields[k]
		typ := f.typ
		if typ == "" {
			typ = "null"
		}
		cols = append(cols, model.Column{
			Name:       k,
			Type:       typ,
			Nullable:   k != "_id" && (f.nullable || f.seen < len(docs)),
			PrimaryKey: k == "_id",
		})
	}
	return cols
}
