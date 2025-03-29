package model

import "time"

// Op is a change operation.
type Op uint8

const (
	OpUpsert Op = iota + 1
	OpDelete
	// OpTruncate removes every row of the source table.
	OpTruncate
	// OpProgress carries no row: the source is caught up to Position (at
	// Time), so the checkpoint can move while the table is idle.
	OpProgress
)

func (o Op) String() string {
	switch o {
	case OpUpsert:
		return "upsert"
	case OpDelete:
		return "delete"
	case OpTruncate:
		return "truncate"
	case OpProgress:
		return "progress"
	}
	return "unknown"
}

// Position is an opaque, connector specific checkpoint (LSN, binlog
// coordinates, resume token...). The empty position means "unknown".
type Position string

// Event is a single change captured from a source.
//
// Row holds the column values normalised to: nil, bool, int64, uint64,
// float64, string, []byte, time.Time, []any or map[string]any. For deletes
// Row holds at least the primary key columns.
type Event struct {
	Op       Op
	Row      map[string]any
	Position Position
	// Time is when the change was committed in the source (zero if unknown).
	Time time.Time
	// Snapshot marks rows read by the initial copy.
	Snapshot bool
	// Partial marks a row that leaves out columns the source did not send
	// (PostgreSQL's unchanged TOAST values). It is merged into the document
	// even in replace mode, so the missing fields are kept.
	Partial bool
}

// Column describes a source column or field.
type Column struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable"`
	PrimaryKey bool   `json:"primaryKey"`
}

// Schema describes a source table. Schemaless sources infer it by sampling
// and set Inferred.
type Schema struct {
	Columns    []Column `json:"columns"`
	PrimaryKey []string `json:"primaryKey"`
	Inferred   bool     `json:"inferred"`
	RowCount   int64    `json:"rowCount"` // estimate, -1 when unknown
}
