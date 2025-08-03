package sqlserver

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

func lsn(b byte, tail byte) []byte {
	out := make([]byte, lsnSize)
	out[0], out[lsnSize-1] = b, tail
	return out
}

func TestPositionRoundTrip(t *testing.T) {
	p := position{lsn: lsn(0x2b, 0x21), seq: lsn(0x2b, 0x20)}
	s := p.format()
	if s != "2b000000000000000021:2b000000000000000020" {
		t.Fatalf("format = %s", s)
	}
	q, err := parsePosition(s)
	if err != nil || !bytes.Equal(q.lsn, p.lsn) || !bytes.Equal(q.seq, p.seq) {
		t.Fatalf("parse = %+v %v", q, err)
	}
	if _, err := parsePosition("0x2b000000000000000021:0x2b000000000000000020"); err != nil {
		t.Fatalf("0x prefix: %v", err)
	}
	for _, bad := range []model.Position{"", "abc", "2b:2b", "zz000000000000000021:2b000000000000000020", "2b000000000000000021"} {
		if _, err := parsePosition(bad); !errors.Is(err, source.ErrPositionLost) {
			t.Errorf("parsePosition(%q) = %v", bad, err)
		}
	}
	if !p.less(position{lsn: p.lsn, seq: lsn(0x2b, 0x21)}) || !p.less(position{lsn: lsn(0x2c, 0), seq: zeroLSN}) || p.less(p) {
		t.Fatal("less is wrong")
	}
}

func TestStartPosition(t *testing.T) {
	min, max := lsn(1, 0), lsn(2, 0)
	if p := startPosition(max, min); !bytes.Equal(p.lsn, max) || !bytes.Equal(p.seq, maxSeq) {
		t.Fatalf("normal: %s", p.format())
	}
	// Capture has not processed anything after the instance start yet.
	if p := startPosition(lsn(0, 0), min); !bytes.Equal(p.lsn, min) || !bytes.Equal(p.seq, zeroLSN) {
		t.Fatalf("before capture: %s", p.format())
	}
	if p := startPosition(nil, min); !bytes.Equal(p.lsn, min) {
		t.Fatalf("null max: %s", p.format())
	}
}

func TestPlan(t *testing.T) {
	cursor := position{lsn: lsn(1, 0), seq: lsn(1, 0)}
	rows := []changeRow{
		{lsn: lsn(2, 0), seq: lsn(2, 1), op: 2},
		{lsn: lsn(3, 0), seq: lsn(3, 1), op: 1}, // primary key update: delete + insert share seqval
		{lsn: lsn(3, 0), seq: lsn(3, 1), op: 2},
		{lsn: lsn(3, 0), seq: lsn(3, 2), op: 4},
		{lsn: lsn(4, 0), seq: lsn(4, 1), op: 2},
	}
	keep, pos := plan(rows, cursor, false)
	if keep != 5 {
		t.Fatalf("keep = %d", keep)
	}
	want := []model.Position{
		position{lsn(2, 0), lsn(2, 1)}.format(),
		position{lsn(2, 0), lsn(2, 1)}.format(), // before its group
		position{lsn(3, 0), lsn(3, 1)}.format(),
		position{lsn(3, 0), lsn(3, 2)}.format(),
		position{lsn(4, 0), lsn(4, 1)}.format(),
	}
	for i := range want {
		if pos[i].format() != want[i] {
			t.Fatalf("position %d = %s, want %s", i, pos[i].format(), want[i])
		}
	}

	// A full page drops the last (possibly incomplete) transaction.
	keep, _ = plan(rows, cursor, true)
	if keep != 4 {
		t.Fatalf("full page keep = %d", keep)
	}
	// A full page holding one transaction keeps whole seqval groups.
	single := []changeRow{
		{lsn: lsn(3, 0), seq: lsn(3, 1), op: 4},
		{lsn: lsn(3, 0), seq: lsn(3, 2), op: 1},
		{lsn: lsn(3, 0), seq: lsn(3, 2), op: 2},
	}
	keep, pos = plan(single, cursor, true)
	if keep != 1 || !bytes.Equal(pos[0].seq, lsn(3, 1)) {
		t.Fatalf("single transaction keep = %d", keep)
	}
	// The first group's leading rows fall back to the cursor.
	keep, pos = plan(single[1:], cursor, false)
	if keep != 2 || pos[0].format() != cursor.format() || !bytes.Equal(pos[1].seq, lsn(3, 2)) {
		t.Fatalf("leading group: %d %v", keep, pos)
	}
	if keep, _ := plan(nil, cursor, true); keep != 0 {
		t.Fatal("empty page")
	}
}

func TestQueries(t *testing.T) {
	first, next := buildSnapshotQueries("dbo", "it]ems", []string{"id", "name"}, []string{"a", "b"}, 10)
	if first != "SELECT TOP (10) [id], [name] FROM [dbo].[it]]ems] ORDER BY [a], [b]" {
		t.Fatalf("first = %s", first)
	}
	if !strings.Contains(next, "WHERE ([a] > @p1) OR ([a] = @p1 AND [b] > @p2) ORDER BY [a], [b]") {
		t.Fatalf("next = %s", next)
	}
	q := buildChangesQuery("dbo_items", []string{"id", "name"}, 100)
	for _, want := range []string{"SELECT TOP (100) ct.[__$start_lsn], ct.[__$seqval], ct.[__$operation], m.tran_end_time, ct.[id], ct.[name]",
		"FROM cdc.[dbo_items_CT] AS ct", "ct.[__$start_lsn] <= @p3", "IN (1, 2, 4)", "ORDER BY ct.[__$start_lsn], ct.[__$seqval], ct.[__$operation]"} {
		if !strings.Contains(q, want) {
			t.Fatalf("changes query misses %q:\n%s", want, q)
		}
	}
}

func TestConvertValue(t *testing.T) {
	if v := convertValue("DECIMAL", []byte("12.50")); v != 12.5 {
		t.Fatalf("decimal = %#v", v)
	}
	if v := keyParam("NUMERIC", []byte("12.50")); v != "12.50" {
		t.Fatalf("decimal key = %#v", v)
	}
	var u mssql.UniqueIdentifier
	_ = u.Scan("6F9619FF-8B86-D011-B42D-00C04FC964FF")
	raw, _ := u.Value()
	if v := convertValue("UNIQUEIDENTIFIER", raw); v != "6F9619FF-8B86-D011-B42D-00C04FC964FF" {
		t.Fatalf("uuid = %#v", v)
	}
	if _, ok := keyParam("UNIQUEIDENTIFIER", raw).(mssql.UniqueIdentifier); !ok {
		t.Fatal("uuid key param")
	}
	if v := convertValue("INT", int64(3)); v != int64(3) {
		t.Fatalf("int = %#v", v)
	}
	if v := convertValue("NVARCHAR", "x"); v != "x" {
		t.Fatalf("string = %#v", v)
	}
}

func TestWithDatabase(t *testing.T) {
	got, err := withDatabase("sqlserver://sa:p%40ss@host:1433?encrypt=disable", "shop")
	if err != nil || !strings.Contains(got, "database=shop") || !strings.Contains(got, "encrypt=disable") || !strings.Contains(got, "p%40ss") {
		t.Fatalf("url: %s %v", got, err)
	}
	if got, _ := withDatabase("server=h;user id=sa;", "shop"); got != "server=h;user id=sa;database=shop" {
		t.Fatalf("ado: %s", got)
	}
	if got, _ := withDatabase("x", ""); got != "x" {
		t.Fatal("unchanged")
	}
}

func TestClassify(t *testing.T) {
	if !source.IsPermanent(classify(mssql.Error{Number: 18456})) {
		t.Fatal("login failure must be permanent")
	}
	if source.IsPermanent(classify(errors.New("i/o timeout"))) {
		t.Fatal("transient error marked permanent")
	}
}
