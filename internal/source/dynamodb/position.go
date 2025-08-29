package dynamodb

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

// position is the DynamoDB Streams checkpoint, serialised as JSON:
//
//	{"shards": {"shardId-...": "<last sequence number>"},
//	 "done":   ["shardId-..."],
//	 "since":  "2026-01-02T03:04:05Z"}
//
// shards maps every shard being read to the sequence number of the last
// record delivered from it. done lists closed shards read to the end, so
// their children can start. Shards found in neither are read from
// TRIM_HORIZON, skipping records created more than a minute before since (the
// time the position was first taken); replaying a few records is harmless.
type position struct {
	Shards map[string]string `json:"shards,omitempty"`
	Done   []string          `json:"done,omitempty"`
	Since  time.Time         `json:"since"`
}

func (p position) isDone(shard string) bool { return slices.Contains(p.Done, shard) }

func (p position) encode() model.Position {
	b, _ := json.Marshal(p)
	return model.Position(b)
}

func decodePosition(pos model.Position) (position, error) {
	var p position
	if err := json.Unmarshal([]byte(pos), &p); err != nil {
		return position{}, fmt.Errorf("dynamodb: invalid position %q: %w", pos, err)
	}
	if p.Shards == nil {
		p.Shards = map[string]string{}
	}
	return p, nil
}
