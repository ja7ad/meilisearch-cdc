// Package app wires the service together; main and the end-to-end tests use it.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/adminops"
	"github.com/ja7ad/meilisearch-cdc/internal/alerts"
	"github.com/ja7ad/meilisearch-cdc/internal/api"
	"github.com/ja7ad/meilisearch-cdc/internal/audit"
	"github.com/ja7ad/meilisearch-cdc/internal/auth"
	"github.com/ja7ad/meilisearch-cdc/internal/config"
	"github.com/ja7ad/meilisearch-cdc/internal/dest"
	"github.com/ja7ad/meilisearch-cdc/internal/engine"
	"github.com/ja7ad/meilisearch-cdc/internal/mcpserver"
	"github.com/ja7ad/meilisearch-cdc/internal/metrics"
	"github.com/ja7ad/meilisearch-cdc/internal/notify"
	"github.com/ja7ad/meilisearch-cdc/internal/secret"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
	"github.com/ja7ad/meilisearch-cdc/web"
)

// App is a running service instance.
type App struct {
	Config config.Config
	Engine *engine.Engine
	Auth   *auth.Service
	Notify *notify.Service
	Alerts *alerts.Manager
	Audit  *audit.Log
	MCP    *mcpserver.Service
	// Ops runs the operator commands of `meilisearch-cdc admin`; Start
	// serves them on the admin socket.
	Ops     *adminops.Ops
	Handler http.Handler
	Log     *slog.Logger
	db      *store.DB
	cancel  context.CancelFunc
	bg      chan struct{}
	admin   *http.Server
}

// NewLogger builds the logger described by the configuration.
func NewLogger(c config.Config) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.LogLevel))
	opts := &slog.HandlerOptions{Level: level}
	if c.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

// New opens the state store and builds the engine and HTTP handler.
func New(c config.Config, log *slog.Logger) (*App, error) {
	return open(c, log, 5*time.Second, true)
}

// OpenOffline opens the data directory for the operator CLI while the
// service is stopped: it gives up quickly when the store is locked
// (store.ErrLocked) and does not create the CDC_ADMIN_EMAIL admin. Call
// Close, not Start.
func OpenOffline(c config.Config, log *slog.Logger) (*App, error) {
	return open(c, log, 500*time.Millisecond, false)
}

func open(c config.Config, log *slog.Logger, lockTimeout time.Duration, bootstrap bool) (*App, error) {
	if log == nil {
		log = NewLogger(c)
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, err
	}
	key := c.SecretKey
	if key == "" {
		keyFile := filepath.Join(c.DataDir, "secret.key")
		if _, err := os.Stat(keyFile); err != nil && !bootstrap {
			// The CLI must not invent a key the service does not use.
			return nil, fmt.Errorf("no secret key: %s does not exist, so set CDC_SECRET_KEY (or -secret-key) as the service does", keyFile)
		}
		var err error
		if key, err = secret.LoadOrCreateKeyFile(keyFile); err != nil {
			return nil, err
		}
	}
	cipher, err := secret.New(key, c.SecretKeyPrevious)
	if err != nil {
		return nil, err
	}
	db, err := store.OpenTimeout(c.DBPath(), lockTimeout)
	if err != nil {
		return nil, err
	}
	dests, err := dest.New(db, cipher, log)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := dests.EnsureEnv(c.MeiliURL, c.MeiliAPIKey, c.MeiliGzip, c.MeiliTimeout); err != nil {
		db.Close()
		return nil, err
	}
	if dests.Single {
		log.Info("single instance mode: writing only to MEILI_URL", "url", c.MeiliURL)
	}
	eng, err := engine.New(engine.Config{
		Destinations: dests,
		DB:           db,
		Cipher:       cipher,
		Metrics:      metrics.New(),
		Logger:       log,
		StopTimeout:  c.StopTimeout,
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	fail := func(err error) (*App, error) { db.Close(); return nil, err }
	users, err := auth.NewService(db)
	if err != nil {
		return fail(err)
	}
	notifier, err := notify.NewService(db, cipher, c.PublicURL)
	if err != nil {
		return fail(err)
	}
	auditLog, err := audit.New(db)
	if err != nil {
		return fail(err)
	}
	alertMgr, err := alerts.New(db, observer{eng}, notifier, cipher, log)
	if err == nil {
		alertMgr.Available = func() alerts.Availability {
			return alerts.Availability{Email: notifier.EmailConfigured(), Telegram: notifier.TelegramConfigured()}
		}
	}
	if err != nil {
		return fail(err)
	}
	if c.AlertInterval > 0 {
		alertMgr.Interval = c.AlertInterval
	}
	mcpSvc, err := mcpserver.New(db, eng, alertMgr, auditLog, log)
	if err != nil {
		return fail(err)
	}
	if bootstrap {
		if err := bootstrapAdmin(users, c, log); err != nil {
			return fail(err)
		}
	}
	var ui fs.FS
	if !c.DisableUI {
		ui = web.FS
	}
	a := &App{Config: c, Engine: eng, Auth: users, Notify: notifier, Alerts: alertMgr, Audit: auditLog, MCP: mcpSvc, Log: log, db: db}
	var restoreEnv func() error
	if c.MeiliURL != "" {
		restoreEnv = func() error { return dests.EnsureEnv(c.MeiliURL, c.MeiliAPIKey, c.MeiliGzip, c.MeiliTimeout) }
	}
	a.Ops = adminops.New(adminops.Deps{Auth: users, Engine: eng, Notify: notifier, Alerts: alertMgr, Audit: auditLog,
		MCP: mcpSvc, DB: db, RestoreEnv: restoreEnv})
	a.Handler = api.New(api.Deps{
		Engine: eng, Auth: users, Notify: notifier, Alerts: alertMgr, Audit: auditLog, MCPService: mcpSvc,
		AdminToken: c.AdminToken, Web: ui, WebBuild: web.Build(), Log: log,
		CookieSecure: c.CookieSecure, TrustProxy: c.TrustProxy, Pprof: c.Pprof,
	})
	return a, nil
}

// bootstrapAdmin creates the first admin from CDC_ADMIN_EMAIL/PASSWORD.
func bootstrapAdmin(users *auth.Service, c config.Config, log *slog.Logger) error {
	has, err := users.HasUsers()
	if err != nil {
		return err
	}
	switch {
	case has:
		return nil
	case c.AdminEmail != "" && c.AdminPassword != "":
		if _, err := users.Setup(c.AdminEmail, "Administrator", c.AdminPassword); err != nil {
			return fmt.Errorf("create the admin from CDC_ADMIN_EMAIL: %w", err)
		}
		log.Info("created the first admin", "email", c.AdminEmail)
	default:
		log.Warn("no user exists yet: open the dashboard to create the first admin (or set CDC_ADMIN_EMAIL and CDC_ADMIN_PASSWORD)")
	}
	return nil
}

// Start resumes tasks and applies the declarative tasks file.
func (a *App) Start(ctx context.Context) error {
	if err := a.Engine.Start(ctx); err != nil {
		return err
	}
	bg, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a.cancel, a.bg = cancel, make(chan struct{})
	go func() {
		defer close(a.bg)
		cleanup := time.NewTicker(time.Hour)
		defer cleanup.Stop()
		done := make(chan struct{})
		go func() { a.Alerts.Run(bg); close(done) }()
		monitored := make(chan struct{})
		go func() { a.Engine.MonitorDestinations(bg); close(monitored) }()
		for {
			select {
			case <-bg.Done():
				<-done
				<-monitored
				return
			case <-cleanup.C:
				a.Auth.Cleanup()
			}
		}
	}()
	a.serveAdmin()
	if a.Config.TasksFile != "" {
		tasks, err := config.LoadTasks(a.Config.TasksFile)
		if err != nil {
			return err
		}
		for _, t := range tasks {
			if err := a.Engine.Declare(ctx, t); err != nil {
				return fmt.Errorf("task %s: %w", t.ID, err)
			}
		}
		a.Log.Info("applied tasks file", "path", a.Config.TasksFile, "tasks", len(tasks))
	}
	return nil
}

// SocketPath is where the service answers `meilisearch-cdc admin`.
func SocketPath(c config.Config) string { return adminops.SocketPath(c.DataDir) }

// serveAdmin answers operator commands on the admin socket. Without it the
// CLI cannot reach a running service, but the service works, so a failure
// is only logged (a data directory path too long for a unix socket, say).
func (a *App) serveAdmin() {
	path := SocketPath(a.Config)
	l, err := adminops.Listen(path)
	if err != nil {
		a.Log.Warn("admin socket unavailable: `meilisearch-cdc admin` works only while the service is stopped", "path", path, "err", err)
		return
	}
	a.admin = &http.Server{Handler: a.Ops.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := a.admin.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.Log.Warn("admin socket stopped", "err", err)
		}
	}()
}

// Close stops every task and closes the store.
func (a *App) Close(ctx context.Context) error {
	if a.admin != nil {
		// Waits for a running command, e.g. a purge, to finish.
		_ = a.admin.Shutdown(ctx)
		_ = os.Remove(SocketPath(a.Config))
	}
	if a.cancel != nil {
		a.cancel()
		<-a.bg
	}
	a.Engine.Shutdown(ctx)
	return a.db.Close()
}

// Run serves HTTP until ctx is cancelled, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	if err := a.Start(ctx); err != nil {
		return err
	}
	srv := &http.Server{Addr: a.Config.Listen, Handler: a.Handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		a.Log.Info("listening", "addr", a.Config.Listen, "meilisearch", a.Config.MeiliURL)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), a.Config.StopTimeout+10*time.Second)
	defer cancel()
	a.Log.Info("shutting down")
	_ = srv.Shutdown(sctx)
	return a.Close(sctx)
}

// observer adapts the engine to what alert rules watch.
type observer struct{ *engine.Engine }

func (o observer) Destinations(ctx context.Context) []alerts.DestState {
	health := o.DestinationHealth(ctx)
	out := make([]alerts.DestState, len(health))
	for i, h := range health {
		out[i] = alerts.DestState{ID: h.ID, Name: h.Name, Healthy: h.Healthy, Error: h.Error}
	}
	return out
}
