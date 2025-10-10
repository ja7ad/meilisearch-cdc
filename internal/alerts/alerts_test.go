package alerts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/notify"
	"github.com/ja7ad/meilisearch-cdc/internal/secret"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

type fakeObs struct {
	mu      sync.Mutex
	tasks   []model.TaskView
	meiliUp bool
	extra   []DestState
}

func (f *fakeObs) List(string, string, string) ([]model.TaskView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.TaskView(nil), f.tasks...), nil
}
func (f *fakeObs) Destinations(context.Context) []DestState {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []DestState{{ID: "default", Name: "Main", Healthy: f.meiliUp, Error: "connection refused"}}
	return append(out, f.extra...)
}

func (f *fakeObs) set(id string, fn func(*model.Status)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.tasks {
		if f.tasks[i].ID == id {
			fn(&f.tasks[i].Status)
		}
	}
}

type sent struct{ channel, target, text string }

type fakeNotif struct {
	mu     sync.Mutex
	sent   []sent
	failTG bool
}

func (f *fakeNotif) add(s sent) { f.mu.Lock(); f.sent = append(f.sent, s); f.mu.Unlock() }
func (f *fakeNotif) Mail(_ context.Context, e notify.Email) error {
	f.add(sent{"email", e.To[0], e.Subject})
	return nil
}
func (f *fakeNotif) SendTelegram(_ context.Context, chat, html string) error {
	if f.failTG {
		return errors.New("chat not found")
	}
	f.add(sent{"telegram", chat, html})
	return nil
}
func (f *fakeNotif) Webhook(_ context.Context, url, secret string, _ any) error {
	f.add(sent{"webhook", url, secret})
	return nil
}
func (f *fakeNotif) PublicURL(string) string { return "https://cdc.example.com" }
func (f *fakeNotif) count() int              { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

type harness struct {
	m     *Manager
	obs   *fakeObs
	notif *fakeNotif
	now   time.Time
	db    *store.DB
}

func newHarness(t *testing.T) *harness {
	db, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := &harness{db: db, obs: &fakeObs{meiliUp: true}, notif: &fakeNotif{}, now: time.Unix(1_700_000_000, 0)}
	h.obs.tasks = []model.TaskView{
		{Task: model.Task{ID: "t1", Name: "Products"}, Status: model.Status{State: model.StateStreaming}},
		{Task: model.Task{ID: "t2", Name: "Orders"}, Status: model.Status{State: model.StateStreaming}},
	}
	h.m = h.newManager(t)
	return h
}

func (h *harness) newManager(t *testing.T) *Manager {
	c, err := secret.New("alerts test key")
	require.NoError(t, err)
	m, err := New(h.db, h.obs, h.notif, c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	m.now = func() time.Time { return h.now }
	return m
}

func (h *harness) tick(d time.Duration) {
	h.now = h.now.Add(d)
	h.m.Evaluate(context.Background())
	h.m.Wait()
}

func TestRuleValidation(t *testing.T) {
	r := Rule{Name: " x ", Condition: Condition{Type: CondDLQ}, Channels: Channels{Emails: []string{" Ops@X.io ", "ops@x.io", ""}}}
	require.NoError(t, r.Normalize())
	assert.Equal(t, "x", r.Name)
	assert.Equal(t, SevWarning, r.Severity)
	assert.Equal(t, int64(1), r.Condition.Count)
	assert.Equal(t, 5*time.Minute, r.Condition.Window.D())
	assert.Equal(t, 30*time.Minute, r.Cooldown.D())
	assert.Equal(t, []string{"ops@x.io"}, r.Channels.Emails)

	bad := Rule{Condition: Condition{Type: "boom"}, Severity: "loud", Cooldown: model.Duration(time.Second),
		Channels: Channels{Emails: []string{"nope"}, WebhookURL: "ftp://x"}}
	err := bad.Normalize()
	require.Error(t, err)
	for _, s := range []string{"name is required", "unknown condition", "unknown severity", "cooldown", "invalid email", "webhookUrl"} {
		assert.Contains(t, err.Error(), s)
	}
	lag := Rule{Name: "lag", Condition: Condition{Type: CondLag}}
	assert.ErrorContains(t, lag.Normalize(), "thresholdMs")
}

func TestRulesNeedADeliveryChannel(t *testing.T) {
	h := newHarness(t)
	avail := Availability{}
	h.m.Available = func() Availability { return avail }
	mail := Rule{Name: "m", Enabled: true, Condition: Condition{Type: CondMeiliDown}, Channels: Channels{Emails: []string{"a@x.io"}}}
	tg := Rule{Name: "t", Enabled: true, Condition: Condition{Type: CondMeiliDown}, Channels: Channels{TelegramChatIDs: []string{"1"}}}

	_, err := h.m.Create(mail)
	assert.ErrorIs(t, err, ErrNoChannel)
	assert.Equal(t, Availability{Webhook: true}, h.m.Channels())

	avail.Telegram = true
	_, err = h.m.Create(mail)
	assert.ErrorContains(t, err, "SMTP")
	saved, err := h.m.Create(tg)
	require.NoError(t, err)
	_, err = h.m.Create(Rule{Name: "none", Condition: Condition{Type: CondMeiliDown}})
	assert.ErrorContains(t, err, "at least one recipient")

	avail.Email = true
	_, err = h.m.Create(mail)
	assert.NoError(t, err)

	// The bot is removed: the rule can be disabled but not re-enabled.
	avail.Telegram = false
	saved.Enabled = false
	_, err = h.m.Update(saved.ID, saved)
	assert.NoError(t, err)
	saved.Enabled = true
	_, err = h.m.Update(saved.ID, saved)
	assert.ErrorContains(t, err, "Telegram")
}

func TestTaskStateFiresOnceThenRemindsAndResolves(t *testing.T) {
	h := newHarness(t)
	r, err := h.m.Create(Rule{Name: "Task down", Enabled: true, Severity: SevCritical, NotifyResolved: true,
		Condition: Condition{Type: CondTaskState, For: model.Duration(time.Minute)},
		Channels:  Channels{Emails: []string{"ops@x.io"}, TelegramChatIDs: []string{"-100"}},
		Cooldown:  model.Duration(10 * time.Minute)})
	require.NoError(t, err)
	sub := h.m.Bus.Subscribe(16)
	defer sub.Close()

	h.tick(5 * time.Second)
	assert.Zero(t, h.notif.count())

	h.obs.set("t1", func(s *model.Status) { s.State = model.StateFailed; s.Error = "auth failed" })
	h.tick(5 * time.Second)
	assert.Zero(t, h.notif.count(), "waits for the 'for' duration")
	h.tick(time.Minute)
	assert.Equal(t, 2, h.notif.count(), "email + telegram")
	active, _ := h.m.Active()
	require.Len(t, active, 1)
	assert.Equal(t, "t1", active[0].TaskID)
	assert.Contains(t, active[0].Message, "auth failed")
	assert.Len(t, active[0].Deliveries, 2)
	ev := <-sub.C
	assert.Equal(t, Firing, ev.State)

	h.tick(time.Minute)
	assert.Equal(t, 2, h.notif.count(), "cooldown")
	h.tick(10 * time.Minute)
	assert.Equal(t, 4, h.notif.count(), "reminder after the cooldown")

	// A restart must not notify again while still firing.
	h.m = h.newManager(t)
	h.tick(5 * time.Second)
	assert.Equal(t, 4, h.notif.count())

	h.obs.set("t1", func(s *model.Status) { s.State = model.StateStreaming; s.Error = "" })
	h.tick(5 * time.Second)
	assert.Equal(t, 6, h.notif.count(), "resolved notification")
	active, _ = h.m.Active()
	assert.Empty(t, active)
	evs, _ := h.m.Events(10, r.ID, "")
	require.NotEmpty(t, evs)
	assert.Equal(t, Resolved, evs[0].State, "newest first")
	resolved, _ := h.m.Events(10, "", Resolved)
	assert.Len(t, resolved, 1)
}

func TestWindowedConditionsAndScoping(t *testing.T) {
	h := newHarness(t)
	_, err := h.m.Create(Rule{Name: "DLQ", Enabled: true, TaskIDs: []string{"t2"},
		Condition: Condition{Type: CondDLQ, Count: 3, Window: model.Duration(time.Minute)}, Channels: Channels{WebhookURL: "https://hooks.example.com/x"}})
	require.NoError(t, err)
	h.tick(5 * time.Second)
	h.obs.set("t1", func(s *model.Status) { s.Counters.DLQ = 50 })
	h.obs.set("t2", func(s *model.Status) { s.Counters.DLQ = 2 })
	h.tick(5 * time.Second)
	assert.Zero(t, h.notif.count(), "t1 is out of scope, t2 below the count")
	h.obs.set("t2", func(s *model.Status) { s.Counters.DLQ = 3 })
	h.tick(5 * time.Second)
	assert.Equal(t, 1, h.notif.count())
	assert.Equal(t, "webhook", h.notif.sent[0].channel)
	// The window slides past the increase: the alert resolves.
	h.tick(2 * time.Minute)
	h.tick(5 * time.Second)
	active, _ := h.m.Active()
	assert.Empty(t, active)
}

func TestLagMeiliDownAndDeletedTasks(t *testing.T) {
	h := newHarness(t)
	_, _ = h.m.Create(Rule{Name: "Lag", Enabled: true, Condition: Condition{Type: CondLag, ThresholdMs: 5000}, Channels: Channels{Emails: []string{"a@x.io"}}})
	_, _ = h.m.Create(Rule{Name: "Meili", Enabled: true, Condition: Condition{Type: CondMeiliDown}, Channels: Channels{Emails: []string{"a@x.io"}}})
	_, _ = h.m.Create(Rule{Name: "Off", Enabled: false, Condition: Condition{Type: CondMeiliDown}, Channels: Channels{Emails: []string{"b@x.io"}}})

	h.obs.set("t1", func(s *model.Status) { s.LagMs = 9000 })
	h.obs.meiliUp = false
	h.tick(5 * time.Second)
	active, _ := h.m.Active()
	assert.Len(t, active, 2)
	assert.Equal(t, 2, h.notif.count(), "the disabled rule stays silent")

	// Deleting the lagging task drops its alert.
	h.obs.mu.Lock()
	h.obs.tasks = h.obs.tasks[1:]
	h.obs.mu.Unlock()
	h.obs.meiliUp = true
	h.tick(5 * time.Second)
	active, _ = h.m.Active()
	assert.Empty(t, active)
}

func TestStalled(t *testing.T) {
	h := newHarness(t)
	_, _ = h.m.Create(Rule{Name: "Stuck", Enabled: true, Condition: Condition{Type: CondStalled, For: model.Duration(time.Minute)}})
	h.obs.set("t1", func(s *model.Status) { s.State = model.StateSnapshotting; s.Snapshot.Done = 10 })
	h.tick(5 * time.Second)
	h.obs.set("t1", func(s *model.Status) { s.Snapshot.Done = 20 })
	h.tick(30 * time.Second)
	active, _ := h.m.Active()
	assert.Empty(t, active, "progressing")
	for i := 0; i < 3; i++ {
		h.tick(30 * time.Second)
	}
	active, _ = h.m.Active()
	require.Len(t, active, 1)
	assert.Contains(t, active[0].Message, "without progress")
}

func TestTestNotification(t *testing.T) {
	h := newHarness(t)
	h.notif.failTG = true
	r, _ := h.m.Create(Rule{Name: "All", Enabled: true, Condition: Condition{Type: CondMeiliDown},
		Channels: Channels{Emails: []string{"a@x.io"}, TelegramChatIDs: []string{"1"}, WebhookURL: "https://h.example.com"}})
	res, err := h.m.Test(context.Background(), r.ID)
	require.NoError(t, err)
	require.Len(t, res, 3)
	byChannel := map[string]Delivery{}
	for _, d := range res {
		byChannel[d.Channel] = d
	}
	assert.True(t, byChannel["email"].OK)
	assert.True(t, byChannel["webhook"].OK)
	assert.False(t, byChannel["telegram"].OK)
	assert.Equal(t, "chat not found", byChannel["telegram"].Error)
	_, err = h.m.Test(context.Background(), "missing")
	assert.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, h.m.Delete(r.ID))
	assert.ErrorIs(t, h.m.Delete(r.ID), ErrNotFound)
}

func TestMeiliDownPerDestination(t *testing.T) {
	h := newHarness(t)
	h.obs.extra = []DestState{{ID: "eu", Name: "EU replica", Healthy: false, Error: "timeout"}, {ID: "us", Name: "US", Healthy: true}}
	_, err := h.m.Create(Rule{Name: "Any Meili", Enabled: true, Condition: Condition{Type: CondMeiliDown}, Channels: Channels{Emails: []string{"a@x.io"}}})
	require.NoError(t, err)
	_, err = h.m.Create(Rule{Name: "US only", Enabled: true, DestinationIDs: []string{"us"}, Condition: Condition{Type: CondMeiliDown}})
	require.NoError(t, err)
	h.tick(5 * time.Second)
	active, _ := h.m.Active()
	require.Len(t, active, 1)
	assert.Equal(t, "eu", active[0].DestinationID)
	assert.Equal(t, "EU replica", active[0].DestinationName)
	assert.Contains(t, active[0].Message, "timeout")
	assert.Contains(t, h.notif.sent[0].text, "EU replica")

	h.obs.mu.Lock()
	h.obs.extra[0].Healthy = true
	h.obs.mu.Unlock()
	h.tick(5 * time.Second)
	active, _ = h.m.Active()
	assert.Empty(t, active)
}

func TestWebhookSecret(t *testing.T) {
	h := newHarness(t)
	r := Rule{Name: "hook", Enabled: true, Condition: Condition{Type: CondTaskState, States: []model.State{model.StatePaused}},
		Severity: SevWarning, Channels: Channels{WebhookURL: "https://hooks.example.com/cdc", WebhookSecret: "short"}}
	_, err := h.m.Create(r)
	require.ErrorContains(t, err, "at least 16")

	r.Channels.WebhookSecret = "whsec_0123456789abcdef"
	created, err := h.m.Create(r)
	require.NoError(t, err)
	stored, err := h.m.Rule(created.ID)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stored.Channels.WebhookSecret, "enc:"), "encrypted at rest")
	assert.Equal(t, SecretMask, stored.Redacted().Channels.WebhookSecret)

	// Saving the masked value keeps the secret; deliveries use the plain one.
	upd := stored.Redacted()
	upd.Name = "hook 2"
	_, err = h.m.Update(created.ID, upd)
	require.NoError(t, err)
	res, err := h.m.Test(context.Background(), created.ID)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, "whsec_0123456789abcdef", h.notif.sent[len(h.notif.sent)-1].text)

	// Removing the URL drops the masked secret instead of failing.
	upd.Channels.WebhookURL = ""
	upd.Channels.Emails = []string{"ops@example.com"}
	saved, err := h.m.Update(created.ID, upd)
	require.NoError(t, err)
	assert.Empty(t, saved.Channels.WebhookSecret)

	// A secret without a URL is refused.
	r.Channels.WebhookURL = ""
	_, err = h.m.Create(r)
	require.ErrorContains(t, err, "needs a webhook URL")
}
