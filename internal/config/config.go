// Package config loads the service configuration from flags and the environment.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

// Config is the service configuration. Every field can be set with a flag
// or the environment variable in its env tag.
type Config struct {
	Listen       string        `env:"CDC_LISTEN"`
	DataDir      string        `env:"CDC_DATA_DIR"`
	MeiliURL     string        `env:"MEILI_URL"`
	MeiliAPIKey  string        `env:"MEILI_API_KEY"`
	MeiliGzip    bool          `env:"MEILI_GZIP"`
	MeiliTimeout time.Duration `env:"MEILI_TIMEOUT"`
	AdminToken   string        `env:"CDC_ADMIN_TOKEN"`
	SecretKey    string        `env:"CDC_SECRET_KEY"`
	TasksFile    string        `env:"CDC_TASKS_FILE"`
	LogLevel     string        `env:"CDC_LOG_LEVEL"`
	LogFormat    string        `env:"CDC_LOG_FORMAT"`
	DisableUI    bool          `env:"CDC_DISABLE_UI"`
	StopTimeout  time.Duration `env:"CDC_STOP_TIMEOUT"`

	// SecretKeyPrevious decrypts values written with an older key (rotation).
	SecretKeyPrevious string `env:"CDC_SECRET_KEY_PREVIOUS"`
	// PublicURL is the dashboard base URL used in emailed links.
	PublicURL string `env:"CDC_PUBLIC_URL"`
	// AdminEmail/AdminPassword create the first admin when no user exists.
	AdminEmail    string `env:"CDC_ADMIN_EMAIL"`
	AdminPassword string `env:"CDC_ADMIN_PASSWORD"`
	CookieSecure  bool   `env:"CDC_COOKIE_SECURE"`
	TrustProxy    bool   `env:"CDC_TRUST_PROXY"`
	// Pprof exposes /debug/pprof to admins.
	Pprof bool `env:"CDC_PPROF"`
	// AlertInterval is how often alert rules are evaluated.
	AlertInterval time.Duration `env:"CDC_ALERT_INTERVAL"`
}

// Load parses args (without the program name) over environment defaults.
func Load(args []string) (Config, error) {
	c := FromEnv()
	fs := flag.NewFlagSet("meilisearch-cdc", flag.ContinueOnError)
	c.Flags(fs)
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	return c, nil
}

// FromEnv returns the configuration from the environment and defaults.
func FromEnv() Config {
	return Config{
		Listen:       env("CDC_LISTEN", ":8080"),
		DataDir:      env("CDC_DATA_DIR", "./data"),
		MeiliURL:     env("MEILI_URL", ""),
		MeiliAPIKey:  envSecret("MEILI_API_KEY", env("MEILI_MASTER_KEY", "")),
		MeiliGzip:    envBool("MEILI_GZIP", true),
		MeiliTimeout: envDuration("MEILI_TIMEOUT", 30*time.Second),
		AdminToken:   envSecret("CDC_ADMIN_TOKEN", ""),
		SecretKey:    envSecret("CDC_SECRET_KEY", ""),
		TasksFile:    env("CDC_TASKS_FILE", ""),
		LogLevel:     env("CDC_LOG_LEVEL", "info"),
		LogFormat:    env("CDC_LOG_FORMAT", "text"),
		DisableUI:    envBool("CDC_DISABLE_UI", false),
		StopTimeout:  envDuration("CDC_STOP_TIMEOUT", 20*time.Second),

		SecretKeyPrevious: envSecret("CDC_SECRET_KEY_PREVIOUS", ""),
		PublicURL:         env("CDC_PUBLIC_URL", ""),
		AdminEmail:        env("CDC_ADMIN_EMAIL", ""),
		AdminPassword:     envSecret("CDC_ADMIN_PASSWORD", ""),
		CookieSecure:      envBool("CDC_COOKIE_SECURE", false),
		TrustProxy:        envBool("CDC_TRUST_PROXY", false),
		Pprof:             envBool("CDC_PPROF", false),
		AlertInterval:     envDuration("CDC_ALERT_INTERVAL", 5*time.Second),
	}
}

// Flags registers every setting on fs, defaulting to the current values.
func (c *Config) Flags(fs *flag.FlagSet) {
	fs.StringVar(&c.Listen, "listen", c.Listen, "HTTP listen address (CDC_LISTEN)")
	fs.StringVar(&c.DataDir, "data-dir", c.DataDir, "directory of the state database (CDC_DATA_DIR)")
	fs.StringVar(&c.MeiliURL, "meili-url", c.MeiliURL, "pin the service to this Meilisearch instance (MEILI_URL); leave empty to manage destinations in the dashboard")
	fs.StringVar(&c.MeiliAPIKey, "meili-api-key", c.MeiliAPIKey, "Meilisearch API key (MEILI_API_KEY)")
	fs.BoolVar(&c.MeiliGzip, "meili-gzip", c.MeiliGzip, "gzip request bodies sent to Meilisearch (MEILI_GZIP)")
	fs.DurationVar(&c.MeiliTimeout, "meili-timeout", c.MeiliTimeout, "Meilisearch request timeout (MEILI_TIMEOUT)")
	fs.StringVar(&c.AdminToken, "admin-token", c.AdminToken, "bearer token protecting the API and UI (CDC_ADMIN_TOKEN)")
	fs.StringVar(&c.SecretKey, "secret-key", c.SecretKey, "passphrase encrypting stored credentials (CDC_SECRET_KEY)")
	fs.StringVar(&c.TasksFile, "tasks", c.TasksFile, "YAML/JSON file of tasks applied at startup (CDC_TASKS_FILE)")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "debug, info, warn, error (CDC_LOG_LEVEL)")
	fs.StringVar(&c.LogFormat, "log-format", c.LogFormat, "text or json (CDC_LOG_FORMAT)")
	fs.BoolVar(&c.DisableUI, "disable-ui", c.DisableUI, "do not serve the dashboard (CDC_DISABLE_UI)")
	fs.DurationVar(&c.StopTimeout, "stop-timeout", c.StopTimeout, "how long a stopping task may drain (CDC_STOP_TIMEOUT)")
	fs.StringVar(&c.PublicURL, "public-url", c.PublicURL, "dashboard base URL used in emailed links (CDC_PUBLIC_URL)")
	fs.BoolVar(&c.CookieSecure, "cookie-secure", c.CookieSecure, "always set the Secure cookie flag (CDC_COOKIE_SECURE)")
	fs.BoolVar(&c.TrustProxy, "trust-proxy", c.TrustProxy, "trust X-Forwarded-For/Proto from a reverse proxy (CDC_TRUST_PROXY)")
	fs.BoolVar(&c.Pprof, "pprof", c.Pprof, "expose /debug/pprof to admins (CDC_PPROF)")
	fs.DurationVar(&c.AlertInterval, "alert-interval", c.AlertInterval, "how often alert rules are evaluated (CDC_ALERT_INTERVAL)")
}

// DBPath returns the path of the state database.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "cdc.db") }

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

// envSecret reads K, or the file named by K_FILE (Docker/Kubernetes
// secrets), so secrets need not live in the environment.
func envSecret(k, def string) string {
	if path := os.Getenv(k + "_FILE"); path != "" {
		if raw, err := os.ReadFile(path); err == nil { // #nosec G304 -- operator-supplied *_FILE path
			return strings.TrimRight(string(raw), "\r\n")
		}
	}
	return env(k, def)
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

// LoadTasks reads a declarative tasks file. Values like ${VAR} are expanded
// from the environment so credentials stay out of the file.
func LoadTasks(path string) ([]model.Task, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied tasks file
	if err != nil {
		return nil, err
	}
	expanded := os.Expand(string(raw), func(k string) string { return os.Getenv(k) })
	var doc struct {
		Tasks []map[string]any `yaml:"tasks" json:"tasks"`
	}
	if err := yaml.Unmarshal([]byte(expanded), &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// Re-encode through JSON so the model's JSON tags and Duration parsing apply.
	tasks := make([]model.Task, 0, len(doc.Tasks))
	for i, m := range doc.Tasks {
		var t model.Task
		if err := remarshal(m, &t); err != nil {
			return nil, fmt.Errorf("task #%d: %w", i+1, err)
		}
		if strings.TrimSpace(t.ID) == "" {
			return nil, fmt.Errorf("task #%d: id is required in a tasks file", i+1)
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}
