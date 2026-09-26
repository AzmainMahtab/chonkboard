package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, EnvLocal, cfg.Env)
	assert.True(t, cfg.Env.IsLocal())
	assert.Equal(t, ":8080", cfg.Addr)
	assert.Equal(t, "./data/chonkboard.db", cfg.DBPath)
	assert.Equal(t, 7*24*time.Hour, cfg.SessionTTL, "168h is seven days")
	assert.Equal(t, int64(10*1024*1024), cfg.UploadMaxBytes)
	assert.Equal(t, 6*time.Hour, cfg.BackupInterval)
	assert.Equal(t, 28, cfg.BackupKeep)
	assert.False(t, cfg.SessionCookieSecure)
}

func TestLoadReadsTheEnvironment(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("APP_ADDR", ":9000")
	t.Setenv("DB_PATH", "/data/board.db")
	t.Setenv("SESSION_TTL", "24h")
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("SUPER_ADMIN_EMAIL", "owner@example.com")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, EnvProduction, cfg.Env)
	assert.False(t, cfg.Env.IsLocal())
	assert.Equal(t, ":9000", cfg.Addr)
	assert.Equal(t, 24*time.Hour, cfg.SessionTTL)
}

func TestValidationRejectsBadConfiguration(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "unknown environment",
			env:  map[string]string{"APP_ENV": "staging"},
			want: `APP_ENV is "staging"`,
		},
		{
			name: "unknown log level",
			env:  map[string]string{"LOG_LEVEL": "verbose"},
			want: "LOG_LEVEL",
		},
		{
			name: "days are not a duration unit",
			env:  map[string]string{"SESSION_TTL": "0s"},
			want: "use 168h",
		},
		{
			name: "insecure cookies in production",
			env: map[string]string{
				"APP_ENV":               "production",
				"SESSION_COOKIE_SECURE": "false",
			},
			want: "SESSION_COOKIE_SECURE must be true",
		},
		{
			name: "malformed super admin address",
			env:  map[string]string{"SUPER_ADMIN_EMAIL": "not-an-address"},
			want: "SUPER_ADMIN_EMAIL",
		},
		{
			name: "empty database path",
			env:  map[string]string{"DB_PATH": " "},
			want: "DB_PATH is empty",
		},
		{
			name: "backup retention below one",
			env:  map[string]string{"BACKUP_KEEP": "0"},
			want: "BACKUP_KEEP",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestDurationsThatDoNotParseAreRejected(t *testing.T) {
	// The trap this exists for: "7d" looks obviously right and is not a Go
	// duration. Note the parser reports the struct field, not the variable
	// name, so the actionable part of the message is the unit.
	t.Setenv("SESSION_TTL", "7d")

	_, err := Load()
	require.Error(t, err)
	assert.ErrorContains(t, err, `unknown unit "d"`)
	assert.ErrorContains(t, err, "SessionTTL")
}
