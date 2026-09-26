// Package config loads the application's settings from the environment.
//
// The environment is the only source. .env is read by make and by docker
// compose, both of which export into the process environment, so there is never
// a second place a value could disagree with.
package config

import (
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Environment names a deployment context.
type Environment string

const (
	// EnvLocal is a developer machine: text logs, insecure cookies allowed.
	EnvLocal Environment = "local"
	// EnvProduction is anything real.
	EnvProduction Environment = "production"
)

// IsLocal reports whether this is a developer machine.
func (e Environment) IsLocal() bool { return e == EnvLocal }

// Config is the whole of the application's configuration. It is deliberately
// flat: a nested struct buys nothing here and makes the env tags harder to read
// against docs/PROJECT_INFRA.md.
type Config struct {
	Env     Environment `env:"APP_ENV"      envDefault:"local"`
	Addr    string      `env:"APP_ADDR"     envDefault:":8080"`
	BaseURL string      `env:"APP_BASE_URL" envDefault:"http://localhost:8080"`

	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`

	DBPath        string        `env:"DB_PATH"         envDefault:"./data/chonkboard.db"`
	DBMaxReaders  int           `env:"DB_MAX_READERS"  envDefault:"0"`
	DBConnMaxLife time.Duration `env:"DB_CONN_MAX_LIFETIME" envDefault:"0s"`

	// SessionTTL has no day unit: Go durations stop at hours, so seven days is
	// 168h. "7d" does not parse.
	SessionTTL          time.Duration `env:"SESSION_TTL"           envDefault:"168h"`
	SessionCookieSecure bool          `env:"SESSION_COOKIE_SECURE" envDefault:"false"`

	// SuperAdminPassword is used once, on first boot against an empty users
	// table. Left unset, one is generated and printed to stdout a single time.
	SuperAdminEmail    string `env:"SUPER_ADMIN_EMAIL"`
	SuperAdminPassword string `env:"SUPER_ADMIN_PASSWORD"`

	UploadDir      string `env:"UPLOAD_DIR"       envDefault:"./data/uploads"`
	UploadMaxBytes int64  `env:"UPLOAD_MAX_BYTES" envDefault:"10485760"`

	BackupDir      string        `env:"BACKUP_DIR"      envDefault:"./data/backups"`
	BackupInterval time.Duration `env:"BACKUP_INTERVAL" envDefault:"6h"`
	BackupKeep     int           `env:"BACKUP_KEEP"     envDefault:"28"`
}

// Load reads and validates the environment.
func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validate rejects a configuration that would fail later and less clearly. It is
// the difference between a startup error naming the variable and a panic three
// requests in.
func (c Config) validate() error {
	var problems []string

	switch c.Env {
	case EnvLocal, EnvProduction:
	default:
		problems = append(problems, fmt.Sprintf(
			"APP_ENV is %q, want %q or %q", c.Env, EnvLocal, EnvProduction))
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf(
			"LOG_LEVEL is %q, want debug, info, warn or error", c.LogLevel))
	}

	if strings.TrimSpace(c.Addr) == "" {
		problems = append(problems, "APP_ADDR is empty")
	}
	if strings.TrimSpace(c.DBPath) == "" {
		problems = append(problems, "DB_PATH is empty")
	}
	if c.SessionTTL <= 0 {
		problems = append(problems, "SESSION_TTL must be positive (days are not a unit; use 168h)")
	}
	if c.UploadMaxBytes <= 0 {
		problems = append(problems, "UPLOAD_MAX_BYTES must be positive")
	}
	if c.BackupInterval <= 0 {
		problems = append(problems, "BACKUP_INTERVAL must be positive")
	}
	if c.BackupKeep < 1 {
		problems = append(problems, "BACKUP_KEEP must be at least 1")
	}
	if c.SuperAdminEmail != "" {
		if _, err := mail.ParseAddress(c.SuperAdminEmail); err != nil {
			problems = append(problems, "SUPER_ADMIN_EMAIL is not a valid address")
		}
	}
	// Sessions are cookies. Over plain HTTP they are readable in transit, so a
	// production deployment that forgets this is handing them out.
	if c.Env == EnvProduction && !c.SessionCookieSecure {
		problems = append(problems,
			"SESSION_COOKIE_SECURE must be true when APP_ENV=production")
	}

	if len(problems) > 0 {
		return fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}
	return nil
}
