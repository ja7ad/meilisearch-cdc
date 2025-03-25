package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration that marshals to and from a human readable
// string such as "1.5s" or "250ms". Plain numbers are read as milliseconds.
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Or returns d, or def when d is zero.
func (d Duration) Or(def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return time.Duration(d)
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case nil:
		*d = 0
	case float64:
		*d = Duration(time.Duration(t) * time.Millisecond)
	case string:
		if t == "" {
			*d = 0
			return nil
		}
		p, err := time.ParseDuration(t)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", t, err)
		}
		*d = Duration(p)
	default:
		return fmt.Errorf("invalid duration %v", v)
	}
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	return d.UnmarshalJSON([]byte(fmt.Sprintf("%q", s)))
}
