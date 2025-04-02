package model

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// DefaultDestinationID is the id of the destination seeded from MEILI_URL.
const DefaultDestinationID = "default"

// Bounds of the background health check interval of a destination.
const (
	DefaultHealthInterval = 30 * time.Second
	MinHealthInterval     = 5 * time.Second
	MaxHealthInterval     = time.Hour
)

// Destination is a named Meilisearch instance tasks write to.
type Destination struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	URL         string   `json:"url"`
	APIKey      string   `json:"apiKey,omitempty"`
	Gzip        bool     `json:"gzip"`
	Timeout     Duration `json:"timeout,omitempty"`
	// HealthCheckInterval is how often the instance is checked in the
	// background (5s to 1h, 30s when unset).
	HealthCheckInterval Duration `json:"healthCheckInterval,omitempty"`
	IsDefault           bool     `json:"isDefault"`
	// ManagedByEnv marks the destination seeded from MEILI_URL; its URL and
	// key are refreshed from the environment at every start.
	ManagedByEnv bool      `json:"managedByEnv"`
	Tags         []string  `json:"tags,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Validate checks and normalises the destination.
func (d *Destination) Validate() error {
	var errs []error
	d.Name = strings.TrimSpace(d.Name)
	d.URL = strings.TrimRight(strings.TrimSpace(d.URL), "/")
	if d.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if d.ID != "" && !idPattern.MatchString(d.ID) {
		errs = append(errs, errors.New("id may only contain a-z A-Z 0-9 - _"))
	}
	if u, err := url.Parse(d.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, errors.New("url must be an absolute http(s) URL"))
	}
	if d.Timeout < 0 {
		errs = append(errs, errors.New("timeout must not be negative"))
	}
	if d.Timeout == 0 {
		d.Timeout = Duration(30 * time.Second)
	}
	if d.HealthCheckInterval == 0 {
		d.HealthCheckInterval = Duration(DefaultHealthInterval)
	}
	if i := d.HealthCheckInterval.D(); i < MinHealthInterval || i > MaxHealthInterval {
		errs = append(errs, errors.New("healthCheckInterval must be between 5s and 1h"))
	}
	return errors.Join(errs...)
}

// HealthEvery returns the health check interval (the default when unset,
// e.g. on destinations stored before the setting existed).
func (d Destination) HealthEvery() time.Duration {
	return min(max(d.HealthCheckInterval.Or(DefaultHealthInterval), MinHealthInterval), MaxHealthInterval)
}

// Redacted masks the API key.
func (d Destination) Redacted() Destination {
	if d.APIKey != "" {
		d.APIKey = secretMask
	}
	return d
}
