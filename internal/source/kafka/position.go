package kafka

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

// offsets is the Kafka checkpoint: the next offset to read per partition,
// serialised as a JSON object keyed by "topic/partition", for example
// {"inventory.public.items/0": 42, "inventory.public.items/1": 7}.
type offsets map[string]int64

func partitionKey(topic string, partition int32) string {
	return topic + "/" + strconv.Itoa(int(partition))
}

func splitKey(k string) (topic string, partition int32, err error) {
	i := strings.LastIndexByte(k, '/')
	if i <= 0 {
		return "", 0, fmt.Errorf("kafka: invalid partition key %q", k)
	}
	p, err := strconv.ParseInt(k[i+1:], 10, 32)
	if err != nil {
		return "", 0, fmt.Errorf("kafka: invalid partition key %q", k)
	}
	return k[:i], int32(p), nil
}

func (o offsets) encode() model.Position {
	b, _ := json.Marshal(o)
	return model.Position(b)
}

func decodeOffsets(pos model.Position) (offsets, error) {
	o := offsets{}
	if pos == "" {
		return o, nil
	}
	if err := json.Unmarshal([]byte(pos), &o); err != nil {
		return nil, fmt.Errorf("kafka: invalid position %q: %w", pos, err)
	}
	for k := range o {
		if _, _, err := splitKey(k); err != nil {
			return nil, err
		}
	}
	return o, nil
}

// byTopic converts o to topic -> partition -> offset.
func (o offsets) byTopic() map[string]map[int32]int64 {
	out := map[string]map[int32]int64{}
	for k, off := range o {
		t, p, err := splitKey(k)
		if err != nil {
			continue
		}
		if out[t] == nil {
			out[t] = map[int32]int64{}
		}
		out[t][p] = off
	}
	return out
}
