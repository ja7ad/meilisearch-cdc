package sqlserver

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// quote quotes an identifier with brackets.
func quote(ident string) string { return "[" + strings.ReplaceAll(ident, "]", "]]") + "]" }

func quoteAll(names []string, prefix string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = prefix + quote(n)
	}
	return out
}

// keysetAfter renders "(c1 > @p1) OR (c1 = @p1 AND c2 > @p2) ..." which
// selects the rows strictly after the key in (c1, c2...) order; SQL Server
// has no row value comparison. Parameters are numbered from first.
func keysetAfter(cols []string, first int) string {
	var ors []string
	for i := range cols {
		var ands []string
		for j := 0; j < i; j++ {
			ands = append(ands, fmt.Sprintf("%s = @p%d", quote(cols[j]), first+j))
		}
		ands = append(ands, fmt.Sprintf("%s > @p%d", quote(cols[i]), first+i))
		ors = append(ors, "("+strings.Join(ands, " AND ")+")")
	}
	return strings.Join(ors, " OR ")
}

// buildSnapshotQueries returns the first page query and the query for the
// following pages (parameters: the last key).
func buildSnapshotQueries(schema, table string, cols, pk []string, limit int) (first, next string) {
	base := fmt.Sprintf("SELECT TOP (%d) %s FROM %s.%s", limit, strings.Join(quoteAll(cols, ""), ", "), quote(schema), quote(table))
	order := " ORDER BY " + strings.Join(quoteAll(pk, ""), ", ")
	return base + order, base + " WHERE " + keysetAfter(pk, 1) + order
}

// buildChangesQuery reads the change rows after (@p1, @p2) up to the LSN
// @p3, skipping update before images (operation 3).
func buildChangesQuery(instance string, cols []string, limit int) string {
	return fmt.Sprintf(`SELECT TOP (%d) ct.[__$start_lsn], ct.[__$seqval], ct.[__$operation], m.tran_end_time, %s
FROM cdc.%s AS ct
LEFT JOIN cdc.lsn_time_mapping AS m ON m.start_lsn = ct.[__$start_lsn]
WHERE (ct.[__$start_lsn] > @p1 OR (ct.[__$start_lsn] = @p1 AND ct.[__$seqval] > @p2))
  AND ct.[__$start_lsn] <= @p3 AND ct.[__$operation] IN (1, 2, 4)
ORDER BY ct.[__$start_lsn], ct.[__$seqval], ct.[__$operation]`,
		limit, strings.Join(quoteAll(cols, "ct."), ", "), quote(instance+"_CT"))
}

// convertValue normalises a driver value given the column's database type.
func convertValue(dbType string, v any) any {
	switch strings.ToUpper(dbType) {
	case "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY":
		if b, ok := v.([]byte); ok {
			if f, err := strconv.ParseFloat(string(b), 64); err == nil {
				return f
			}
			return string(b)
		}
	case "UNIQUEIDENTIFIER":
		if b, ok := v.([]byte); ok && len(b) == 16 {
			var u mssql.UniqueIdentifier
			if err := u.Scan(b); err == nil {
				return u.String()
			}
		}
	}
	return source.Normalize(v)
}

// keyParam turns a scanned key value back into a query parameter.
func keyParam(dbType string, v any) any {
	switch strings.ToUpper(dbType) {
	case "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY":
		if b, ok := v.([]byte); ok {
			return string(b)
		}
	case "UNIQUEIDENTIFIER":
		if b, ok := v.([]byte); ok && len(b) == 16 {
			var u mssql.UniqueIdentifier
			if err := u.Scan(b); err == nil {
				return u
			}
		}
	}
	return v
}

// scanRow scans the current row into raw driver values.
func scanRow(rows *sql.Rows, n int) ([]any, error) {
	vals := make([]any, n)
	ptrs := make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	for i, v := range vals {
		if b, ok := v.([]byte); ok {
			vals[i] = append([]byte(nil), b...) // the driver may reuse the buffer
		}
	}
	return vals, nil
}
