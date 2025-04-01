package model

import (
	"errors"
	"strings"
	"time"
)

// Connector is a reusable source connection: tasks reference it by id and
// add only table level settings.
type Connector struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Type        SourceType `json:"type"`
	Tags        []string   `json:"tags,omitempty"`
	// Config holds the connection fields of Source for Type.
	Config    Source    `json:"config"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ConnectionFields keeps only the connection part of a Source.
func ConnectionFields(s Source) Source {
	return Source{
		Type: s.Type, DSN: s.DSN, Database: s.Database, Region: s.Region, Endpoint: s.Endpoint,
		AccessKeyID: s.AccessKeyID, SecretAccessKey: s.SecretAccessKey, Brokers: s.Brokers, Driver: s.Driver,
	}
}

// StripConnection removes the fields a connector provides, so tasks using
// a connector never duplicate its credentials.
func (s Source) StripConnection() Source {
	s.DSN, s.Region, s.Endpoint, s.AccessKeyID, s.SecretAccessKey, s.Brokers, s.Driver = "", "", "", "", "", nil, ""
	return s
}

// WithConnector merges the connector's connection into the task source.
// Task values win for Database (a per task override).
func (s Source) WithConnector(c Connector) Source {
	s.Type = c.Type
	s.DSN, s.Region, s.Endpoint = c.Config.DSN, c.Config.Region, c.Config.Endpoint
	s.AccessKeyID, s.SecretAccessKey = c.Config.AccessKeyID, c.Config.SecretAccessKey
	s.Brokers, s.Driver = c.Config.Brokers, c.Config.Driver
	if s.Database == "" {
		s.Database = c.Config.Database
	}
	return s
}

// Validate checks the connector.
func (c *Connector) Validate() error {
	var errs []error
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if c.ID != "" && !idPattern.MatchString(c.ID) {
		errs = append(errs, errors.New("id may only contain a-z A-Z 0-9 - _"))
	}
	c.Config = ConnectionFields(c.Config)
	c.Config.Type = c.Type
	// Reuse task validation with a placeholder table.
	probe := Task{Name: c.Name, Source: c.Config, Index: Index{UID: "probe"}}
	probe.Source.Table = "probe"
	probe.ApplyDefaults()
	if c.Type == SourceSQLPoll {
		probe.Source.CursorColumn = "probe"
	}
	if c.Type == SourceMongoDB && probe.Source.Database == "" {
		probe.Source.Database = "probe" // the database may be chosen per task
	}
	if err := probe.Validate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Redacted masks secrets.
func (c Connector) Redacted() Connector {
	c.Config.DSN = RedactDSN(c.Config.DSN)
	if c.Config.SecretAccessKey != "" {
		c.Config.SecretAccessKey = secretMask
	}
	return c
}

// RestoreSecrets keeps stored secrets where the update carries the mask.
func (c *Connector) RestoreSecrets(prev Connector) {
	t := Task{Source: c.Config}
	t.RestoreSecrets(Task{Source: prev.Config})
	c.Config.DSN, c.Config.SecretAccessKey = t.Source.DSN, t.Source.SecretAccessKey
}
