package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// SourceType identifies a source connector.
type SourceType string

const (
	SourcePostgres SourceType = "postgres"
	SourceMySQL    SourceType = "mysql"
	SourceMariaDB  SourceType = "mariadb"
	SourceMongoDB  SourceType = "mongodb"
	SourceDynamoDB SourceType = "dynamodb"
	// SourceKafka consumes Debezium change events from Kafka, which brings
	// every database Debezium supports (SQL Server, Oracle, Db2, Cassandra...).
	SourceKafka SourceType = "kafka"
	// SourceSQLPoll polls any database/sql driver with an incrementing cursor
	// column; it is the fallback when log based capture is not available.
	SourceSQLPoll SourceType = "sqlpoll"
	// SourceCouchDB follows the _changes feed (CouchDB, Cloudant, PouchDB servers).
	SourceCouchDB SourceType = "couchdb"
	// SourceSQLServer reads SQL Server's native CDC change tables.
	SourceSQLServer SourceType = "sqlserver"
	// SourceCockroachDB uses CockroachDB changefeeds.
	SourceCockroachDB SourceType = "cockroachdb"
	// SourceRedis consumes a Redis stream of change entries.
	SourceRedis SourceType = "redis"
	// SourceWebhook accepts changes pushed over HTTP by any application.
	SourceWebhook SourceType = "webhook"
)

// extraSourceTypes holds connector types registered outside this list
// (for example test sources).
var extraSourceTypes = map[SourceType]bool{}

// RegisterSourceType makes Validate accept an additional source type.
func RegisterSourceType(t SourceType) { extraSourceTypes[t] = true }

// SourceTypes lists every supported connector.
var SourceTypes = []SourceType{SourcePostgres, SourceMySQL, SourceMariaDB, SourceMongoDB, SourceDynamoDB, SourceKafka, SourceSQLPoll,
	SourceCouchDB, SourceSQLServer, SourceCockroachDB, SourceRedis, SourceWebhook}

// SyncMode controls the phases a task runs.
type SyncMode string

const (
	// ModeSnapshotStream copies existing rows then streams changes (default).
	ModeSnapshotStream SyncMode = "snapshot_stream"
	// ModeStream only streams changes from the current position.
	ModeStream SyncMode = "stream"
	// ModeSnapshot copies existing rows once and stops.
	ModeSnapshot SyncMode = "snapshot"
)

// WriteMode controls how documents are written to Meilisearch.
type WriteMode string

const (
	// WriteUpsert merges fields into existing documents (PUT /documents).
	WriteUpsert WriteMode = "upsert"
	// WriteReplace replaces whole documents (POST /documents).
	WriteReplace WriteMode = "replace"
)

// TimeFormat controls how time values are written.
type TimeFormat string

const (
	TimeRFC3339 TimeFormat = "rfc3339"
	// TimeUnix writes seconds since epoch, which makes dates filterable and sortable.
	TimeUnix   TimeFormat = "unix"
	TimeUnixMs TimeFormat = "unix_ms"
)

// Task is the persisted definition of a CDC pipeline.
type Task struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Source      Source    `json:"source"`
	Index       Index     `json:"index"`
	Mapping     Mapping   `json:"mapping"`
	Sync        Sync      `json:"sync"`
	Desired     State     `json:"desiredState"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Source configures the connector. Only the fields relevant to Type are used.
type Source struct {
	Type SourceType `json:"type"`
	// ConnectorID references a saved connector that provides the
	// connection settings; the task then only sets table level fields.
	ConnectorID string `json:"connectorId,omitempty"`
	// DSN is the connection string (postgres, mysql, mariadb, mongodb, sqlpoll).
	DSN string `json:"dsn,omitempty"`
	// Database is the database name (mongodb; optional for others, taken from DSN).
	Database string `json:"database,omitempty"`
	// Schema is the namespace of Table (postgres, default "public").
	Schema string `json:"schema,omitempty"`
	// Table is the table, collection, DynamoDB table or Kafka topic.
	Table string `json:"table"`

	// Postgres: replication slot and publication names (generated when empty).
	Slot        string `json:"slot,omitempty"`
	Publication string `json:"publication,omitempty"`

	// MySQL/MariaDB: replica server id (generated when zero).
	ServerID uint32 `json:"serverId,omitempty"`

	// DynamoDB.
	Region          string `json:"region,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	AccessKeyID     string `json:"accessKeyId,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`

	// Kafka (Debezium).
	Brokers []string `json:"brokers,omitempty"`
	GroupID string   `json:"groupId,omitempty"`

	// SQL poll.
	Driver       string   `json:"driver,omitempty"` // pgx, mysql, sqlite
	CursorColumn string   `json:"cursorColumn,omitempty"`
	PollInterval Duration `json:"pollInterval,omitempty"`

	// Webhook: when set, pushes must be signed with this secret (HMAC-SHA256,
	// see package signature) and need no API token.
	WebhookSecret string `json:"webhookSecret,omitempty"`
}

// Index configures the target Meilisearch index.
type Index struct {
	UID string `json:"uid"`
	// DestinationID is the Meilisearch instance (empty = the default one,
	// resolved and stored when the task is saved).
	DestinationID string `json:"destinationId,omitempty"`
	// Settings are desired Meilisearch index settings (same JSON as PATCH /settings).
	Settings json.RawMessage `json:"settings,omitempty"`
	// ManageSettings applies Settings on start and reports drift.
	ManageSettings bool      `json:"manageSettings"`
	WriteMode      WriteMode `json:"writeMode,omitempty"`
}

// GeoMapping builds the Meilisearch _geo field from two columns.
type GeoMapping struct {
	Lat string `json:"lat"`
	Lng string `json:"lng"`
}

// Mapping transforms source rows into documents.
type Mapping struct {
	// PrimaryKey lists the source columns that identify a row. When empty the
	// source primary key is used. Several columns build a composite id.
	PrimaryKey []string `json:"primaryKey,omitempty"`
	// PrimaryKeyField is the document field holding the id (default "id").
	PrimaryKeyField string `json:"primaryKeyField,omitempty"`
	// KeepKeyColumns keeps the original key columns in the document.
	KeepKeyColumns bool `json:"keepKeyColumns"`
	// Include restricts the copied columns; empty means all.
	Include []string `json:"include,omitempty"`
	// Exclude drops columns (applied after Include).
	Exclude []string `json:"exclude,omitempty"`
	// Rename maps source column to document field.
	Rename map[string]string `json:"rename,omitempty"`
	// SoftDeleteColumn turns rows whose column equals SoftDeleteValue (or is
	// non-null when SoftDeleteValue is empty) into deletes.
	SoftDeleteColumn string `json:"softDeleteColumn,omitempty"`
	SoftDeleteValue  string `json:"softDeleteValue,omitempty"`
	// Geo builds _geo from latitude and longitude columns.
	Geo *GeoMapping `json:"geo,omitempty"`
	// TimeFormat controls time serialisation (default rfc3339).
	TimeFormat TimeFormat `json:"timeFormat,omitempty"`
	// Static fields added to every document (for multi-tenant indexes).
	Static map[string]any `json:"static,omitempty"`
}

// Retry configures the retry policy for Meilisearch and source errors.
type Retry struct {
	MaxAttempts    int      `json:"maxAttempts,omitempty"`
	InitialBackoff Duration `json:"initialBackoff,omitempty"`
	MaxBackoff     Duration `json:"maxBackoff,omitempty"`
}

// Safety guards the index against destructive changes.
type Safety struct {
	// MaxDeleteRatio holds the task when a single flush would delete more than
	// this fraction of the index (0 disables).
	MaxDeleteRatio float64 `json:"maxDeleteRatio"`
	// MinDocsForDeleteGuard skips the ratio check for small indexes.
	MinDocsForDeleteGuard int64 `json:"minDocsForDeleteGuard,omitempty"`
	// AllowTruncate lets TRUNCATE events clear the index; otherwise the task is held.
	AllowTruncate bool `json:"allowTruncate"`
	// ZeroDowntimeResync rebuilds into a shadow index and swaps it in.
	ZeroDowntimeResync bool `json:"zeroDowntimeResync"`
	// DumpBeforeResync asks Meilisearch for a dump before a resync.
	DumpBeforeResync bool `json:"dumpBeforeResync"`
}

// Sync configures batching and runtime behaviour.
type Sync struct {
	Mode          SyncMode `json:"mode,omitempty"`
	BatchSize     int      `json:"batchSize,omitempty"`
	FlushInterval Duration `json:"flushInterval,omitempty"`
	// MaxInflight is the number of Meilisearch tasks enqueued but not yet
	// confirmed. Higher values raise throughput.
	MaxInflight int    `json:"maxInflight,omitempty"`
	Retry       Retry  `json:"retry"`
	Safety      Safety `json:"safety"`
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var indexPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,400}$`)

// ApplyDefaults fills zero values with sane defaults.
func (t *Task) ApplyDefaults() {
	if t.Source.Type == SourcePostgres && t.Source.Schema == "" {
		t.Source.Schema = "public"
	}
	if t.Source.Type == SourceSQLPoll && t.Source.PollInterval == 0 {
		t.Source.PollInterval = Duration(2 * time.Second)
	}
	if t.Mapping.PrimaryKeyField == "" {
		t.Mapping.PrimaryKeyField = "id"
	}
	if t.Mapping.TimeFormat == "" {
		t.Mapping.TimeFormat = TimeRFC3339
	}
	if t.Index.WriteMode == "" {
		t.Index.WriteMode = WriteReplace
	}
	if t.Sync.Mode == "" {
		t.Sync.Mode = ModeSnapshotStream
	}
	if t.Sync.BatchSize <= 0 {
		t.Sync.BatchSize = 1000
	}
	if t.Sync.FlushInterval <= 0 {
		t.Sync.FlushInterval = Duration(time.Second)
	}
	if t.Sync.MaxInflight <= 0 {
		t.Sync.MaxInflight = 4
	}
	if t.Sync.Retry.MaxAttempts <= 0 {
		t.Sync.Retry.MaxAttempts = 8
	}
	if t.Sync.Retry.InitialBackoff <= 0 {
		t.Sync.Retry.InitialBackoff = Duration(200 * time.Millisecond)
	}
	if t.Sync.Retry.MaxBackoff <= 0 {
		t.Sync.Retry.MaxBackoff = Duration(30 * time.Second)
	}
	if t.Sync.Safety.MinDocsForDeleteGuard <= 0 {
		t.Sync.Safety.MinDocsForDeleteGuard = 1000
	}
	if t.Desired == "" {
		t.Desired = StateStopped
	}
}

// Validate reports configuration errors, joined.
func (t *Task) Validate() error {
	var errs []error
	if t.ID != "" && !idPattern.MatchString(t.ID) {
		errs = append(errs, fmt.Errorf("id must match %s", idPattern))
	}
	if strings.TrimSpace(t.Name) == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if !indexPattern.MatchString(t.Index.UID) {
		errs = append(errs, errors.New("index.uid is required and may only contain a-z A-Z 0-9 - _"))
	}
	if !indexPattern.MatchString(t.Mapping.PrimaryKeyField) {
		errs = append(errs, errors.New("mapping.primaryKeyField is invalid"))
	}
	if t.Source.Table == "" {
		errs = append(errs, errors.New("source.table is required"))
	}
	// With a connector, the connector provides and validates the connection.
	if t.Source.ConnectorID == "" {
		switch t.Source.Type {
		case SourcePostgres, SourceMySQL, SourceMariaDB, SourceMongoDB, SourceCouchDB, SourceSQLServer, SourceCockroachDB, SourceRedis:
			if t.Source.DSN == "" {
				errs = append(errs, errors.New("source.dsn is required"))
			}
			if t.Source.Type == SourceMongoDB && t.Source.Database == "" {
				errs = append(errs, errors.New("source.database is required for mongodb"))
			}
		case SourceWebhook:
			// Changes are pushed to /api/v1/ingest/{taskId}; nothing to connect to.
		case SourceDynamoDB:
			if t.Source.Region == "" {
				errs = append(errs, errors.New("source.region is required for dynamodb"))
			}
		case SourceKafka:
			if len(t.Source.Brokers) == 0 {
				errs = append(errs, errors.New("source.brokers is required for kafka"))
			}
		case SourceSQLPoll:
			if t.Source.DSN == "" || t.Source.Driver == "" {
				errs = append(errs, errors.New("source.dsn and source.driver are required for sqlpoll"))
			}
			if t.Source.CursorColumn == "" {
				errs = append(errs, errors.New("source.cursorColumn is required for sqlpoll"))
			}
		default:
			if !extraSourceTypes[t.Source.Type] {
				errs = append(errs, fmt.Errorf("unsupported source type %q", t.Source.Type))
			}
		}
	}
	switch t.Sync.Mode {
	case ModeSnapshotStream, ModeStream, ModeSnapshot:
	default:
		errs = append(errs, fmt.Errorf("unsupported sync mode %q", t.Sync.Mode))
	}
	switch t.Index.WriteMode {
	case WriteUpsert, WriteReplace:
	default:
		errs = append(errs, fmt.Errorf("unsupported write mode %q", t.Index.WriteMode))
	}
	switch t.Mapping.TimeFormat {
	case TimeRFC3339, TimeUnix, TimeUnixMs:
	default:
		errs = append(errs, fmt.Errorf("unsupported time format %q", t.Mapping.TimeFormat))
	}
	if t.Sync.Safety.MaxDeleteRatio < 0 || t.Sync.Safety.MaxDeleteRatio > 1 {
		errs = append(errs, errors.New("sync.safety.maxDeleteRatio must be between 0 and 1"))
	}
	if t.Source.Type == SourceWebhook && t.Sync.Mode == ModeSnapshot {
		errs = append(errs, errors.New("sync.mode snapshot does nothing for a webhook task: changes are pushed, so use stream or snapshot_stream"))
	}
	if sec := t.Source.WebhookSecret; sec != "" {
		switch {
		case t.Source.Type != SourceWebhook:
			errs = append(errs, errors.New("source.webhookSecret is only used by webhook tasks"))
		case sec == secretMask:
			errs = append(errs, errors.New("source.webhookSecret: enter the secret again (exports do not include it)"))
		case len(sec) < 16:
			errs = append(errs, errors.New("source.webhookSecret must be at least 16 characters"))
		}
	}
	if len(t.Index.Settings) > 0 && !json.Valid(t.Index.Settings) {
		errs = append(errs, errors.New("index.settings must be valid JSON"))
	}
	if t.Mapping.Geo != nil && (t.Mapping.Geo.Lat == "" || t.Mapping.Geo.Lng == "") {
		errs = append(errs, errors.New("mapping.geo requires lat and lng"))
	}
	return errors.Join(errs...)
}

const secretMask = "••••••" // #nosec G101 -- display mask, not a credential

// SecretMask replaces secrets in API responses; sending it back keeps the
// stored value.
const SecretMask = secretMask

// Redacted returns a copy safe to return from the API: passwords inside the
// DSN and the secret access key are masked.
func (t Task) Redacted() Task {
	t.Source.DSN = RedactDSN(t.Source.DSN)
	if t.Source.SecretAccessKey != "" {
		t.Source.SecretAccessKey = secretMask
	}
	if t.Source.WebhookSecret != "" {
		t.Source.WebhookSecret = secretMask
	}
	return t
}

// RestoreSecrets copies secrets from prev into t where t carries the mask, so
// a redacted task can be edited and saved without re-entering passwords.
func (t *Task) RestoreSecrets(prev Task) {
	if strings.Contains(t.Source.DSN, secretMask) && RedactDSN(prev.Source.DSN) == t.Source.DSN {
		t.Source.DSN = prev.Source.DSN
	}
	// The stored key only goes back to the endpoint and key id it belongs to.
	if t.Source.SecretAccessKey == secretMask && t.Source.Endpoint == prev.Source.Endpoint && t.Source.AccessKeyID == prev.Source.AccessKeyID {
		t.Source.SecretAccessKey = prev.Source.SecretAccessKey
	}
	if t.Source.WebhookSecret == secretMask {
		t.Source.WebhookSecret = prev.Source.WebhookSecret
	}
}

var kvPassword = regexp.MustCompile(`(?i)(password\s*=\s*)('[^']*'|\S+)`)
var userinfoPassword = regexp.MustCompile(`^([a-zA-Z0-9+.-]+://[^:/@]+:)([^@]*)(@)`)
var mysqlPassword = regexp.MustCompile(`^([^:/@]+:)([^@]*)(@tcp\(|@unix\(|@\()`)

// RedactDSN masks the password of URL, key/value and MySQL style DSNs.
func RedactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	if strings.HasPrefix(dsn, "enc:") {
		return secretMask // still encrypted: reveal nothing
	}
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		if _, ok := u.User.Password(); ok {
			return userinfoPassword.ReplaceAllString(dsn, "${1}"+secretMask+"${3}")
		}
	}
	if mysqlPassword.MatchString(dsn) {
		return mysqlPassword.ReplaceAllString(dsn, "${1}"+secretMask+"${3}")
	}
	return kvPassword.ReplaceAllString(dsn, "${1}"+secretMask)
}
