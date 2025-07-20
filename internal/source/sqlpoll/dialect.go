package sqlpoll

import (
	"fmt"
	"strconv"
	"strings"
)

// dialect captures the SQL differences between the supported drivers.
type dialect struct {
	name       string // canonical name: postgres, mysql or sqlite
	driverName string // database/sql driver name
	quoteChar  byte
	dollar     bool // $1 placeholders instead of ?
}

func dialectFor(driver string) (dialect, error) {
	switch strings.ToLower(driver) {
	case "pgx", "postgres", "postgresql":
		return dialect{name: "postgres", driverName: "pgx", quoteChar: '"', dollar: true}, nil
	case "mysql", "mariadb":
		return dialect{name: "mysql", driverName: "mysql", quoteChar: '`'}, nil
	case "sqlite", "sqlite3":
		return dialect{name: "sqlite", driverName: "sqlite", quoteChar: '"'}, nil
	}
	return dialect{}, fmt.Errorf("sqlpoll: unsupported driver %q (use pgx, postgres, mysql or sqlite)", driver)
}

// quote quotes an identifier, doubling embedded quote characters.
func (d dialect) quote(ident string) string {
	q := string(d.quoteChar)
	return q + strings.ReplaceAll(ident, q, q+q) + q
}

// table returns the quoted, optionally schema qualified table name.
func (d dialect) table(schema, table string) string {
	if schema == "" {
		return d.quote(table)
	}
	return d.quote(schema) + "." + d.quote(table)
}

// args accumulates query arguments and renders placeholders.
type args struct {
	d    dialect
	vals []any
}

func (a *args) add(v any) string {
	a.vals = append(a.vals, v)
	if a.d.dollar {
		return "$" + strconv.Itoa(len(a.vals))
	}
	return "?"
}

// keysetAfter renders "(c1 > ?) OR (c1 = ? AND c2 > ?) OR ..." which selects
// the rows strictly after vals in (c1, c2, ...) order. The expanded form
// works on every dialect, unlike row value comparisons.
func keysetAfter(a *args, cols []string, vals []any) string {
	var terms []string
	for i := range cols {
		var parts []string
		for j := 0; j < i; j++ {
			parts = append(parts, a.d.quote(cols[j])+" = "+a.add(vals[j]))
		}
		parts = append(parts, a.d.quote(cols[i])+" > "+a.add(vals[i]))
		terms = append(terms, "("+strings.Join(parts, " AND ")+")")
	}
	return strings.Join(terms, " OR ")
}

func (d dialect) columnList(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = d.quote(c)
	}
	return strings.Join(q, ", ")
}

func (d dialect) orderBy(cols []string, desc bool) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = d.quote(c)
		if desc {
			q[i] += " DESC"
		}
	}
	return strings.Join(q, ", ")
}

// snapshotQuery pages through the table by primary key. after is nil for the
// first page.
func (d dialect) snapshotQuery(table string, pk []string, after []any, limit int) (string, []any) {
	a := &args{d: d}
	var b strings.Builder
	b.WriteString("SELECT * FROM " + table)
	if after != nil {
		b.WriteString(" WHERE " + keysetAfter(a, pk, after))
	}
	b.WriteString(" ORDER BY " + d.orderBy(pk, false))
	b.WriteString(" LIMIT " + strconv.Itoa(limit))
	return b.String(), a.vals
}

// pollQuery selects the rows changed after pos, ordered by (cursor, pk).
// A position without cursor value starts from the beginning of the table.
func (d dialect) pollQuery(table, cursor string, pk []string, pos position, limit int) (string, []any) {
	a := &args{d: d}
	var b strings.Builder
	b.WriteString("SELECT * FROM " + table)
	cols := append([]string{cursor}, pk...)
	switch {
	case pos.Cursor == nil:
		b.WriteString(" WHERE " + d.quote(cursor) + " IS NOT NULL")
	case len(pos.PK) == len(pk):
		b.WriteString(" WHERE " + keysetAfter(a, cols, append([]any{pos.Cursor}, pos.PK...)))
	default:
		// No primary key values: everything strictly after the cursor.
		b.WriteString(" WHERE " + d.quote(cursor) + " > " + a.add(pos.Cursor))
	}
	b.WriteString(" ORDER BY " + d.orderBy(cols, false))
	b.WriteString(" LIMIT " + strconv.Itoa(limit))
	return b.String(), a.vals
}

// latestQuery returns the last row in (cursor, pk) order.
func (d dialect) latestQuery(table, cursor string, pk []string) string {
	cols := append([]string{cursor}, pk...)
	return "SELECT " + d.columnList(cols) + " FROM " + table +
		" WHERE " + d.quote(cursor) + " IS NOT NULL ORDER BY " + d.orderBy(cols, true) + " LIMIT 1"
}

// primaryKeyQuery lists the primary key columns in key order.
func (d dialect) primaryKeyQuery(schema, table string) (string, []any) {
	switch d.name {
	case "postgres":
		if schema == "" {
			schema = "public"
		}
		return `SELECT kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
 AND tc.table_name = kcu.table_name
WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = $1 AND tc.table_name = $2
ORDER BY kcu.ordinal_position`, []any{schema, table}
	case "mysql":
		return `SELECT kcu.column_name
FROM information_schema.key_column_usage kcu
WHERE kcu.constraint_name = 'PRIMARY'
  AND kcu.table_schema = COALESCE(NULLIF(?, ''), DATABASE())
  AND kcu.table_name = ?
ORDER BY kcu.ordinal_position`, []any{schema, table}
	default:
		if schema == "" {
			schema = "main"
		}
		return `SELECT name FROM pragma_table_info(?, ?) WHERE pk > 0 ORDER BY pk`, []any{table, schema}
	}
}

// rowCountQuery returns a cheap row count estimate.
func (d dialect) rowCountQuery(schema, table, quoted string) (string, []any) {
	switch d.name {
	case "postgres":
		return `SELECT reltuples::bigint FROM pg_class WHERE oid = $1::regclass`, []any{quoted}
	case "mysql":
		return `SELECT table_rows FROM information_schema.tables
WHERE table_schema = COALESCE(NULLIF(?, ''), DATABASE()) AND table_name = ?`, []any{schema, table}
	default:
		return "SELECT count(*) FROM " + quoted, nil
	}
}
