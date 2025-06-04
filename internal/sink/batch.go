package sink

import (
	"maps"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

type item struct {
	op  model.Op
	doc transform.Doc
	// replace is set in upsert mode when a delete preceded the upsert in the
	// same batch: merging into the old document would resurrect stale fields.
	replace bool
	// partial is set in replace mode for a row missing unchanged columns: it
	// is merged into the stored document instead of replacing it.
	partial bool
}

// batch coalesces changes by document id: only the final state of each
// document is sent, which removes redundant work for hot rows.
type batch struct {
	mode      model.WriteMode
	order     []string
	items     map[string]*item
	pos       model.Position
	ack       Ack
	coalesced int
}

func newBatch(mode model.WriteMode) *batch {
	return &batch{mode: mode, items: map[string]*item{}}
}

func (b *batch) size() int   { return len(b.items) }
func (b *batch) empty() bool { return len(b.items) == 0 && b.pos == "" }

func (b *batch) add(c transform.Change) {
	if c.Position != "" {
		b.pos = c.Position
	}
	if c.Time.After(b.ack.Time) {
		b.ack.Time = c.Time
	}
	if c.Snapshot {
		b.ack.Snapshot = true
	}
	if c.ID == "" {
		// Position marker (e.g. a skipped row): advances the checkpoint only.
		return
	}
	prev, ok := b.items[c.ID]
	if !ok {
		b.order = append(b.order, c.ID)
		b.items[c.ID] = &item{op: c.Op, doc: c.Doc, partial: c.Partial && c.Op == model.OpUpsert && b.mode == model.WriteReplace}
		return
	}
	b.coalesced++
	switch {
	case c.Op == model.OpDelete:
		prev.op, prev.doc, prev.replace, prev.partial = model.OpDelete, nil, false, false
	case prev.op == model.OpDelete:
		prev.op, prev.doc = model.OpUpsert, c.Doc
		prev.replace = b.mode == model.WriteUpsert
	case c.Partial && b.mode == model.WriteReplace:
		// Merge the sent columns into what we have; still partial unless
		// the earlier change carried the whole row.
		merged := maps.Clone(prev.doc)
		maps.Copy(merged, c.Doc)
		prev.doc = merged
	case b.mode == model.WriteUpsert:
		// Partial updates merge, so later fields win and omitted ones stay.
		merged := maps.Clone(prev.doc)
		maps.Copy(merged, c.Doc)
		prev.doc = merged
	default:
		prev.doc, prev.partial = c.Doc, false
	}
}

func (b *batch) toFlight() *flight {
	f := &flight{ready: make(chan struct{})}
	for _, id := range b.order {
		it := b.items[id]
		switch {
		case it.op == model.OpDelete:
			f.deletes = append(f.deletes, id)
		case it.replace:
			f.replace = append(f.replace, it.doc)
		case it.partial:
			f.merge = append(f.merge, it.doc)
		default:
			f.upserts = append(f.upserts, it.doc)
		}
	}
	f.ack = b.ack
	f.ack.Position = b.pos
	f.ack.Upserted = len(f.upserts) + len(f.replace) + len(f.merge)
	f.ack.Deleted = len(f.deletes)
	f.ack.Coalesced = b.coalesced
	return f
}
