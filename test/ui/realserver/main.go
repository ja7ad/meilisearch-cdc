// Command realserver runs the real service for the dashboard end-to-end
// tests: a Meilisearch container, a SQLite source table and the app itself.
// It also serves /__test/* helpers so tests can change the source data.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	_ "modernc.org/sqlite"

	"github.com/ja7ad/meilisearch-cdc/internal/app"
	"github.com/ja7ad/meilisearch-cdc/internal/config"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/all"
)

const (
	masterKey     = "ui-master-key"
	token         = "ui-token"
	adminEmail    = "admin@example.com"
	adminPassword = "ui-admin-password"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "realserver:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8791"
	}

	// CDC_UI_MULTI=1 leaves MEILI_URL unset (destinations are managed in the
	// dashboard) and starts a second instance to add as another destination.
	multi := os.Getenv("CDC_UI_MULTI") != ""
	instances := 1
	if multi {
		instances = 2
	}
	var meiliURLs []string
	for i := range instances {
		meili, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        "getmeili/meilisearch:latest",
				ExposedPorts: []string{"7700/tcp"},
				Env:          map[string]string{"MEILI_MASTER_KEY": masterKey, "MEILI_NO_ANALYTICS": "true"},
				WaitingFor:   wait.ForHTTP("/health").WithPort("7700/tcp").WithStartupTimeout(2 * time.Minute),
			},
			Started: true,
		})
		if err != nil {
			return fmt.Errorf("meilisearch %d: %w", i+1, err)
		}
		defer meili.Terminate(context.Background())
		u, err := meili.PortEndpoint(ctx, "7700/tcp", "http")
		if err != nil {
			return err
		}
		meiliURLs = append(meiliURLs, u)
	}
	meiliURL := meiliURLs[0]
	if multi {
		meiliURL = ""
	}

	dir, err := os.MkdirTemp("", "cdc-ui")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "shop.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := seed(db); err != nil {
		return err
	}

	cfg := config.Config{
		DataDir: filepath.Join(dir, "state"), MeiliURL: meiliURL, MeiliAPIKey: masterKey, MeiliGzip: true,
		MeiliTimeout: 30 * time.Second, AdminToken: token,
		AdminEmail: adminEmail, AdminPassword: adminPassword, SecretKey: "ui-secret", LogLevel: "warn", StopTimeout: 5 * time.Second,
	}
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		return err
	}
	if err := a.Start(ctx); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/", a.Handler)
	mux.HandleFunc("GET /__test/info", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sqlite": dbPath, "token": token, "email": adminEmail, "password": adminPassword,
			"meilisearch": meiliURLs, "masterKey": masterKey,
		})
	})
	mux.HandleFunc("POST /__test/sql", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SQL string `json:"sql"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if _, err := db.Exec(req.SQL); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = a.Close(sctx)
	}()
	fmt.Printf("realserver ready on http://127.0.0.1:%s\n", port)
	for i, u := range meiliURLs {
		fmt.Printf("  meilisearch %d: %s (master key %q)\n", i+1, u, masterKey)
	}
	if multi {
		fmt.Println("  multi-instance mode: add the instances as destinations in the dashboard")
	}
	fmt.Printf("  sqlite source: %s\n", dbPath)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func seed(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE products (id INTEGER PRIMARY KEY, name TEXT NOT NULL, category TEXT, price REAL, version INTEGER NOT NULL)`); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	cats := []string{"books", "games", "music", "garden"}
	for i := 1; i <= 500; i++ {
		if _, err := tx.Exec(`INSERT INTO products VALUES (?, ?, ?, ?, ?)`, i, fmt.Sprintf("Product %d", i), cats[i%4], float64(i%100)+0.99, i); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
