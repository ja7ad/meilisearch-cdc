package config

import (
	"encoding/json"
	"fmt"
)

// remarshal converts YAML decoded maps (which may hold map[any]any) to v via JSON.
func remarshal(in any, v any) error {
	raw, err := json.Marshal(jsonable(in))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func jsonable(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = jsonable(e)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[fmt.Sprint(k)] = jsonable(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = jsonable(e)
		}
		return out
	}
	return v
}
