package config

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr        string
	DataDir           string
	DBPath            string
	SessionCookieName string
	SessionTTL        time.Duration
	SecureCookies     bool
	AdminUsername     string
	AdminPassword     string
	Timezone          string
	EncryptionKey     []byte
	RuleRunRetention  time.Duration
}

func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		ListenAddr:        defaultString(getenv("SYNCTIMETHING_LISTEN_ADDR"), ":8080"),
		DataDir:           defaultString(getenv("SYNCTIMETHING_DATA_DIR"), "./data"),
		SessionCookieName: defaultString(getenv("SYNCTIMETHING_SESSION_COOKIE"), "syncthing_scheduler_session"),
		AdminUsername:     defaultString(getenv("SYNCTIMETHING_ADMIN_USERNAME"), "admin"),
		AdminPassword:     getenv("SYNCTIMETHING_ADMIN_PASSWORD"),
		Timezone:          defaultString(getenv("SYNCTIMETHING_TIMEZONE"), "UTC"),
	}

	ttl, err := time.ParseDuration(defaultString(getenv("SYNCTIMETHING_SESSION_TTL"), "24h"))
	if err != nil {
		return Config{}, fmt.Errorf("parse SYNCTIMETHING_SESSION_TTL: %w", err)
	}
	cfg.SessionTTL = ttl

	retention, err := time.ParseDuration(defaultString(getenv("SYNCTIMETHING_RULE_RUN_RETENTION"), "2160h"))
	if err != nil {
		return Config{}, fmt.Errorf("parse SYNCTIMETHING_RULE_RUN_RETENTION: %w", err)
	}
	if retention < 0 {
		return Config{}, fmt.Errorf("rule run retention cannot be negative")
	}
	cfg.RuleRunRetention = retention

	secureCookies, err := parseBool(defaultString(getenv("SYNCTIMETHING_SECURE_COOKIES"), "false"))
	if err != nil {
		return Config{}, fmt.Errorf("parse SYNCTIMETHING_SECURE_COOKIES: %w", err)
	}
	cfg.SecureCookies = secureCookies

	encryptionKey, err := parseEncryptionKey(getenv("SYNCTIMETHING_ENCRYPTION_KEY"))
	if err != nil {
		return Config{}, fmt.Errorf("parse SYNCTIMETHING_ENCRYPTION_KEY: %w", err)
	}
	cfg.EncryptionKey = encryptionKey

	cfg.DBPath = getenv("SYNCTIMETHING_DB_PATH")
	if strings.TrimSpace(cfg.DBPath) == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "sync-time-thing.db")
	}

	if strings.TrimSpace(cfg.AdminUsername) == "" {
		return Config{}, fmt.Errorf("admin username cannot be empty")
	}
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return Config{}, fmt.Errorf("listen address cannot be empty")
	}
	if strings.TrimSpace(cfg.Timezone) == "" {
		return Config{}, fmt.Errorf("timezone cannot be empty")
	}
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return Config{}, fmt.Errorf("load timezone %q: %w", cfg.Timezone, err)
	}

	return cfg, nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func parseBool(value string) (bool, error) {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid boolean %q", value)
	}
	return parsed, nil
}

func parseEncryptionKey(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		decoded, err := encoding.DecodeString(trimmed)
		if err == nil {
			if len(decoded) != 32 {
				return nil, fmt.Errorf("must decode to exactly 32 bytes")
			}
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("must be a valid base64-encoded 32-byte key")
}
