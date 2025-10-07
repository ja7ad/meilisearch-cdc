package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/secret"
	"github.com/ja7ad/meilisearch-cdc/internal/signature"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// smtpSink is a minimal SMTP server capturing messages (no TLS, no auth).
type smtpSink struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []string
	rcpt [][]string
}

func newSMTPSink(t *testing.T) *smtpSink {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &smtpSink{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *smtpSink) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(line string) { _, _ = io.WriteString(c, line+"\r\n") }
	w("220 sink ready")
	var rcpt []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			w("250 sink")
		case strings.HasPrefix(cmd, "MAIL"):
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT"):
			rcpt = append(rcpt, strings.Trim(strings.TrimPrefix(strings.TrimSpace(line)[8:], ":"), "<> "))
			w("250 ok")
		case cmd == "DATA":
			w("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.msgs = append(s.msgs, b.String())
			s.rcpt = append(s.rcpt, rcpt)
			s.mu.Unlock()
			rcpt = nil
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func (s *smtpSink) cfg() SMTPConfig {
	_, port, _ := net.SplitHostPort(s.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return SMTPConfig{Host: "127.0.0.1", Port: p, From: "Meili CDC <cdc@example.com>", TLS: TLSNone}
}

func TestSendMail(t *testing.T) {
	sink := newSMTPSink(t)
	e := InvitationEmail("new@example.com", "Ada", "moderator", "https://cdc.example.com/#/invite/abc", time.Now().Add(time.Hour))
	require.NoError(t, SendMail(context.Background(), sink.cfg(), e))
	require.Len(t, sink.msgs, 1)
	msg := sink.msgs[0]
	assert.Contains(t, msg, "Subject: You're invited to Meili CDC")
	assert.Contains(t, msg, "multipart/alternative")
	assert.Contains(t, msg, "https://cdc.example.com/#/invite/abc")
	assert.Equal(t, []string{"new@example.com"}, sink.rcpt[0])

	assert.Error(t, SendMail(context.Background(), SMTPConfig{}, e), "not configured")
	bad := e
	bad.To = []string{"not an address"}
	assert.Error(t, SendMail(context.Background(), sink.cfg(), bad))
	cfg := sink.cfg()
	cfg.TLS = TLSStartTLS
	assert.ErrorContains(t, SendMail(context.Background(), cfg, e), "STARTTLS")

	// Auto falls back to plain text only when no password would be sent.
	cfg.TLS = TLSAuto
	require.NoError(t, SendMail(context.Background(), cfg, e))
	cfg.Username, cfg.Password = "u", "p"
	assert.ErrorContains(t, SendMail(context.Background(), cfg, e), "plain text")
}

func TestSMTPEffectiveMode(t *testing.T) {
	for _, c := range []struct {
		port int
		set  TLSMode
		want TLSMode
	}{
		{465, TLSAuto, TLSImplicit},
		{465, TLSStartTLS, TLSImplicit}, // STARTTLS cannot work on 465
		{587, TLSAuto, TLSAuto},
		{587, TLSImplicit, TLSStartTLS}, // nor implicit TLS on 587
		{25, TLSImplicit, TLSStartTLS},
		{2465, TLSImplicit, TLSImplicit},
		{2525, TLSStartTLS, TLSStartTLS},
		{465, TLSNone, TLSNone},
		{587, "", TLSAuto},
	} {
		assert.Equal(t, c.want, SMTPConfig{Port: c.port, TLS: c.set}.mode(), "%d %s", c.port, c.set)
	}
}

func TestSMTPGreetingTimeoutHint(t *testing.T) {
	// A server that accepts the connection but never greets, like an
	// implicit-TLS port reached in plain text.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	cfg := SMTPConfig{Host: "127.0.0.1", Port: p, From: "cdc@example.com", TLS: TLSStartTLS}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = SendMail(ctx, cfg, Email{To: []string{"a@example.com"}, Subject: "x", Text: "x"})
	assert.ErrorContains(t, err, "expects TLS from the start")
}

func TestSMTPValidate(t *testing.T) {
	c := SMTPConfig{Host: "smtp.example.com", Port: 587, From: "cdc@example.com"}
	require.NoError(t, c.Validate())
	assert.Equal(t, TLSAuto, c.TLS, "auto by default")
	c.Port = 0
	assert.Error(t, c.Validate())
	c = SMTPConfig{Host: "x", Port: 25, From: "nope"}
	assert.Error(t, c.Validate())
	assert.NoError(t, (&SMTPConfig{}).Validate(), "empty host disables email")
}

func TestServiceStoresEncryptedSettings(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "n.db"))
	require.NoError(t, err)
	defer db.Close()
	c, _ := secret.New("k")
	s, err := NewService(db, c, "https://default.example.com/")
	require.NoError(t, err)
	assert.False(t, s.EmailConfigured())
	assert.Equal(t, "https://default.example.com", s.PublicURL("http://req"))

	sink := newSMTPSink(t)
	cfg := sink.cfg()
	cfg.Password = "hunter2"
	cfg.PublicURL = "https://cdc.example.com/"
	_, err = s.SetSMTP(cfg)
	require.NoError(t, err)
	assert.True(t, s.EmailConfigured())
	assert.Equal(t, "https://cdc.example.com", s.PublicURL("http://req"))

	// The masked password keeps the stored one; it is encrypted at rest.
	cfg.Password = Masked
	saved, err := s.SetSMTP(cfg)
	require.NoError(t, err)
	assert.Equal(t, "hunter2", saved.Password)
	raw, _ := store.NewBucket[map[string]any](db, "settings_smtp")
	stored, _ := raw.MustGet("config")
	assert.True(t, strings.HasPrefix(stored["password"].(string), "enc:v2:"))

	cfg.PublicURL = "ftp://nope"
	_, err = s.SetSMTP(cfg)
	assert.Error(t, err)

	require.NoError(t, s.Mail(context.Background(), TestEmail("ops@example.com")))
	assert.Len(t, sink.msgs, 1)
}

func TestTelegram(t *testing.T) {
	var sent atomic.Int32
	var flood atomic.Bool
	flood.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/botgood/getMe"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"username":"cdc_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/botgood/sendMessage"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if flood.CompareAndSwap(true, false) {
				_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":1}}`)
				return
			}
			if body["chat_id"] == "missing" {
				_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
				return
			}
			assert.Equal(t, "HTML", body["parse_mode"])
			sent.Add(1)
			_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
		default:
			_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		}
	}))
	defer srv.Close()

	db, _ := store.Open(filepath.Join(t.TempDir(), "n.db"))
	defer db.Close()
	s, _ := NewService(db, nil, "")
	_, err := s.SetTelegram(context.Background(), TelegramConfig{BotToken: "bad", APIURL: srv.URL})
	assert.ErrorContains(t, err, "Unauthorized")
	cfg, err := s.SetTelegram(context.Background(), TelegramConfig{BotToken: "good", APIURL: srv.URL})
	require.NoError(t, err)
	assert.Equal(t, "cdc_bot", cfg.BotName)

	start := time.Now()
	require.NoError(t, s.SendTelegram(context.Background(), "-100123", "<b>hi</b>"))
	assert.GreaterOrEqual(t, time.Since(start), time.Second, "honours retry_after")
	assert.Equal(t, int32(1), sent.Load())
	err = s.SendTelegram(context.Background(), "missing", "x")
	assert.ErrorContains(t, err, "chat not found")
	assert.NotContains(t, err.Error(), "good", "the token never leaks into errors")
}

func TestWebhook(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(502)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "hello", body["text"])
		w.WriteHeader(204)
	}))
	defer srv.Close()
	s := &Service{}
	require.NoError(t, s.Webhook(context.Background(), srv.URL, "", map[string]any{"text": "hello"}))
	assert.Equal(t, int32(2), calls.Load(), "retried after a 502")
	assert.Error(t, s.Webhook(context.Background(), "file:///etc/passwd", "", nil))
}

func TestWebhookSigned(t *testing.T) {
	const secret = "whsec_test_secret_value"
	var verified, unsigned atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get(signature.HeaderSignature) == "" {
			unsigned.Add(1)
		} else if signature.Verify(r.Header, secret, time.Now(), body) == nil {
			verified.Add(1)
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	s := &Service{}
	require.NoError(t, s.Webhook(context.Background(), srv.URL, secret, map[string]any{"text": "signed"}))
	require.NoError(t, s.Webhook(context.Background(), srv.URL, "", map[string]any{"text": "plain"}))
	assert.Equal(t, int32(1), verified.Load())
	assert.Equal(t, int32(1), unsigned.Load())
}
