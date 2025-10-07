// Package notify sends email (SMTP), Telegram and webhook notifications
// and stores their configuration.
package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// TLSMode selects how the SMTP connection is secured.
type TLSMode string

const (
	// TLSAuto picks the mode from the port: implicit TLS on 465, STARTTLS
	// otherwise (plain text only when the server lacks STARTTLS and no
	// credentials would be sent).
	TLSAuto     TLSMode = "auto"
	TLSStartTLS TLSMode = "starttls" // upgrade a plain connection (port 587)
	TLSImplicit TLSMode = "tls"      // TLS from the first byte (port 465)
	TLSNone     TLSMode = "none"     // plain text; only for local relays
)

// SMTPConfig configures outgoing email.
type SMTPConfig struct {
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Username string  `json:"username"`
	Password string  `json:"password"`
	From     string  `json:"from"`
	TLS      TLSMode `json:"tls"`
	// PublicURL is the dashboard base URL used in emailed links.
	PublicURL string `json:"publicUrl"`
}

// Configured reports whether email can be sent.
func (c SMTPConfig) Configured() bool { return c.Host != "" && c.From != "" }

// Validate checks the configuration.
func (c *SMTPConfig) Validate() error {
	if c.Host == "" {
		return nil // disabled
	}
	if c.Port <= 0 || c.Port > 65535 {
		return errors.New("smtp port must be between 1 and 65535")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return fmt.Errorf("invalid from address: %w", err)
	}
	switch c.TLS {
	case "":
		c.TLS = TLSAuto
	case TLSAuto, TLSStartTLS, TLSImplicit, TLSNone:
	default:
		return fmt.Errorf("unknown tls mode %q", c.TLS)
	}
	return nil
}

// mode returns the effective TLS mode. A mode that cannot work on a
// well-known port is corrected: port 465 only speaks TLS from the first
// byte, and ports 25 and 587 never do.
func (c SMTPConfig) mode() TLSMode {
	switch {
	case c.TLS == TLSNone:
		return TLSNone
	case c.Port == 465:
		return TLSImplicit
	case c.TLS == TLSImplicit && c.Port != 25 && c.Port != 587:
		return TLSImplicit
	case c.TLS == TLSAuto || c.TLS == "":
		return TLSAuto
	}
	return TLSStartTLS
}

// Email is a message to send.
type Email struct {
	To      []string
	Subject string
	Text    string
	HTML    string
}

// SendMail delivers e with cfg. It honours ctx for the whole exchange.
func SendMail(ctx context.Context, cfg SMTPConfig, e Email) error {
	if !cfg.Configured() {
		return errors.New("email is not configured")
	}
	if len(e.To) == 0 {
		return errors.New("no recipients")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return fmt.Errorf("invalid from address: %w", err)
	}
	for _, to := range e.To {
		if _, err := mail.ParseAddress(to); err != nil {
			return fmt.Errorf("invalid recipient %q", to)
		}
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	dialer := &net.Dialer{Deadline: deadline}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	mode := cfg.mode()
	var conn net.Conn
	if mode == TLSImplicit {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp handshake with %s: %w%s", addr, err, handshakeHint(mode, err, ctx.Err() != nil))
	}
	defer c.Close()
	if mode == TLSStartTLS || mode == TLSAuto {
		ok, _ := c.Extension("STARTTLS")
		switch {
		case ok:
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		case mode == TLSStartTLS:
			return errors.New("the server does not support STARTTLS; choose TLS (port 465) or None")
		case cfg.Username != "":
			return errors.New("the server does not support STARTTLS, so the password would be sent in plain text; use port 465 with TLS")
		}
	}
	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			// PlainAuth refuses to send credentials over plain text except to localhost.
			if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, to := range e.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("smtp RCPT TO %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(buildMessage(from, e)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	return c.Quit()
}

// handshakeHint explains the usual cause of a failed SMTP greeting.
// expired is set when the context ended, which closes the connection
// before the read deadline reports a timeout.
func handshakeHint(mode TLSMode, err error, expired bool) string {
	var ne net.Error
	timeout := expired || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
	switch {
	case timeout && mode != TLSImplicit:
		return " (the server sent no greeting: it probably expects TLS from the start; use port 465 with TLS, or check the port)"
	case mode == TLSImplicit && strings.Contains(err.Error(), "first record does not look like a TLS handshake"):
		return " (the server does not speak TLS on this port; use STARTTLS on port 587)"
	}
	return ""
}

func randomBoundary() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// buildMessage renders a multipart/alternative message.
func buildMessage(from *mail.Address, e Email) []byte {
	var b bytes.Buffer
	host := "meilisearch-cdc"
	if i := strings.LastIndex(from.Address, "@"); i >= 0 {
		host = from.Address[i+1:]
	}
	boundary := randomBoundary()
	hdr := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	hdr("From", from.String())
	hdr("To", strings.Join(e.To, ", "))
	hdr("Subject", mime.QEncoding.Encode("utf-8", e.Subject))
	hdr("Date", time.Now().Format(time.RFC1123Z))
	hdr("Message-ID", fmt.Sprintf("<%s@%s>", randomBoundary(), host))
	hdr("MIME-Version", "1.0")
	hdr("Auto-Submitted", "auto-generated")
	hdr("Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", boundary))
	b.WriteString("\r\n")
	part := func(ctype, body string) {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", boundary, ctype)
		qp := quotedprintable.NewWriter(&b)
		_, _ = qp.Write([]byte(body))
		_ = qp.Close()
		b.WriteString("\r\n")
	}
	part("text/plain", e.Text)
	if e.HTML != "" {
		part("text/html", e.HTML)
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes()
}
