package cockroachdb

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

func TestParseHLC(t *testing.T) {
	w, l, err := parseHLC("1700000000123456789.0000000002")
	if err != nil || w != 1700000000123456789 || l != 2 {
		t.Fatalf("got %d %d %v", w, l, err)
	}
	if _, _, err := parseHLC("1700000000123456789"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "x", "1.", "-1.0", "1.x", "1'; DROP TABLE t; --"} {
		if _, _, err := parseHLC(bad); err == nil {
			t.Errorf("parseHLC(%q) succeeded", bad)
		}
	}
	if !hlcLess("10.0000000001", "10.0000000002") || !hlcLess("9.5", "10.0") || hlcLess("10.0", "10.0") || hlcLess("10", "bad") || !hlcLess("bad", "1") {
		t.Fatal("hlcLess is wrong")
	}
	if got := hlcTime("1700000000000000000.0000000001"); !got.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("hlcTime = %v", got)
	}
}

func TestDecodeMessage(t *testing.T) {
	types := map[string]string{"id": "INT8", "name": "STRING", "price": "DECIMAL(10,2)", "at": "TIMESTAMPTZ", "local": "TIMESTAMP"}
	pk := []string{"id"}
	tbl := "items"

	ev, resolved, err := decodeMessage(&tbl, []byte(`[4]`),
		[]byte(`{"after": {"id": 4, "name": "x", "price": 10.50, "at": "2024-01-02T03:04:05.123456+00:00", "local": "2024-01-02T03:04:05", "j": {"a": [1, 2.5]}}, "updated": "1700000000000000000.0000000000"}`),
		pk, types)
	if err != nil || resolved != "" {
		t.Fatal(err, resolved)
	}
	if ev.Op != model.OpUpsert || ev.Row["id"] != int64(4) || ev.Row["price"] != 10.5 || ev.Time.IsZero() {
		t.Fatalf("upsert %+v", ev)
	}
	if at, ok := ev.Row["at"].(time.Time); !ok || at.Nanosecond() != 123456000 {
		t.Fatalf("at = %#v", ev.Row["at"])
	}
	if _, ok := ev.Row["local"].(time.Time); !ok {
		t.Fatalf("local = %#v", ev.Row["local"])
	}
	if j := ev.Row["j"].(map[string]any)["a"].([]any); j[0] != int64(1) || j[1] != 2.5 {
		t.Fatalf("json = %#v", j)
	}

	ev, _, err = decodeMessage(&tbl, []byte(`[2]`), []byte(`{"after": null, "updated": "1.0"}`), pk, types)
	if err != nil || ev.Op != model.OpDelete || len(ev.Row) != 1 || ev.Row["id"] != int64(2) {
		t.Fatalf("delete %+v %v", ev, err)
	}

	ev, resolved, err = decodeMessage(nil, nil, []byte(`{"resolved": "1700000000000000000.0000000000"}`), pk, types)
	if err != nil || ev != nil || resolved != "1700000000000000000.0000000000" {
		t.Fatalf("resolved %+v %q %v", ev, resolved, err)
	}

	if _, _, err := decodeMessage(&tbl, []byte(`[1, 2]`), []byte(`{"after": null}`), pk, types); err == nil {
		t.Fatal("key arity mismatch accepted")
	}
}

func TestBuildSnapshotQueries(t *testing.T) {
	types := map[string]string{"a": "INT8", "b": "STRING"}
	first, next := buildSnapshotQueries(`"public"."items"`, []string{"a", "b"}, types, "17.0000000001", 10)
	wantFirst := `SELECT t."a"::STRING, t."b"::STRING, row_to_json(t)::STRING FROM "public"."items" AS t AS OF SYSTEM TIME '17.0000000001' ORDER BY t."a", t."b" LIMIT 10`
	if first != wantFirst {
		t.Fatalf("first =\n%s\nwant\n%s", first, wantFirst)
	}
	if !strings.Contains(next, `WHERE (t."a", t."b") > ($1::STRING::INT8, $2::STRING::STRING) ORDER BY`) {
		t.Fatalf("next = %s", next)
	}
	// A malformed timestamp is never interpolated.
	first, _ = buildSnapshotQueries(`"t"`, []string{"a"}, types, "1'; --", 10)
	if strings.Contains(first, "AS OF") {
		t.Fatalf("unsafe AS OF: %s", first)
	}
}

func TestChangefeedStatements(t *testing.T) {
	st := changefeedStatements(`"public"."items"`, "5.0000000000")
	if len(st) != 3 || !strings.HasPrefix(st[0], `CREATE CHANGEFEED FOR TABLE "public"."items" WITH updated, resolved = '1s', format = json, cursor = '5.0000000000'`) ||
		!strings.HasPrefix(st[2], "EXPERIMENTAL CHANGEFEED FOR") {
		t.Fatalf("%q", st)
	}
}

func TestClassify(t *testing.T) {
	if err := classify(errors.New("batch timestamp 1.0 must be after replica GC threshold 2.0")); !errors.Is(err, source.ErrPositionLost) {
		t.Fatalf("gc: %v", err)
	}
	if err := classify(errors.New(`failed to resolve targets in the CHANGEFEED stmt: table "public.things" does not exist: supplied backups do not cover requested time`)); !errors.Is(err, source.ErrPositionLost) {
		t.Fatalf("resolve targets: %v", err)
	}
	if err := classify(&pgconn.PgError{Code: "28P01"}); !source.IsPermanent(err) {
		t.Fatalf("auth: %v", err)
	}
	if err := classify(errors.New("connection reset")); source.IsPermanent(err) || errors.Is(err, source.ErrPositionLost) {
		t.Fatalf("transient: %v", err)
	}
}
