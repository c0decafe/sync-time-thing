package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.ListenAddr != ":8080" {
		t.Fatalf("unexpected listen addr: %s", cfg.ListenAddr)
	}
	if cfg.DBPath != "data/sync-time-thing.db" && cfg.DBPath != "./data/sync-time-thing.db" {
		t.Fatalf("unexpected db path: %s", cfg.DBPath)
	}
	if cfg.SessionTTL != 24*time.Hour {
		t.Fatalf("unexpected ttl: %s", cfg.SessionTTL)
	}
	if cfg.Timezone != "UTC" {
		t.Fatalf("unexpected timezone: %s", cfg.Timezone)
	}
	if cfg.RuleRunRetention != 90*24*time.Hour {
		t.Fatalf("unexpected rule run retention: %s", cfg.RuleRunRetention)
	}
	if len(cfg.EncryptionKey) != 0 {
		t.Fatalf("expected empty encryption key by default, got %d bytes", len(cfg.EncryptionKey))
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{
		"SYNCTIMETHING_LISTEN_ADDR":        "127.0.0.1:9999",
		"SYNCTIMETHING_DATA_DIR":           "/srv/data",
		"SYNCTIMETHING_DB_PATH":            "/srv/state/app.db",
		"SYNCTIMETHING_SESSION_COOKIE":     "custom",
		"SYNCTIMETHING_SESSION_TTL":        "2h",
		"SYNCTIMETHING_SECURE_COOKIES":     "true",
		"SYNCTIMETHING_ADMIN_USERNAME":     "root",
		"SYNCTIMETHING_ADMIN_PASSWORD":     "secret",
		"SYNCTIMETHING_TIMEZONE":           "Europe/London",
		"SYNCTIMETHING_ENCRYPTION_KEY":     base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
		"SYNCTIMETHING_RULE_RUN_RETENTION": "720h",
	}
	cfg, err := Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.ListenAddr != env["SYNCTIMETHING_LISTEN_ADDR"] || cfg.DBPath != env["SYNCTIMETHING_DB_PATH"] || cfg.SessionCookieName != env["SYNCTIMETHING_SESSION_COOKIE"] {
		t.Fatal("expected overrides to be used")
	}
	if !cfg.SecureCookies || cfg.AdminUsername != "root" || cfg.AdminPassword != "secret" {
		t.Fatal("expected auth overrides to be used")
	}
	if len(cfg.EncryptionKey) != 32 || cfg.RuleRunRetention != 30*24*time.Hour {
		t.Fatal("expected encryption key and retention overrides to be used")
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "bad ttl",
			env:  map[string]string{"SYNCTIMETHING_SESSION_TTL": "abc"},
			want: "SYNCTIMETHING_SESSION_TTL",
		},
		{
			name: "bad retention",
			env:  map[string]string{"SYNCTIMETHING_RULE_RUN_RETENTION": "forever"},
			want: "SYNCTIMETHING_RULE_RUN_RETENTION",
		},
		{
			name: "negative retention",
			env:  map[string]string{"SYNCTIMETHING_RULE_RUN_RETENTION": "-1h"},
			want: "rule run retention cannot be negative",
		},
		{
			name: "bad bool",
			env:  map[string]string{"SYNCTIMETHING_SECURE_COOKIES": "maybe"},
			want: "SYNCTIMETHING_SECURE_COOKIES",
		},
		{
			name: "bad encryption key",
			env:  map[string]string{"SYNCTIMETHING_ENCRYPTION_KEY": "bad"},
			want: "SYNCTIMETHING_ENCRYPTION_KEY",
		},
		{
			name: "blank listen addr",
			env:  map[string]string{"SYNCTIMETHING_LISTEN_ADDR": "   ", "SYNCTIMETHING_DATA_DIR": "/tmp"},
			want: "listen address",
		},
		{
			name: "blank admin username",
			env:  map[string]string{"SYNCTIMETHING_ADMIN_USERNAME": "   "},
			want: "admin username",
		},
		{
			name: "bad timezone",
			env:  map[string]string{"SYNCTIMETHING_TIMEZONE": "Mars/Olympus"},
			want: "load timezone",
		},
		{
			name: "blank timezone",
			env:  map[string]string{"SYNCTIMETHING_TIMEZONE": "   "},
			want: "timezone cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(func(key string) string { return tt.env[key] })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestParseEncryptionKeyHelper(t *testing.T) {
	raw := []byte("0123456789abcdef0123456789abcdef")

	decoded, err := parseEncryptionKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil || len(decoded) != 32 {
		t.Fatalf("parseEncryptionKey std returned (%d, %v)", len(decoded), err)
	}

	decoded, err = parseEncryptionKey(base64.RawURLEncoding.EncodeToString(raw))
	if err != nil || len(decoded) != 32 {
		t.Fatalf("parseEncryptionKey raw-url returned (%d, %v)", len(decoded), err)
	}

	decoded, err = parseEncryptionKey("   ")
	if err != nil || len(decoded) != 0 {
		t.Fatalf("parseEncryptionKey blank returned (%d, %v)", len(decoded), err)
	}

	if _, err := parseEncryptionKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil || !strings.Contains(err.Error(), "exactly 32 bytes") {
		t.Fatalf("unexpected parseEncryptionKey short error: %v", err)
	}
	if _, err := parseEncryptionKey("***"); err == nil || !strings.Contains(err.Error(), "valid base64") {
		t.Fatalf("unexpected parseEncryptionKey invalid error: %v", err)
	}
}
