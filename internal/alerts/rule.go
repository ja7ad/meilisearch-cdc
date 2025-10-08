// Package alerts evaluates alert rules against task and Meilisearch health
// and notifies by email, Telegram and webhooks.
package alerts

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/signature"
)

// ConditionType selects what a rule watches.
type ConditionType string

const (
	CondTaskState ConditionType = "task_state"
	CondLag       ConditionType = "lag"
	CondDLQ       ConditionType = "dlq"
	CondErrors    ConditionType = "errors"
	CondMeiliDown ConditionType = "meili_down"
	CondStalled   ConditionType = "stalled"
)

// Severity of a rule.
type Severity string

const (
	SevInfo     Severity = "info"
	SevWarning  Severity = "warning"
	SevCritical Severity = "critical"
)

// Condition parameters; only the ones relevant to Type are used.
type Condition struct {
	Type        ConditionType  `json:"type"`
	States      []model.State  `json:"states,omitempty"`
	ThresholdMs int64          `json:"thresholdMs,omitempty"`
	Count       int64          `json:"count,omitempty"`
	Window      model.Duration `json:"window,omitempty"`
	For         model.Duration `json:"for,omitempty"`
}

// Channels lists where notifications go.
type Channels struct {
	Emails          []string `json:"emails"`
	TelegramChatIDs []string `json:"telegramChatIds"`
	WebhookURL      string   `json:"webhookUrl,omitempty"`
	// WebhookSecret signs webhook deliveries (X-CDC-Signature, see package
	// signature). Encrypted at rest and masked in responses.
	WebhookSecret string `json:"webhookSecret,omitempty"`
}

// SecretMask replaces the webhook secret in responses; sending it back
// keeps the stored secret.
const SecretMask = model.SecretMask

// Redacted returns a copy safe to return from the API.
func (r Rule) Redacted() Rule {
	if r.Channels.WebhookSecret != "" {
		r.Channels.WebhookSecret = SecretMask
	}
	return r
}

// Rule is an alert definition.
type Rule struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	Condition Condition `json:"condition"`
	TaskIDs   []string  `json:"taskIds"`
	// DestinationIDs limits meili_down rules (empty = every destination).
	DestinationIDs []string       `json:"destinationIds"`
	Severity       Severity       `json:"severity"`
	Channels       Channels       `json:"channels"`
	Cooldown       model.Duration `json:"cooldown"`
	NotifyResolved bool           `json:"notifyResolved"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

// Normalize fills defaults and validates the rule.
func (r *Rule) Normalize() error {
	var errs []error
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if r.Severity == "" {
		r.Severity = SevWarning
	}
	if !slices.Contains([]Severity{SevInfo, SevWarning, SevCritical}, r.Severity) {
		errs = append(errs, fmt.Errorf("unknown severity %q", r.Severity))
	}
	c := &r.Condition
	switch c.Type {
	case CondTaskState:
		if len(c.States) == 0 {
			c.States = []model.State{model.StateFailed, model.StateHeld}
		}
	case CondLag:
		if c.ThresholdMs <= 0 {
			errs = append(errs, errors.New("condition.thresholdMs must be positive"))
		}
	case CondDLQ, CondErrors:
		if c.Count <= 0 {
			c.Count = 1
		}
		if c.Window <= 0 {
			c.Window = model.Duration(5 * time.Minute)
		}
	case CondStalled:
		if c.For <= 0 {
			c.For = model.Duration(10 * time.Minute)
		}
	case CondMeiliDown:
		r.TaskIDs = nil
	default:
		errs = append(errs, fmt.Errorf("unknown condition type %q", c.Type))
	}
	if c.For < 0 || c.Window < 0 {
		errs = append(errs, errors.New("durations must not be negative"))
	}
	if r.Cooldown <= 0 {
		r.Cooldown = model.Duration(30 * time.Minute)
	}
	if r.Cooldown.D() < time.Minute {
		errs = append(errs, errors.New("cooldown must be at least 1m"))
	}
	r.Channels.Emails = clean(r.Channels.Emails)
	for i, e := range r.Channels.Emails {
		a, err := mail.ParseAddress(e)
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid email %q", e))
			continue
		}
		r.Channels.Emails[i] = strings.ToLower(a.Address)
	}
	r.Channels.Emails = clean(r.Channels.Emails) // dedupe after case folding
	r.Channels.TelegramChatIDs = clean(r.Channels.TelegramChatIDs)
	if u := strings.TrimSpace(r.Channels.WebhookURL); u != "" {
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			errs = append(errs, errors.New("channels.webhookUrl must be an http(s) URL"))
		}
		r.Channels.WebhookURL = u
	}
	r.Channels.WebhookSecret = strings.TrimSpace(r.Channels.WebhookSecret)
	switch sec := r.Channels.WebhookSecret; {
	case sec == "":
	case r.Channels.WebhookURL == "":
		errs = append(errs, errors.New("channels.webhookSecret needs a webhook URL"))
	case sec == SecretMask:
		errs = append(errs, errors.New("channels.webhookSecret: enter the secret again"))
	case len(sec) < signature.MinSecretLen:
		errs = append(errs, fmt.Errorf("channels.webhookSecret must be at least %d characters", signature.MinSecretLen))
	}
	if r.TaskIDs == nil {
		r.TaskIDs = []string{}
	}
	if r.DestinationIDs == nil {
		r.DestinationIDs = []string{}
	}
	return errors.Join(errs...)
}

func clean(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Delivery is the outcome of one notification.
type Delivery struct {
	Channel string    `json:"channel"`
	Target  string    `json:"target"`
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	At      time.Time `json:"at"`
}

// EventState is firing or resolved.
type EventState string

const (
	Firing   EventState = "firing"
	Resolved EventState = "resolved"
)

// Event is an alert transition.
type Event struct {
	ID       string   `json:"id"`
	RuleID   string   `json:"ruleId"`
	RuleName string   `json:"ruleName"`
	Severity Severity `json:"severity"`
	TaskID   string   `json:"taskId,omitempty"`
	TaskName string   `json:"taskName,omitempty"`
	// DestinationID/Name identify the Meilisearch instance (meili_down
	// alerts, or the destination of the task).
	DestinationID   string     `json:"destinationId,omitempty"`
	DestinationName string     `json:"destinationName,omitempty"`
	State           EventState `json:"state"`
	Message         string     `json:"message"`
	At              time.Time  `json:"at"`
	Reminder        bool       `json:"reminder,omitempty"`
	// Test marks the notification sent by "Send test"; it is never stored.
	Test       bool       `json:"test,omitempty"`
	Deliveries []Delivery `json:"deliveries"`
}

// state tracks one (rule, target) pair; it is persisted so restarts do
// not repeat notifications.
type state struct {
	RuleID       string    `json:"ruleId"`
	TaskID       string    `json:"taskId"`
	Since        time.Time `json:"since"`
	Firing       bool      `json:"firing"`
	LastNotified time.Time `json:"lastNotified"`
	EventID      string    `json:"eventId"`
}

func stateKey(ruleID, taskID string) string { return ruleID + "|" + taskID }
