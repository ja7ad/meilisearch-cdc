package alerts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/events"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/notify"
	"github.com/ja7ad/meilisearch-cdc/internal/secret"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// DestState is the health of a Meilisearch destination.
type DestState struct {
	ID      string
	Name    string
	Healthy bool
	Error   string
}

// Observer exposes what rules watch.
type Observer interface {
	List(state, q, tag string) ([]model.TaskView, error)
	Destinations(ctx context.Context) []DestState
}

// subject is what a rule evaluates: a task or a destination.
type subject struct {
	key      string // task id or "dest:<id>"
	taskID   string
	taskName string
	destID   string
	destName string
	task     *model.TaskView
}

// Notifier sends messages (implemented by notify.Service).
type Notifier interface {
	Mail(ctx context.Context, e notify.Email) error
	SendTelegram(ctx context.Context, chatID, html string) error
	// Webhook posts payload as JSON, signed with secret when it is set.
	Webhook(ctx context.Context, url, secret string, payload any) error
	PublicURL(fallback string) string
}

// ErrNotFound is returned for unknown rules.
var ErrNotFound = errors.New("alert rule not found")

// ErrNoChannel refuses rules while neither email nor Telegram can deliver.
var ErrNoChannel = errors.New("alerts need a notification channel: configure Email (SMTP) or a Telegram bot in Settings first")

// Availability tells which delivery channels are configured.
type Availability struct {
	Email    bool `json:"email"`
	Telegram bool `json:"telegram"`
	Webhook  bool `json:"webhook"` // always available, needs no setup
}

// Manager stores rules and evaluates them periodically.
type Manager struct {
	obs    Observer
	notif  Notifier
	log    *slog.Logger
	cipher *secret.Cipher
	rules  *store.Bucket[Rule]
	evs    *store.Bucket[Event]
	states *store.Bucket[state]
	// Bus publishes events as they fire and resolve.
	Bus *events.Bus[Event]
	// Interval between evaluations.
	Interval time.Duration
	// Available reports the configured channels; when set, rules can only
	// be created (or enabled) with a channel that can deliver.
	Available func() Availability

	mu      sync.Mutex
	samples map[string]*events.Ring[sample] // task id -> history
	now     func() time.Time
	wg      sync.WaitGroup
}

type sample struct {
	t        time.Time
	errors   int64
	dlq      int64
	progress int64
}

// New opens the buckets.
// Webhook secrets are encrypted with cipher (nil stores them as is).
func New(db *store.DB, obs Observer, notif Notifier, cipher *secret.Cipher, log *slog.Logger) (*Manager, error) {
	m := &Manager{obs: obs, notif: notif, log: log, cipher: cipher, Bus: events.NewBus[Event](), Interval: 5 * time.Second,
		samples: map[string]*events.Ring[sample]{}, now: time.Now}
	seal := func(r Rule) (Rule, error) {
		var err error
		r.Channels.WebhookSecret, err = cipher.Rewrap(r.Channels.WebhookSecret, ruleAAD(r.ID))
		return r, err
	}
	var err error
	if m.rules, err = store.NewBucket(db, "alert_rules", store.WithHooks(seal, nil),
		store.WithValidation(func(r Rule) error { return r.Normalize() })); err != nil {
		return nil, err
	}
	if m.evs, err = store.NewBucket[Event](db, "alert_events"); err != nil {
		return nil, err
	}
	if m.states, err = store.NewBucket[state](db, "alert_states"); err != nil {
		return nil, err
	}
	return m, nil
}

func ruleAAD(id string) string { return "alert:" + id + ":webhookSecret" }

// webhookSecret returns the decrypted webhook secret of a stored rule.
func (m *Manager) webhookSecret(r Rule) (string, error) {
	return m.cipher.Decrypt(r.Channels.WebhookSecret, ruleAAD(r.ID))
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Rules lists rules.
func (m *Manager) Rules() ([]Rule, error) {
	out, err := m.rules.List("", 0)
	if out == nil {
		out = []Rule{}
	}
	return out, err
}

// Rule returns one rule.
func (m *Manager) Rule(id string) (Rule, error) {
	r, ok, err := m.rules.Get(id)
	if err == nil && !ok {
		err = ErrNotFound
	}
	return r, err
}

// Create validates and stores a rule.
func (m *Manager) Create(r Rule) (Rule, error) {
	if err := r.Normalize(); err != nil {
		return r, err
	}
	if err := m.checkChannels(r); err != nil {
		return r, err
	}
	r.ID = newID()
	r.CreatedAt = m.now().UTC()
	r.UpdatedAt = r.CreatedAt
	return r, m.rules.Put(r.ID, r)
}

// Channels returns the configured delivery channels.
func (m *Manager) Channels() Availability {
	if m.Available == nil {
		return Availability{Email: true, Telegram: true, Webhook: true}
	}
	a := m.Available()
	a.Webhook = true
	return a
}

// checkChannels requires email or Telegram to be configured and every
// channel of the rule to be deliverable.
func (m *Manager) checkChannels(r Rule) error {
	if m.Available == nil {
		return nil
	}
	a := m.Available()
	if !a.Email && !a.Telegram {
		return ErrNoChannel
	}
	var errs []error
	if len(r.Channels.Emails) > 0 && !a.Email {
		errs = append(errs, errors.New("email recipients need SMTP: configure it in Settings → Email"))
	}
	if len(r.Channels.TelegramChatIDs) > 0 && !a.Telegram {
		errs = append(errs, errors.New("telegram chats need a bot: configure it in Settings → Telegram"))
	}
	if len(r.Channels.Emails) == 0 && len(r.Channels.TelegramChatIDs) == 0 && r.Channels.WebhookURL == "" {
		errs = append(errs, errors.New("add at least one recipient: an email, a Telegram chat or a webhook"))
	}
	return errors.Join(errs...)
}

// Update replaces a rule; its alert state restarts.
func (m *Manager) Update(id string, r Rule) (Rule, error) {
	prev, err := m.Rule(id)
	if err != nil {
		return r, err
	}
	if r.Channels.WebhookSecret == SecretMask {
		r.Channels.WebhookSecret = "" // the URL was removed: the secret goes too
		if strings.TrimSpace(r.Channels.WebhookURL) != "" {
			r.Channels.WebhookSecret = prev.Channels.WebhookSecret // still encrypted; kept as is
		}
	}
	if err := r.Normalize(); err != nil {
		return r, err
	}
	// A disabled rule can always be saved, e.g. after SMTP was removed.
	if r.Enabled {
		if err := m.checkChannels(r); err != nil {
			return r, err
		}
	}
	r.ID, r.CreatedAt, r.UpdatedAt = id, prev.CreatedAt, m.now().UTC()
	if err := m.rules.Put(id, r); err != nil {
		return r, err
	}
	// A changed condition or target set restarts the alert state.
	if !r.Enabled || !reflect.DeepEqual(r.Condition, prev.Condition) || !slices.Equal(r.TaskIDs, prev.TaskIDs) {
		m.dropStates(id)
	}
	return r, nil
}

// Delete removes a rule and its state.
func (m *Manager) Delete(id string) error {
	if _, err := m.Rule(id); err != nil {
		return err
	}
	m.dropStates(id)
	return m.rules.Delete(id)
}

func (m *Manager) dropStates(ruleID string) { _, _ = m.states.DeletePrefix(ruleID + "|") }

// PurgeEvents deletes the alert history and every alert state, so firing
// alerts start over as resolved. Rules stay.
func (m *Manager) PurgeEvents() (int, error) {
	n, err := m.evs.Clear()
	if err != nil {
		return n, err
	}
	_, err = m.states.Clear()
	return n, err
}

// PurgeRules deletes every rule and its alert state. The history stays.
func (m *Manager) PurgeRules() (int, error) {
	n, err := m.rules.Clear()
	if err != nil {
		return n, err
	}
	_, err = m.states.Clear()
	return n, err
}

// Events returns events newest first.
func (m *Manager) Events(limit int, ruleID string, st EventState) ([]Event, error) {
	var all []Event
	err := m.evs.ForEach("", func(_ string, e Event) bool {
		if (ruleID == "" || e.RuleID == ruleID) && (st == "" || e.State == st) {
			all = append(all, e)
		}
		return true
	})
	out := make([]Event, 0, min(limit, len(all)))
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, all[i])
	}
	return out, err
}

// Active returns the firing alerts.
func (m *Manager) Active() ([]Event, error) {
	out := []Event{}
	err := m.states.ForEach("", func(_ string, s state) bool {
		if s.Firing && s.EventID != "" {
			if e, ok, _ := m.evs.Get(s.EventID); ok {
				out = append(out, e)
			}
		}
		return true
	})
	return out, err
}

// Run evaluates rules until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.wg.Wait()
			return
		case <-t.C:
			m.Evaluate(ctx)
		}
	}
}

// Evaluate runs every enabled rule once.
func (m *Manager) Evaluate(ctx context.Context) {
	rules, err := m.Rules()
	if err != nil {
		m.log.Error("alerts: list rules", "err", err)
		return
	}
	tasks, err := m.obs.List("", "", "")
	if err != nil {
		m.log.Error("alerts: list tasks", "err", err)
		return
	}
	now := m.now()
	m.record(tasks, now)
	var dests []DestState
	if slices.ContainsFunc(rules, func(r Rule) bool { return r.Enabled && r.Condition.Type == CondMeiliDown }) {
		dests = m.obs.Destinations(ctx)
	}
	byID := map[string]model.TaskView{}
	for _, t := range tasks {
		byID[t.ID] = t
	}

	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		seen := map[string]bool{}
		if r.Condition.Type == CondMeiliDown {
			for _, d := range dests {
				if len(r.DestinationIDs) > 0 && !slices.Contains(r.DestinationIDs, d.ID) {
					continue
				}
				sub := subject{key: "dest:" + d.ID, destID: d.ID, destName: d.Name}
				seen[sub.key] = true
				m.step(ctx, r, sub, !d.Healthy, fmt.Sprintf("Meilisearch %q is unreachable: %s", d.Name, d.Error), now)
			}
		} else {
			for _, t := range tasks {
				if len(r.TaskIDs) > 0 && !slices.Contains(r.TaskIDs, t.ID) {
					continue
				}
				sub := subject{key: t.ID, taskID: t.ID, taskName: t.Name, destID: t.Index.DestinationID, task: &t}
				seen[sub.key] = true
				on, msg := m.check(r.Condition, t, now)
				m.step(ctx, r, sub, on, msg, now)
			}
		}
		// Subjects that disappeared (deleted tasks or destinations) are
		// dropped silently. Collected first: resolving writes to the store.
		type gone struct {
			key string
			s   state
		}
		var dropped []gone
		_ = m.states.ForEach(r.ID+"|", func(k string, s state) bool {
			if !seen[s.TaskID] {
				dropped = append(dropped, gone{k, s})
			}
			return true
		})
		for _, g := range dropped {
			if g.s.Firing {
				sub := subject{key: g.s.TaskID, taskID: g.s.TaskID}
				if t, ok := byID[g.s.TaskID]; ok {
					sub.taskName = t.Name
				}
				m.resolve(ctx, r, g.s, sub, "it no longer exists", false)
			}
			_ = m.states.Delete(g.key)
		}
	}
}

// record keeps per task history for windowed conditions.
func (m *Manager) record(tasks []model.TaskView, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	live := map[string]bool{}
	for _, t := range tasks {
		live[t.ID] = true
		ring, ok := m.samples[t.ID]
		if !ok {
			// Two hours at the default interval.
			ring = events.NewRing[sample](1440)
			m.samples[t.ID] = ring
		}
		c := t.Status.Counters
		ring.Push(sample{t: now, errors: c.Errors, dlq: c.DLQ, progress: c.Upserted + c.Deleted + t.Status.Snapshot.Done})
	}
	for id := range m.samples {
		if !live[id] {
			delete(m.samples, id)
		}
	}
}

// delta returns how much a counter grew over the window, and the time since
// the progress counter last changed.
func (m *Manager) history(taskID string, window time.Duration, now time.Time) (errs, dlq int64, stillFor time.Duration) {
	m.mu.Lock()
	ring := m.samples[taskID]
	m.mu.Unlock()
	if ring == nil {
		return 0, 0, 0
	}
	vals := ring.Values()
	if len(vals) == 0 {
		return 0, 0, 0
	}
	last := vals[len(vals)-1]
	base := vals[0]
	for _, s := range vals {
		if now.Sub(s.t) <= window {
			base = s
			break
		}
	}
	// Counters start at zero for a fresh history, so compare to the base sample.
	errs, dlq = last.errors-base.errors, last.dlq-base.dlq
	changed := vals[0].t
	for i := len(vals) - 1; i > 0; i-- {
		if vals[i].progress != vals[i-1].progress {
			changed = vals[i].t
			break
		}
	}
	return errs, dlq, now.Sub(changed)
}

// check evaluates a condition for a task.
func (m *Manager) check(c Condition, t model.TaskView, now time.Time) (bool, string) {
	st := t.Status
	switch c.Type {
	case CondTaskState:
		if slices.Contains(c.States, st.State) {
			msg := fmt.Sprintf("Task %q is %s", t.Name, st.State)
			if st.Error != "" {
				msg += ": " + st.Error
			} else if st.Message != "" {
				msg += ": " + st.Message
			}
			return true, msg
		}
	case CondLag:
		if st.State == model.StateStreaming && st.LagMs > c.ThresholdMs {
			return true, fmt.Sprintf("Task %q lags %s behind the source (threshold %s)", t.Name,
				time.Duration(st.LagMs)*time.Millisecond, time.Duration(c.ThresholdMs)*time.Millisecond)
		}
	case CondDLQ:
		_, dlq, _ := m.history(t.ID, c.Window.D(), now)
		if dlq >= c.Count {
			return true, fmt.Sprintf("Task %q sent %d document(s) to the dead letter queue in the last %s", t.Name, dlq, c.Window.D())
		}
	case CondErrors:
		errs, _, _ := m.history(t.ID, c.Window.D(), now)
		if errs >= c.Count {
			return true, fmt.Sprintf("Task %q had %d pipeline error(s) in the last %s (last: %s)", t.Name, errs, c.Window.D(), st.Error)
		}
	case CondStalled:
		switch st.State {
		case model.StateStarting, model.StateSnapshotting, model.StateResyncing, model.StateRetrying:
			if _, _, still := m.history(t.ID, 0, now); still >= c.For.D() {
				return true, fmt.Sprintf("Task %q has been %s without progress for %s", t.Name, st.State, still.Round(time.Second))
			}
		}
	}
	return false, ""
}

// step advances the state machine of one (rule, target).
func (m *Manager) step(ctx context.Context, r Rule, sub subject, on bool, msg string, now time.Time) {
	key := stateKey(r.ID, sub.key)
	s, exists, _ := m.states.Get(key)
	if !on {
		if exists {
			if s.Firing {
				m.resolve(ctx, r, s, sub, "", r.NotifyResolved)
			}
			_ = m.states.Delete(key)
		}
		return
	}
	if !exists {
		s = state{RuleID: r.ID, TaskID: sub.key, Since: now}
	}
	wait := r.Condition.For.D()
	if r.Condition.Type == CondStalled {
		wait = 0 // the condition already includes its duration
	}
	switch {
	case !s.Firing && now.Sub(s.Since) >= wait:
		ev := m.newEvent(r, sub, Firing, msg, false)
		s.Firing, s.LastNotified, s.EventID = true, now, ev.ID
		m.emit(ctx, r, ev, true)
	case s.Firing && now.Sub(s.LastNotified) >= r.Cooldown.D():
		ev := m.newEvent(r, sub, Firing, msg, true)
		s.LastNotified, s.EventID = now, ev.ID
		m.emit(ctx, r, ev, true)
	}
	_ = m.states.Put(key, s)
}

func (m *Manager) resolve(ctx context.Context, r Rule, s state, sub subject, why string, notifyIt bool) {
	msg := "The condition cleared"
	switch {
	case why != "":
		msg = "The alert was closed: " + why
	case sub.task != nil:
		msg = fmt.Sprintf("Task %q is healthy again (%s)", sub.taskName, sub.task.Status.State)
	case sub.destName != "":
		msg = fmt.Sprintf("Meilisearch %q is reachable again", sub.destName)
	}
	m.emit(ctx, r, m.newEvent(r, sub, Resolved, msg, false), notifyIt)
}

func (m *Manager) newEvent(r Rule, sub subject, st EventState, msg string, reminder bool) Event {
	return Event{ID: fmt.Sprintf("%020d-%s", m.now().UnixNano(), newID()), RuleID: r.ID, RuleName: r.Name, Severity: r.Severity,
		TaskID: sub.taskID, TaskName: sub.taskName, DestinationID: sub.destID, DestinationName: sub.destName,
		State: st, Message: msg, At: m.now().UTC(), Reminder: reminder, Deliveries: []Delivery{}}
}

// emit stores and publishes an event and notifies in the background.
func (m *Manager) emit(ctx context.Context, r Rule, ev Event, notifyIt bool) {
	_ = m.evs.Put(ev.ID, ev)
	m.Bus.Publish(ev)
	if !notifyIt {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ev.Deliveries = m.deliver(context.WithoutCancel(ctx), r, ev)
		_ = m.evs.Put(ev.ID, ev)
		m.Bus.Publish(ev)
		for _, d := range ev.Deliveries {
			if !d.OK {
				m.log.Warn("alert delivery failed", "rule", r.Name, "channel", d.Channel, "target", d.Target, "err", d.Error)
			}
		}
	}()
}

// Test sends a sample notification on every channel of the rule.
func (m *Manager) Test(ctx context.Context, id string) ([]Delivery, error) {
	r, err := m.Rule(id)
	if err != nil {
		return nil, err
	}
	ev := Event{RuleID: r.ID, RuleName: r.Name, Severity: r.Severity, State: Firing, At: m.now().UTC(), Test: true,
		Message: "This is a test notification for the alert rule \"" + r.Name + "\"."}
	return m.deliver(ctx, r, ev), nil
}

// deliver sends ev to every channel concurrently.
func (m *Manager) deliver(ctx context.Context, r Rule, ev Event) []Delivery {
	base := m.notif.PublicURL("")
	link := ""
	if base != "" {
		link = base + "/#/alerts"
		if ev.TaskID != "" {
			link = base + "/#/tasks/" + ev.TaskID
		}
	}
	sev := string(ev.Severity)
	label := strings.ToUpper(sev)
	if ev.State == Resolved {
		sev, label = "resolved", "RESOLVED"
	}
	if ev.Test {
		label = "TEST · " + label
	}
	subject := fmt.Sprintf("[%s] %s", label, r.Name)
	if ev.TaskName != "" {
		subject += " · " + ev.TaskName
	} else if ev.DestinationName != "" {
		subject += " · " + ev.DestinationName
	}

	var (
		mu  sync.Mutex
		out []Delivery
		wg  sync.WaitGroup
	)
	add := func(channel, target string, err error) {
		d := Delivery{Channel: channel, Target: target, OK: err == nil, At: m.now().UTC()}
		if err != nil {
			d.Error = err.Error()
		}
		mu.Lock()
		out = append(out, d)
		mu.Unlock()
	}
	if len(r.Channels.Emails) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := m.notif.Mail(ctx, notify.AlertEmail(r.Channels.Emails, subject, r.Name, ev.Message, link, sev))
			add("email", strings.Join(r.Channels.Emails, ", "), err)
		}()
	}
	icon := map[string]string{"critical": "🔴", "warning": "🟠", "info": "🔵", "resolved": "🟢"}[sev]
	tg := fmt.Sprintf("%s <b>%s</b> · %s\n%s", icon, notify.HTMLEscape(label), notify.HTMLEscape(r.Name), notify.HTMLEscape(ev.Message))
	if link != "" {
		tg += fmt.Sprintf("\n<a href=\"%s\">Open dashboard</a>", notify.HTMLEscape(link))
	}
	for _, chat := range r.Channels.TelegramChatIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			add("telegram", chat, m.notif.SendTelegram(ctx, chat, tg))
		}()
	}
	if r.Channels.WebhookURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := map[string]any{"text": subject + "\n" + ev.Message, "event": ev, "link": link}
			sec, err := m.webhookSecret(r)
			if err == nil {
				err = m.notif.Webhook(ctx, r.Channels.WebhookURL, sec, payload)
			}
			add("webhook", r.Channels.WebhookURL, err)
		}()
	}
	wg.Wait()
	if out == nil {
		out = []Delivery{}
	}
	return out
}

// Wait blocks until background deliveries finish (tests, shutdown).
func (m *Manager) Wait() { m.wg.Wait() }
