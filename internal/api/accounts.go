package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/alerts"
	"github.com/ja7ad/meilisearch-cdc/internal/audit"
	"github.com/ja7ad/meilisearch-cdc/internal/auth"
	"github.com/ja7ad/meilisearch-cdc/internal/notify"
	"github.com/ja7ad/meilisearch-cdc/internal/version"
)

type credentials struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

type userResp struct {
	User auth.PublicUser `json:"user"`
}

// authErr maps auth errors to HTTP errors.
func authErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrInvalidCredentials):
		return &HTTPError{Code: http.StatusUnauthorized, Msg: err.Error()}
	case errors.Is(err, auth.ErrInvalidToken):
		return &HTTPError{Code: http.StatusBadRequest, Msg: err.Error()}
	case errors.Is(err, auth.ErrForbidden):
		return &HTTPError{Code: http.StatusForbidden, Msg: err.Error()}
	case errors.Is(err, auth.ErrConflict):
		return &HTTPError{Code: http.StatusConflict, Msg: err.Error()}
	case errors.Is(err, auth.ErrNotFound):
		return &HTTPError{Code: http.StatusNotFound, Msg: err.Error()}
	case errors.Is(err, auth.ErrWeakPassword), strings.Contains(err.Error(), "invalid email"), strings.Contains(err.Error(), "unknown role"):
		return &HTTPError{Code: http.StatusUnprocessableEntity, Msg: err.Error()}
	}
	return err
}

// origin is the public base URL for links in emails.
func (s *Server) origin(r *http.Request) string {
	scheme := "http"
	if s.secure(r) {
		scheme = "https"
	}
	return s.Notify.PublicURL(scheme + "://" + r.Host)
}

// login starts a session. Failures are rate limited per IP and per account.
func (s *Server) login(w http.ResponseWriter, r *http.Request, u auth.User) {
	token, _, err := s.Auth.CreateSession(u.ID, s.clientIP(r), r.UserAgent())
	if err != nil {
		writeErr(w, err)
		return
	}
	s.setSession(w, r, token)
	writeJSON(w, http.StatusOK, userResp{User: u.Public()})
}

func (s *Server) tooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many attempts, try again later", "retryAfter": int(wait.Seconds()) + 1})
}

func decode[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	if err := decodeJSON(w, r, &v); err != nil {
		writeErr(w, badRequest("invalid JSON body: %v", err))
		return v, false
	}
	return v, true
}

func (s *Server) accountRoutes(h route, _ *http.ServeMux) {
	h("GET /api/v1/auth/config", handler(http.StatusOK, func(r *http.Request, _ none) (map[string]any, error) {
		has, err := s.Auth.HasUsers()
		if err != nil {
			return nil, err
		}
		out := map[string]any{"setupRequired": !has, "smtpConfigured": s.Notify.EmailConfigured(), "passwordLogin": true, "version": version.Version}
		// Deployment details only for signed in users.
		if auth.FromContext(r.Context()) != nil {
			dests, _ := s.eng.Destinations().List()
			conns, _ := s.eng.Connectors()
			tasks, _ := s.eng.List("", "", "")
			out["singleDestination"] = s.eng.Destinations().Single
			out["telegramConfigured"] = s.Notify.TelegramConfigured()
			out["onboarding"] = map[string]any{"destinations": len(dests), "connectors": len(conns), "tasks": len(tasks),
				"complete": len(dests) > 0 && len(tasks) > 0}
		}
		return out, nil
	}))
	h("POST /api/v1/auth/setup", func(w http.ResponseWriter, r *http.Request) {
		c, ok := decode[credentials](w, r)
		if !ok {
			return
		}
		u, err := s.Auth.Setup(c.Email, c.Name, c.Password)
		if err != nil {
			writeErr(w, authErr(err))
			return
		}
		s.Audit.Record(audit.Entry{UserID: u.ID, UserEmail: u.Email, Action: "auth.setup", Target: u.Email, IP: s.clientIP(r)})
		s.login(w, r, u)
	})
	h("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		c, ok := decode[credentials](w, r)
		if !ok {
			return
		}
		ip := s.clientIP(r)
		acct := strings.ToLower(strings.TrimSpace(c.Email))
		if blocked, wait := s.ipLimit.Blocked(ip); blocked {
			s.tooMany(w, wait)
			return
		}
		if blocked, wait := s.idLimit.Blocked(acct); blocked {
			s.tooMany(w, wait)
			return
		}
		u, err := s.Auth.Authenticate(acct, c.Password)
		if err != nil {
			s.ipLimit.Fail(ip)
			s.idLimit.Fail(acct)
			s.Audit.Record(audit.Entry{UserEmail: acct, Action: "auth.login_failed", IP: ip})
			writeErr(w, authErr(err))
			return
		}
		s.idLimit.Reset(acct)
		s.Audit.Record(audit.Entry{UserID: u.ID, UserEmail: u.Email, Action: "auth.login", IP: ip})
		s.login(w, r, u)
	})
	h("POST /api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			_ = s.Auth.RevokeSession(c.Value)
		}
		s.clearSession(w, r)
		w.WriteHeader(http.StatusNoContent)
	})
	h("POST /api/v1/auth/forgot", func(w http.ResponseWriter, r *http.Request) {
		c, ok := decode[credentials](w, r)
		if !ok {
			return
		}
		if !s.Notify.EmailConfigured() {
			writeErr(w, &HTTPError{Code: http.StatusConflict, Msg: "email is not configured; ask an administrator to reset your password"})
			return
		}
		ip := s.clientIP(r)
		if blocked, wait := s.ipLimit.Blocked(ip); blocked {
			s.tooMany(w, wait)
			return
		}
		s.ipLimit.Fail(ip) // every request counts, so the endpoint cannot be used to spam
		u, token, err := s.Auth.CreateReset(c.Email)
		if err == nil && token != "" {
			link := s.origin(r) + "/#/reset/" + token
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				if err := s.Notify.Mail(ctx, notify.ResetEmail(u.Email, link)); err != nil {
					s.log.Warn("send password reset email", "err", err)
				}
			}()
			s.Audit.Record(audit.Entry{UserID: u.ID, UserEmail: u.Email, Action: "auth.reset_requested", IP: ip})
		}
		// The same answer whether or not the account exists.
		writeJSON(w, http.StatusAccepted, map[string]any{})
	})
	h("POST /api/v1/auth/reset", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}](w, r)
		if !ok {
			return
		}
		if err := s.Auth.ResetPassword(req.Token, req.Password); err != nil {
			writeErr(w, authErr(err))
			return
		}
		s.Audit.Record(audit.Entry{Action: "auth.password_reset", IP: s.clientIP(r)})
		w.WriteHeader(http.StatusNoContent)
	})
	h("GET /api/v1/auth/invite/{token}", handler(http.StatusOK, func(r *http.Request, _ none) (map[string]any, error) {
		inv, err := s.Auth.Invitation(r.PathValue("token"))
		if err != nil {
			return nil, &HTTPError{Code: http.StatusNotFound, Msg: err.Error()}
		}
		return map[string]any{"email": inv.Email, "role": inv.Role, "expiresAt": inv.ExpiresAt}, nil
	}))
	h("POST /api/v1/auth/invite/accept", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[struct {
			Token    string `json:"token"`
			Name     string `json:"name"`
			Password string `json:"password"`
		}](w, r)
		if !ok {
			return
		}
		u, err := s.Auth.AcceptInvitation(req.Token, req.Name, req.Password)
		if err != nil {
			writeErr(w, authErr(err))
			return
		}
		s.Audit.Record(audit.Entry{UserID: u.ID, UserEmail: u.Email, Action: "auth.invitation_accepted", Target: string(u.Role), IP: s.clientIP(r)})
		s.login(w, r, u)
	})

	// Current user.
	h("GET /api/v1/auth/me", handler(http.StatusOK, func(r *http.Request, _ none) (userResp, error) {
		p := auth.FromContext(r.Context())
		if p.Kind != "session" {
			return userResp{User: auth.PublicUser{ID: p.ID, Name: p.Name, Role: p.Role}}, nil
		}
		u, err := s.Auth.User(p.ID)
		return userResp{User: u.Public()}, err
	}))
	h("PATCH /api/v1/auth/me", handler(http.StatusOK, func(r *http.Request, req struct {
		Name string `json:"name"`
	}) (userResp, error) {
		p := auth.FromContext(r.Context())
		if p.Kind != "session" {
			return userResp{}, badRequest("only user accounts have a profile")
		}
		u, err := s.Auth.UpdateUser(p.ID, p.ID, &req.Name, nil)
		return userResp{User: u.Public()}, authErr(err)
	}))
	h("POST /api/v1/auth/me/password", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[struct {
			Current string `json:"currentPassword"`
			New     string `json:"newPassword"`
		}](w, r)
		if !ok {
			return
		}
		p := auth.FromContext(r.Context())
		if p.Kind != "session" {
			writeErr(w, badRequest("only user accounts have a password"))
			return
		}
		if blocked, wait := s.idLimit.Blocked("pw:" + p.ID); blocked {
			s.tooMany(w, wait)
			return
		}
		if err := s.Auth.ChangePassword(p.ID, req.Current, req.New, p.SessionID); err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				s.idLimit.Fail("pw:" + p.ID)
				err = &HTTPError{Code: http.StatusUnprocessableEntity, Msg: "the current password is wrong"}
			}
			writeErr(w, authErr(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Users (admin).
	h("GET /api/v1/users", handler(http.StatusOK, func(r *http.Request, _ none) ([]auth.PublicUser, error) {
		return s.Auth.Users()
	}))
	h("POST /api/v1/users", handler(http.StatusCreated, func(r *http.Request, req struct {
		credentials
		Role auth.Role `json:"role"`
	}) (auth.PublicUser, error) {
		u, err := s.Auth.CreateUser(req.Email, req.Name, req.Role, req.Password)
		return u.Public(), authErr(err)
	}))
	h("PATCH /api/v1/users/{id}", handler(http.StatusOK, func(r *http.Request, req struct {
		Name *string    `json:"name"`
		Role *auth.Role `json:"role"`
	}) (auth.PublicUser, error) {
		u, err := s.Auth.UpdateUser(auth.FromContext(r.Context()).ID, r.PathValue("id"), req.Name, req.Role)
		return u.Public(), authErr(err)
	}))
	h("POST /api/v1/users/{id}/password", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[struct {
			Password string `json:"password"`
		}](w, r)
		if !ok {
			return
		}
		keep := ""
		p := auth.FromContext(r.Context())
		if p.ID == r.PathValue("id") {
			keep = p.SessionID
		}
		if err := s.Auth.SetUserPassword(p.ID, r.PathValue("id"), req.Password, keep); err != nil {
			writeErr(w, authErr(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h("DELETE /api/v1/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Auth.DeleteUser(r.PathValue("id"), auth.FromContext(r.Context()).ID); err != nil {
			writeErr(w, authErr(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Invitations (admin).
	h("GET /api/v1/invitations", handler(http.StatusOK, func(r *http.Request, _ none) ([]auth.Invitation, error) {
		return s.Auth.Invitations()
	}))
	h("POST /api/v1/invitations", handler(http.StatusCreated, func(r *http.Request, req struct {
		Email string    `json:"email"`
		Role  auth.Role `json:"role"`
	}) (map[string]any, error) {
		p := auth.FromContext(r.Context())
		inv, token, err := s.Auth.CreateInvitation(req.Email, req.Role, p.Label())
		if err != nil {
			return nil, authErr(err)
		}
		link := s.origin(r) + "/#/invite/" + token
		out := map[string]any{"invitation": inv, "link": link, "emailed": false}
		if s.Notify.EmailConfigured() {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			inviter := p.Name
			if inviter == "" {
				inviter = p.Label()
			}
			if err := s.Notify.Mail(ctx, notify.InvitationEmail(inv.Email, inviter, string(inv.Role), link, inv.ExpiresAt)); err != nil {
				out["emailError"] = err.Error()
			} else {
				s.Auth.MarkInvitationEmailed(inv.ID)
				inv.Emailed = true
				out["invitation"], out["emailed"] = inv, true
			}
		}
		return out, nil
	}))
	h("DELETE /api/v1/invitations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Auth.RevokeInvitation(r.PathValue("id")); err != nil {
			writeErr(w, authErr(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// SMTP (admin).
	smtpView := func(c notify.SMTPConfig) map[string]any {
		pw := ""
		if c.Password != "" {
			pw = notify.Masked
		}
		if c.Port == 0 {
			c.Port = 587
		}
		if c.TLS == "" {
			c.TLS = notify.TLSStartTLS
		}
		return map[string]any{"host": c.Host, "port": c.Port, "username": c.Username, "password": pw, "from": c.From,
			"tls": c.TLS, "publicUrl": c.PublicURL, "configured": c.Configured()}
	}
	h("GET /api/v1/settings/smtp", handler(http.StatusOK, func(r *http.Request, _ none) (map[string]any, error) {
		return smtpView(s.Notify.SMTP()), nil
	}))
	h("PUT /api/v1/settings/smtp", handler(http.StatusOK, func(r *http.Request, c notify.SMTPConfig) (map[string]any, error) {
		saved, err := s.Notify.SetSMTP(c)
		if err != nil {
			return nil, &HTTPError{Code: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		return smtpView(saved), nil
	}))
	h("POST /api/v1/settings/smtp/test", handler(http.StatusOK, func(r *http.Request, req struct {
		To string `json:"to"`
	}) (map[string]any, error) {
		to := strings.TrimSpace(req.To)
		if to == "" {
			to = auth.FromContext(r.Context()).Email
		}
		if err := s.Notify.Mail(r.Context(), notify.TestEmail(to)); err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		return map[string]any{"ok": true}, nil
	}))

	// Telegram (admin).
	tgView := func(c notify.TelegramConfig) map[string]any {
		tok := ""
		if c.BotToken != "" {
			tok = notify.Masked
		}
		api := c.APIURL
		if api == "" {
			api = notify.DefaultTelegramAPI
		}
		return map[string]any{"botToken": tok, "apiUrl": api, "configured": c.Configured(), "botName": c.BotName}
	}
	h("GET /api/v1/settings/telegram", handler(http.StatusOK, func(r *http.Request, _ none) (map[string]any, error) {
		return tgView(s.Notify.Telegram()), nil
	}))
	h("PUT /api/v1/settings/telegram", handler(http.StatusOK, func(r *http.Request, c notify.TelegramConfig) (map[string]any, error) {
		saved, err := s.Notify.SetTelegram(r.Context(), c)
		if err != nil {
			return nil, &HTTPError{Code: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		return tgView(saved), nil
	}))
	h("POST /api/v1/settings/telegram/test", handler(http.StatusOK, func(r *http.Request, req struct {
		ChatID string `json:"chatId"`
	}) (map[string]any, error) {
		if strings.TrimSpace(req.ChatID) == "" {
			return nil, badRequest("chatId is required")
		}
		err := s.Notify.SendTelegram(r.Context(), req.ChatID, "✅ <b>Meili CDC</b>: Telegram notifications work.")
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		return map[string]any{"ok": true}, nil
	}))

	// Audit (admin).
	h("GET /api/v1/audit", handler(http.StatusOK, func(r *http.Request, _ none) ([]audit.Entry, error) {
		q := r.URL.Query()
		return s.Audit.List(intParam(r, "limit", 200), q.Get("action"), q.Get("user"))
	}))
}

func alertErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, alerts.ErrNotFound):
		return &HTTPError{Code: http.StatusNotFound, Msg: err.Error()}
	case errors.Is(err, alerts.ErrNoChannel):
		return &HTTPError{Code: http.StatusConflict, Msg: err.Error()}
	}
	return &HTTPError{Code: http.StatusUnprocessableEntity, Msg: err.Error(), Details: strings.Split(err.Error(), "\n")}
}

func (s *Server) alertRoutes(h route) {
	h("GET /api/v1/alerts/channels", handler(http.StatusOK, func(r *http.Request, _ none) (alerts.Availability, error) {
		return s.Alerts.Channels(), nil
	}))
	h("GET /api/v1/alerts/rules", handler(http.StatusOK, func(r *http.Request, _ none) ([]alerts.Rule, error) {
		rules, err := s.Alerts.Rules()
		for i := range rules {
			rules[i] = rules[i].Redacted()
		}
		return rules, err
	}))
	h("POST /api/v1/alerts/rules", handler(http.StatusCreated, func(r *http.Request, rule alerts.Rule) (alerts.Rule, error) {
		out, err := s.Alerts.Create(rule)
		return out.Redacted(), alertErr(err)
	}))
	h("PUT /api/v1/alerts/rules/{id}", handler(http.StatusOK, func(r *http.Request, rule alerts.Rule) (alerts.Rule, error) {
		out, err := s.Alerts.Update(r.PathValue("id"), rule)
		return out.Redacted(), alertErr(err)
	}))
	h("DELETE /api/v1/alerts/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Alerts.Delete(r.PathValue("id")); err != nil {
			writeErr(w, alertErr(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h("POST /api/v1/alerts/rules/{id}/test", handler(http.StatusOK, func(r *http.Request, _ none) (map[string]any, error) {
		res, err := s.Alerts.Test(r.Context(), r.PathValue("id"))
		if err != nil {
			return nil, alertErr(err)
		}
		if len(res) == 0 {
			return nil, &HTTPError{Code: http.StatusUnprocessableEntity, Msg: "the rule has no notification channel"}
		}
		return map[string]any{"results": res}, nil
	}))
	h("GET /api/v1/alerts/events", handler(http.StatusOK, func(r *http.Request, _ none) ([]alerts.Event, error) {
		q := r.URL.Query()
		return s.Alerts.Events(intParam(r, "limit", 100), q.Get("ruleId"), alerts.EventState(q.Get("state")))
	}))
	h("GET /api/v1/alerts/active", handler(http.StatusOK, func(r *http.Request, _ none) ([]alerts.Event, error) {
		return s.Alerts.Active()
	}))
}
