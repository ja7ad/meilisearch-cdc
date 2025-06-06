package sink

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

// Rows missing unchanged columns are merged, never replace the document.
func TestBatchPartialRowsInReplaceMode(t *testing.T) {
	up := func(id string, doc transform.Doc, partial bool) transform.Change {
		doc["id"] = id
		return transform.Change{Op: model.OpUpsert, ID: id, Doc: doc, Partial: partial}
	}

	b := newBatch(model.WriteReplace)
	b.add(up("1", transform.Doc{"price": 2}, true))
	f := b.toFlight()
	assert.Len(t, f.merge, 1, "a lone partial row is merged into the stored document")
	assert.Empty(t, f.upserts)

	b = newBatch(model.WriteReplace)
	b.add(up("1", transform.Doc{"name": "a", "body": "long", "price": 1}, false))
	b.add(up("1", transform.Doc{"price": 2}, true))
	f = b.toFlight()
	assert.Empty(t, f.merge)
	assert.Equal(t, []transform.Doc{{"id": "1", "name": "a", "body": "long", "price": 2}}, f.upserts, "full row then partial: one full document")

	b = newBatch(model.WriteReplace)
	b.add(up("1", transform.Doc{"price": 2}, true))
	b.add(up("1", transform.Doc{"name": "b", "body": "new", "price": 3}, false))
	f = b.toFlight()
	assert.Empty(t, f.merge)
	assert.Len(t, f.upserts, 1, "a later full row replaces")

	b = newBatch(model.WriteUpsert)
	b.add(up("1", transform.Doc{"price": 2}, true))
	f = b.toFlight()
	assert.Len(t, f.upserts, 1, "upsert mode merges anyway")
	assert.Empty(t, f.merge)
	assert.Equal(t, 1, f.ack.Upserted)
}
