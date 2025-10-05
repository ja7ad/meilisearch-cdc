package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/retry"
	"github.com/ja7ad/meilisearch-cdc/internal/secret"
	"github.com/ja7ad/meilisearch-cdc/internal/signature"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// Masked is returned instead of stored secrets.
const Masked = "••••••"

// Service stores notification settings and sends messages.
type Service struct {
	smtp     *store.Bucket[SMTPConfig]
	telegram *store.Bucket[TelegramConfig]
	// DefaultPublicURL is used when the SMTP settings set none (CDC_PUBLIC_URL).
	DefaultPublicURL string

	mu    sync.RWMutex
	cache struct {
		smtp     *SMTPConfig
		telegram *TelegramConfig
	}
}

// NewService opens the settings buckets; secrets are encrypted with c.
func NewService(db *store.DB, c *secret.Cipher, publicURL string) (*Service, error) {
	s := &Service{DefaultPublicURL: strings.TrimRight(publicURL, "/")}
	var err error
	s.smtp, err = store.NewBucket(db, "settings_smtp", store.WithHooks(
		func(v SMTPConfig) (SMTPConfig, error) {
			var err error
			v.Password, err = c.Rewrap(v.Password, "settings:smtp:password")
			return v, err
		},
		func(v SMTPConfig) (SMTPConfig, error) {
			var err error
			v.Password, err = c.Decrypt(v.Password, "settings:smtp:password")
			return v, err
		},
	))
	if err != nil {
		return nil, err
	}
	s.telegram, err = store.NewBucket(db, "settings_telegram", store.WithHooks(
		func(v TelegramConfig) (TelegramConfig, error) {
			var err error
			v.BotToken, err = c.Rewrap(v.BotToken, "settings:telegram:botToken")
			return v, err
		},
		func(v TelegramConfig) (TelegramConfig, error) {
			var err error
			v.BotToken, err = c.Decrypt(v.BotToken, "settings:telegram:botToken")
			return v, err
		},
	))
	return s, err
}

// SMTP returns the stored configuration (with secrets).
func (s *Service) SMTP() SMTPConfig {
	s.mu.RLock()
	if s.cache.smtp != nil {
		c := *s.cache.smtp
		s.mu.RUnlock()
		return c
	}
	s.mu.RUnlock()
	c, _, _ := s.smtp.Get("config")
	s.mu.Lock()
	s.cache.smtp = &c
	s.mu.Unlock()
	return c
}

// SetSMTP validates and stores the configuration. A masked password keeps
// the stored one.
func (s *Service) SetSMTP(c SMTPConfig) (SMTPConfig, error) {
	if c.Password == Masked {
		c.Password = s.SMTP().Password
	}
	c.Host = strings.TrimSpace(c.Host)
	c.PublicURL = strings.TrimRight(strings.TrimSpace(c.PublicURL), "/")
	if c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return c, fmt.Errorf("public URL must be an absolute http(s) URL")
		}
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	if err := s.smtp.Put("config", c); err != nil {
		return c, err
	}
	s.mu.Lock()
	s.cache.smtp = &c
	s.mu.Unlock()
	return c, nil
}

// Telegram returns the stored configuration (with secrets).
func (s *Service) Telegram() TelegramConfig {
	s.mu.RLock()
	if s.cache.telegram != nil {
		c := *s.cache.telegram
		s.mu.RUnlock()
		return c
	}
	s.mu.RUnlock()
	c, _, _ := s.telegram.Get("config")
	s.mu.Lock()
	s.cache.telegram = &c
	s.mu.Unlock()
	return c
}

// SetTelegram validates the token with getMe and stores the configuration.
func (s *Service) SetTelegram(ctx context.Context, c TelegramConfig) (TelegramConfig, error) {
	prev := s.Telegram()
	if c.BotToken == Masked {
		c.BotToken = prev.BotToken
	}
	c.APIURL = strings.TrimRight(strings.TrimSpace(c.APIURL), "/")
	if c.APIURL == DefaultTelegramAPI {
		c.APIURL = ""
	}
	c.BotName = ""
	if c.BotToken != "" {
		name, err := c.GetMe(ctx)
		if err != nil {
			return c, fmt.Errorf("telegram rejected the bot token: %w", err)
		}
		c.BotName = name
	}
	if err := s.telegram.Put("config", c); err != nil {
		return c, err
	}
	s.mu.Lock()
	s.cache.telegram = &c
	s.mu.Unlock()
	return c, nil
}

// Purge deletes the SMTP and Telegram settings and returns how many were
// stored.
func (s *Service) Purge() (int, error) {
	n := 0
	for _, clear := range []func() (int, error){s.smtp.Clear, s.telegram.Clear} {
		c, err := clear()
		if err != nil {
			return n, err
		}
		n += c
	}
	s.mu.Lock()
	s.cache.smtp, s.cache.telegram = nil, nil
	s.mu.Unlock()
	return n, nil
}

// PublicURL is the base for links in messages.
func (s *Service) PublicURL(fallback string) string {
	if u := s.SMTP().PublicURL; u != "" {
		return u
	}
	if s.DefaultPublicURL != "" {
		return s.DefaultPublicURL
	}
	return strings.TrimRight(fallback, "/")
}

// EmailConfigured reports whether email can be sent.
func (s *Service) EmailConfigured() bool { return s.SMTP().Configured() }

// TelegramConfigured reports whether Telegram messages can be sent.
func (s *Service) TelegramConfigured() bool { return s.Telegram().Configured() }

// Mail sends an email with the stored configuration.
func (s *Service) Mail(ctx context.Context, e Email) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	p := retry.Policy{MaxAttempts: 3, Initial: time.Second, Max: 5 * time.Second}
	return retry.Run(ctx, p, func(ctx context.Context, _ int) error { return SendMail(ctx, s.SMTP(), e) })
}

// Telegram message helper.
func (s *Service) SendTelegram(ctx context.Context, chatID, html string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return s.Telegram().SendMessage(ctx, chatID, html)
}

// Webhook posts a JSON payload. The body includes a "text" field so Slack,
// Mattermost and Discord-compatible endpoints render it directly. With a
// secret, every attempt is signed (X-CDC-Timestamp and X-CDC-Signature, see package signature) so the
// receiver can check it came from this service.
func (s *Service) Webhook(ctx context.Context, target, secret string, payload any) error {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid webhook URL")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	p := retry.Policy{MaxAttempts: 3, Initial: time.Second, Max: 5 * time.Second}
	return retry.Run(ctx, p, func(ctx context.Context, _ int) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
		if err != nil {
			return retry.Permanent(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "meilisearch-cdc")
		if secret != "" {
			signature.SetHeaders(req.Header, secret, time.Now(), raw)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		switch {
		case resp.StatusCode < 300:
			return nil
		case resp.StatusCode == 429 || resp.StatusCode >= 500:
			return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
		default:
			return retry.Permanent(fmt.Errorf("webhook returned HTTP %d", resp.StatusCode))
		}
	})
}

// HTMLEscape escapes text for Telegram HTML and email bodies.
func HTMLEscape(s string) string { return html.EscapeString(s) }

// layout wraps body HTML in a small branded email template.
func layout(title, bodyHTML, footer string) string {
	return `<!doctype html><html><body style="margin:0;background:#f7f3f8;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1e1030">
<table width="100%" cellpadding="0" cellspacing="0"><tr><td align="center" style="padding:32px 12px">
<table width="560" cellpadding="0" cellspacing="0" style="max-width:560px;background:#ffffff;border-radius:14px;overflow:hidden;border:1px solid #eadff0">
<tr><td style="height:6px;background:linear-gradient(90deg,#FF5CAA,#FF4E62,#6C47FF)"></td></tr>
<tr><td style="padding:28px 32px 8px"><div style="font-weight:700;font-size:13px;letter-spacing:.06em;color:#FF5CAA;text-transform:uppercase">Meili CDC</div>
<h1 style="margin:8px 0 0;font-size:20px;line-height:1.3">` + html.EscapeString(title) + `</h1></td></tr>
<tr><td style="padding:8px 32px 24px;font-size:15px;line-height:1.6">` + bodyHTML + `</td></tr>
<tr><td style="padding:16px 32px;border-top:1px solid #f0e6f3;font-size:12px;color:#7a6a85">` + footer + `</td></tr>
</table></td></tr></table></body></html>`
}

// Button renders a call to action link for emails.
func button(href, label string) string {
	return `<p style="margin:24px 0"><a href="` + html.EscapeString(href) + `" style="display:inline-block;background:#FF5CAA;color:#ffffff;text-decoration:none;font-weight:600;padding:12px 20px;border-radius:10px">` +
		html.EscapeString(label) + `</a></p><p style="font-size:13px;color:#7a6a85">Or paste this link into your browser:<br><span style="word-break:break-all">` + html.EscapeString(href) + `</span></p>`
}

// InvitationEmail renders an invitation.
func InvitationEmail(to, inviter, role, link string, expires time.Time) Email {
	body := `<p>` + HTMLEscape(inviter) + ` invited you to the Meili CDC dashboard as <b>` + HTMLEscape(role) + `</b>.</p>` +
		button(link, "Accept invitation") + `<p style="font-size:13px;color:#7a6a85">The link expires on ` + expires.UTC().Format("Jan 2, 2006 15:04 MST") + ` and works once.</p>`
	return Email{
		To:      []string{to},
		Subject: "You're invited to Meili CDC",
		Text:    fmt.Sprintf("%s invited you to the Meili CDC dashboard as %s.\n\nAccept the invitation: %s\n\nThe link expires on %s and works once.\n", inviter, role, link, expires.UTC().Format(time.RFC1123)),
		HTML:    layout("You're invited", body, "If you were not expecting this invitation, ignore this email."),
	}
}

// ResetEmail renders a password reset message.
func ResetEmail(to, link string) Email {
	body := `<p>Someone asked to reset the password of your Meili CDC account.</p>` + button(link, "Choose a new password") +
		`<p style="font-size:13px;color:#7a6a85">The link expires in one hour and works once.</p>`
	return Email{
		To:      []string{to},
		Subject: "Reset your Meili CDC password",
		Text:    fmt.Sprintf("Someone asked to reset the password of your Meili CDC account.\n\nChoose a new password: %s\n\nThe link expires in one hour. If it wasn't you, ignore this email.\n", link),
		HTML:    layout("Reset your password", body, "If you did not ask for this, ignore this email; your password stays the same."),
	}
}

// TestEmail renders the SMTP test message.
func TestEmail(to string) Email {
	return Email{
		To:      []string{to},
		Subject: "Meili CDC test email",
		Text:    "Email notifications from Meili CDC work.\n",
		HTML:    layout("Email works", "<p>Email notifications from Meili CDC are configured correctly.</p>", "Sent from the SMTP settings page."),
	}
}

// AlertEmail renders an alert notification.
func AlertEmail(to []string, subject, title, message, link, severity string) Email {
	color := map[string]string{"critical": "#FF4E62", "warning": "#E8A33D", "info": "#6C47FF", "resolved": "#18A058"}[severity]
	if color == "" {
		color = "#6C47FF"
	}
	body := `<p style="display:inline-block;padding:3px 10px;border-radius:99px;background:` + color + `;color:#fff;font-size:12px;font-weight:700;text-transform:uppercase">` + HTMLEscape(severity) + `</p>` +
		`<p style="white-space:pre-wrap">` + HTMLEscape(message) + `</p>`
	if link != "" {
		body += button(link, "Open in the dashboard")
	}
	text := message
	if link != "" {
		text += "\n\n" + link
	}
	return Email{To: to, Subject: subject, Text: text + "\n", HTML: layout(title, body, "You receive this because an alert rule lists your address.")}
}
