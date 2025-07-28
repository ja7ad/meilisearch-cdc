package sqlserver

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// lsnSize is the size of a SQL Server log sequence number (binary(10)).
const lsnSize = 10

// position is a point in the change table order (__$start_lsn, __$seqval).
// Stream resumes with the rows strictly after it.
type position struct {
	lsn, seq []byte
}

var (
	zeroLSN = make([]byte, lsnSize)
	maxSeq  = bytes.Repeat([]byte{0xff}, lsnSize)
)

// format renders "lsnhex:seqvalhex", 20 lowercase hex digits each.
func (p position) format() model.Position {
	return model.Position(hex.EncodeToString(p.lsn) + ":" + hex.EncodeToString(p.seq))
}

func (p position) less(o position) bool {
	if c := bytes.Compare(p.lsn, o.lsn); c != 0 {
		return c < 0
	}
	return bytes.Compare(p.seq, o.seq) < 0
}

// parsePosition parses a position written by format. Any other input wraps
// ErrPositionLost, since the engine can only recover with a resync.
func parsePosition(pos model.Position) (position, error) {
	l, s, ok := strings.Cut(string(pos), ":")
	lsn, err1 := hex.DecodeString(strings.TrimPrefix(l, "0x"))
	seq, err2 := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if !ok || err1 != nil || err2 != nil || len(lsn) != lsnSize || len(seq) != lsnSize {
		return position{}, fmt.Errorf("sqlserver: invalid position %q (want lsnhex:seqvalhex, 20 hex digits each): %w", pos, source.ErrPositionLost)
	}
	return position{lsn: lsn, seq: seq}, nil
}

// startPosition returns the position after every change captured up to
// maxLSN, or the start of the capture instance when the capture job has not
// processed anything newer than its first LSN yet.
func startPosition(maxLSN, minLSN []byte) position {
	if len(maxLSN) != lsnSize || bytes.Compare(maxLSN, minLSN) < 0 {
		return position{lsn: pad(minLSN), seq: zeroLSN}
	}
	return position{lsn: maxLSN, seq: maxSeq}
}

func pad(b []byte) []byte {
	if len(b) == lsnSize {
		return b
	}
	out := make([]byte, lsnSize)
	copy(out[lsnSize-min(len(b), lsnSize):], b)
	return out
}

// changeRow is one row of a change table.
type changeRow struct {
	lsn, seq []byte
	op       int
}

// plan decides which fetched rows to emit and the position of each.
//
// Rows are ordered by (lsn, seqval, operation). When the page was full
// (more rows may follow) and holds more than one transaction, the last
// transaction is dropped so transactions are not split across emits; it is
// read again by the next query. Rows sharing (lsn, seqval) (the delete and
// insert of a primary key update) get the position before their group,
// except the last of the group, so acknowledging any event never skips a
// row that was not delivered.
func plan(rows []changeRow, cursor position, full bool) (keep int, positions []position) {
	keep = len(rows)
	if full && keep > 0 {
		last := rows[keep-1].lsn
		i := keep
		for i > 0 && bytes.Equal(rows[i-1].lsn, last) {
			i--
		}
		if i > 0 {
			keep = i
		} else {
			// One transaction larger than a page: emit it in parts, but
			// never split rows sharing (lsn, seqval), since the next query
			// starts strictly after the last emitted row.
			lastSeq := rows[keep-1].seq
			j := keep
			for j > 0 && bytes.Equal(rows[j-1].seq, lastSeq) {
				j--
			}
			if j > 0 {
				keep = j
			}
		}
	}
	positions = make([]position, keep)
	prev := cursor
	for i := 0; i < keep; {
		j := i
		for j+1 < keep && bytes.Equal(rows[j+1].lsn, rows[i].lsn) && bytes.Equal(rows[j+1].seq, rows[i].seq) {
			j++
		}
		for k := i; k < j; k++ {
			positions[k] = prev
		}
		positions[j] = position{lsn: rows[j].lsn, seq: rows[j].seq}
		prev = positions[j]
		i = j + 1
	}
	return keep, positions
}
