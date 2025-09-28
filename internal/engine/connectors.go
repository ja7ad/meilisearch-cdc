package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/discover"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// TaskRef identifies a task using a connector.
type TaskRef struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	State model.State `json:"state"`
}

// ConnectorView is a redacted connector with the tasks using it.
type ConnectorView struct {
	model.Connector
	UsedBy []TaskRef `json:"usedBy"`
	// LastTest is the outcome of the latest connection test, if any.
	LastTest *ConnectorTest `json:"lastTest,omitempty"`
}

// ConnectorTest is the stored outcome of a connection test.
type ConnectorTest struct {
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	LatencyMs int64     `json:"latencyMs"`
	Version   string    `json:"version,omitempty"`
	Tables    int       `json:"tables"`
	At        time.Time `json:"at"`
}

// TestConnector tests a saved connector and stores the outcome, so lists
// show whether it works without testing again.
func (e *Engine) TestConnector(ctx context.Context, id string) (discover.Result, error) {
	c, err := e.RevealConnector(id)
	if err != nil {
		return discover.Result{}, err
	}
	c.Config.Type = c.Type
	res := discover.Run(ctx, c.Config)
	e.recordConnectorTest(id, res)
	return res, nil
}

// testConnectorSoon tests a connector in the background after it changed.
func (e *Engine) testConnectorSoon(id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = e.TestConnector(ctx, id)
	}()
}

func (e *Engine) recordConnectorTest(id string, res discover.Result) {
	if _, err := e.connector(id); err != nil {
		return
	}
	t := ConnectorTest{OK: res.OK, Error: res.Error, LatencyMs: res.LatencyMs, Version: res.Version, Tables: len(res.Tables), At: time.Now().UTC()}
	if err := e.connTests.Put(id, t); err != nil {
		e.log.Warn("store connector test", "connector", id, "err", err)
		return
	}
	e.TaskBus.Publish(TaskEvent{ID: id, Action: "connector.tested"})
}

// ErrInUse is returned when deleting a connector that tasks still use.
type ErrInUse struct{ UsedBy []TaskRef }

func (e *ErrInUse) Error() string {
	names := make([]string, len(e.UsedBy))
	for i, t := range e.UsedBy {
		names[i] = t.Name
	}
	return fmt.Sprintf("%v: the connector is used by %s", ErrConflict, strings.Join(names, ", "))
}

func (e *ErrInUse) Unwrap() error { return ErrConflict }

func connAAD(id, field string) string { return "connector:" + id + ":" + field }

func (e *Engine) openConnectors() error {
	seal := func(c model.Connector) (model.Connector, error) {
		var err error
		if c.Config.DSN, err = e.cfg.Cipher.Rewrap(c.Config.DSN, connAAD(c.ID, "dsn")); err != nil {
			return c, err
		}
		c.Config.SecretAccessKey, err = e.cfg.Cipher.Rewrap(c.Config.SecretAccessKey, connAAD(c.ID, "secretAccessKey"))
		return c, err
	}
	var err error
	if e.connectors, err = store.NewBucket(e.cfg.DB, "connectors", store.WithHooks(seal, nil)); err != nil {
		return err
	}
	e.connTests, err = store.NewBucket[ConnectorTest](e.cfg.DB, "connector_tests")
	return err
}

func (e *Engine) revealConnector(c model.Connector) (model.Connector, error) {
	var err error
	if c.Config.DSN, err = e.cfg.Cipher.Decrypt(c.Config.DSN, connAAD(c.ID, "dsn")); err != nil {
		return c, fmt.Errorf("decrypt connector credentials: %w", err)
	}
	if c.Config.SecretAccessKey, err = e.cfg.Cipher.Decrypt(c.Config.SecretAccessKey, connAAD(c.ID, "secretAccessKey")); err != nil {
		return c, fmt.Errorf("decrypt connector credentials: %w", err)
	}
	return c, nil
}

// connector returns a stored connector (secrets encrypted).
func (e *Engine) connector(id string) (model.Connector, error) {
	c, ok, err := e.connectors.Get(id)
	if err == nil && !ok {
		err = fmt.Errorf("%w: connector %s", ErrNotFound, id)
	}
	return c, err
}

// RevealConnector returns a connector with decrypted secrets, for opening
// connections. Callers must not store or log the result.
func (e *Engine) RevealConnector(id string) (model.Connector, error) {
	c, err := e.connector(id)
	if err != nil {
		return c, err
	}
	return e.revealConnector(c)
}

func (e *Engine) usedBy(id string) []TaskRef {
	tasks, _ := e.tasks.List("", 0)
	out := []TaskRef{}
	for _, t := range tasks {
		if t.Source.ConnectorID == id {
			out = append(out, TaskRef{ID: t.ID, Name: t.Name, State: e.Status(t.ID).State})
		}
	}
	return out
}

func (e *Engine) connectorView(c model.Connector) ConnectorView {
	if plain, err := e.revealConnector(c); err == nil {
		c = plain
	}
	v := ConnectorView{Connector: c.Redacted(), UsedBy: e.usedBy(c.ID)}
	if t, ok, _ := e.connTests.Get(c.ID); ok {
		v.LastTest = &t
	}
	return v
}

// Connectors lists connectors.
func (e *Engine) Connectors() ([]ConnectorView, error) {
	all, err := e.connectors.List("", 0)
	if err != nil {
		return nil, err
	}
	out := make([]ConnectorView, 0, len(all))
	for _, c := range all {
		out = append(out, e.connectorView(c))
	}
	slices.SortFunc(out, func(a, b ConnectorView) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

// Connector returns one connector view.
func (e *Engine) Connector(id string) (ConnectorView, error) {
	c, err := e.connector(id)
	if err != nil {
		return ConnectorView{}, err
	}
	return e.connectorView(c), nil
}

// CreateConnector stores a new connector.
func (e *Engine) CreateConnector(c model.Connector) (ConnectorView, error) {
	if c.ID == "" {
		c.ID = newID()
	}
	if _, ok, _ := e.connectors.Get(c.ID); ok {
		return ConnectorView{}, fmt.Errorf("%w: connector %s already exists", ErrConflict, c.ID)
	}
	if err := c.Validate(); err != nil {
		return ConnectorView{}, err
	}
	c.CreatedAt = time.Now().UTC()
	c.UpdatedAt = c.CreatedAt
	if err := e.connectors.Put(c.ID, c); err != nil {
		return ConnectorView{}, err
	}
	e.TaskBus.Publish(TaskEvent{ID: c.ID, Action: "connector.created"})
	e.testConnectorSoon(c.ID)
	return e.Connector(c.ID)
}

// UpdateConnector replaces a connector; running tasks using it restart so
// rotated credentials take effect.
func (e *Engine) UpdateConnector(ctx context.Context, id string, c model.Connector) (ConnectorView, error) {
	prev, err := e.connector(id)
	if err != nil {
		return ConnectorView{}, err
	}
	plainPrev, err := e.revealConnector(prev)
	if err != nil {
		return ConnectorView{}, err
	}
	c.ID = id
	c.RestoreSecrets(plainPrev)
	if err := c.Validate(); err != nil {
		return ConnectorView{}, err
	}
	if c.Type != prev.Type && len(e.usedBy(id)) > 0 {
		return ConnectorView{}, fmt.Errorf("%w: the type of a connector in use cannot change", ErrConflict)
	}
	c.CreatedAt, c.UpdatedAt = prev.CreatedAt, time.Now().UTC()
	if err := e.connectors.Put(id, c); err != nil {
		return ConnectorView{}, err
	}
	for _, ref := range e.usedBy(id) {
		if e.running(ref.ID) {
			e.logf(ref.ID, "info", "connector %q changed: restarting", c.Name)
			if _, err := e.Action(ctx, ref.ID, "restart"); err != nil {
				e.logf(ref.ID, "error", "restart after connector change: %v", err)
			}
		}
	}
	e.TaskBus.Publish(TaskEvent{ID: id, Action: "connector.updated"})
	e.testConnectorSoon(id)
	return e.Connector(id)
}

// DeleteConnector removes an unused connector.
func (e *Engine) DeleteConnector(id string) error {
	if _, err := e.connector(id); err != nil {
		return err
	}
	if used := e.usedBy(id); len(used) > 0 {
		return &ErrInUse{UsedBy: used}
	}
	if err := e.connectors.Delete(id); err != nil {
		return err
	}
	_ = e.connTests.Delete(id)
	e.TaskBus.Publish(TaskEvent{ID: id, Action: "connector.deleted"})
	return nil
}

// resolveConnector prepares a task that references a connector: the type
// comes from the connector and connection fields are not stored on the task.
func (e *Engine) resolveConnector(t *model.Task) error {
	if t.Source.ConnectorID == "" {
		return nil
	}
	c, err := e.connector(t.Source.ConnectorID)
	if err != nil {
		return err
	}
	t.Source = t.Source.StripConnection()
	t.Source.Type = c.Type
	if t.Source.Type == model.SourcePostgres && t.Source.Schema == "" {
		t.Source.Schema = "public"
	}
	if t.Source.Type == model.SourceMongoDB && t.Source.Database == "" && c.Config.Database == "" {
		return fmt.Errorf("source.database is required: the connector does not set one")
	}
	return nil
}
