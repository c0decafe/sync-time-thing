package store

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mnm/sync-time-thing/internal/domain"
)

func fixedTime() time.Time {
	return time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)
}

func testEncryptionKey() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func newTestStore(t *testing.T, options ...Option) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	store := New(db, fixedTime, options...)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate returned error: %v", err)
	}
	if err := store.EnsureSettings(context.Background(), "UTC"); err != nil {
		t.Fatalf("EnsureSettings returned error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestOpenValidation(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("expected empty path to fail")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing", "app.db")); err == nil {
		t.Fatal("expected missing parent directory to fail")
	}
}

func TestAdminAndSessions(t *testing.T) {
	store := newTestStore(t)
	hasAdmin, err := store.HasAdmin(context.Background())
	if err != nil || hasAdmin {
		t.Fatalf("expected no admin, got hasAdmin=%v err=%v", hasAdmin, err)
	}
	if err := store.EnsureAdmin(context.Background(), "admin", "hash"); err != nil {
		t.Fatalf("EnsureAdmin returned error: %v", err)
	}
	user, err := store.GetAdmin(context.Background(), "admin")
	if err != nil || user.PasswordHash != "hash" {
		t.Fatalf("GetAdmin returned (%+v, %v)", user, err)
	}
	if _, err := store.GetAdmin(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	expiresAt := fixedTime().Add(time.Hour)
	if err := store.CreateSession(context.Background(), "admin", "token", expiresAt); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session, err := store.GetSession(context.Background(), "token", fixedTime())
	if err != nil || session.Username != "admin" {
		t.Fatalf("GetSession returned (%+v, %v)", session, err)
	}
	if err := store.DeleteSession(context.Background(), "token"); err != nil {
		t.Fatalf("DeleteSession returned error: %v", err)
	}
	if _, err := store.GetSession(context.Background(), "token", fixedTime()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted session to be missing, got %v", err)
	}

	if err := store.CreateSession(context.Background(), "admin", "expired", fixedTime().Add(-time.Minute)); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if _, err := store.GetSession(context.Background(), "expired", fixedTime()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected expired session to be missing, got %v", err)
	}

	if err := store.CreateSession(context.Background(), "admin", "fresh", fixedTime().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	var count int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM sessions`).Scan(&count); err != nil {
		t.Fatalf("QueryRowContext returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected expired sessions to be pruned, got %d session rows", count)
	}
}

func TestSettingsRulesAndRuns(t *testing.T) {
	store := newTestStore(t, WithEncryptionKey(testEncryptionKey()))
	settings := domain.Settings{SyncthingURL: "http://syncthing:8384", SyncthingAPIKey: "key", Timezone: "Europe/London"}
	if err := store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatalf("SaveSettings returned error: %v", err)
	}
	loadedSettings, err := store.GetSettings(context.Background())
	if err != nil || loadedSettings.SyncthingURL != settings.SyncthingURL || loadedSettings.Timezone != settings.Timezone {
		t.Fatalf("GetSettings returned (%+v, %v)", loadedSettings, err)
	}

	rule, err := store.SaveRule(context.Background(), domain.Rule{
		Name:       "Night pause",
		Schedule:   "0 22 * * *",
		Action:     domain.ActionPause,
		TargetKind: domain.TargetGlobal,
		Enabled:    true,
		TargetName: "All devices",
	})
	if err != nil {
		t.Fatalf("SaveRule insert returned error: %v", err)
	}
	if rule.ID == 0 || rule.LastEvaluatedAt == nil {
		t.Fatalf("unexpected inserted rule: %+v", rule)
	}

	rules, err := store.ListRules(context.Background())
	if err != nil || len(rules) != 1 {
		t.Fatalf("ListRules returned (%+v, %v)", rules, err)
	}

	rule.Name = "Updated"
	updatedRule, err := store.SaveRule(context.Background(), rule)
	if err != nil || updatedRule.Name != "Updated" {
		t.Fatalf("SaveRule update returned (%+v, %v)", updatedRule, err)
	}
	if err := store.MarkRuleEvaluated(context.Background(), updatedRule.ID, fixedTime().Add(time.Hour)); err != nil {
		t.Fatalf("MarkRuleEvaluated returned error: %v", err)
	}
	loadedRule, err := store.GetRule(context.Background(), updatedRule.ID)
	if err != nil || loadedRule.LastEvaluatedAt == nil {
		t.Fatalf("GetRule returned (%+v, %v)", loadedRule, err)
	}

	run := domain.RuleRun{
		RuleID:       loadedRule.ID,
		RuleName:     loadedRule.Name,
		Action:       loadedRule.Action,
		TargetKind:   loadedRule.TargetKind,
		TargetID:     loadedRule.TargetID,
		TargetName:   loadedRule.TargetName,
		ScheduledFor: fixedTime(),
		ExecutedAt:   fixedTime(),
		Status:       "success",
		Message:      "executed",
	}
	if err := store.RecordRuleRun(context.Background(), run); err != nil {
		t.Fatalf("RecordRuleRun returned error: %v", err)
	}
	runs, err := store.ListRecentRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].RuleName != loadedRule.Name {
		t.Fatalf("ListRecentRuns returned (%+v, %v)", runs, err)
	}
	if err := store.DeleteRule(context.Background(), loadedRule.ID); err != nil {
		t.Fatalf("DeleteRule returned error: %v", err)
	}
	if _, err := store.GetRule(context.Background(), loadedRule.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted rule to be missing, got %v", err)
	}
	if err := store.DeleteRule(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound deleting missing rule, got %v", err)
	}
}

func TestHelperFunctions(t *testing.T) {
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatal("boolToInt returned unexpected values")
	}
	formatted := formatTime(fixedTime())
	parsed, err := parseTime(formatted)
	if err != nil || !parsed.Equal(fixedTime()) {
		t.Fatalf("parseTime returned (%s, %v)", parsed, err)
	}
	if _, err := parseTime("not-a-time"); err == nil {
		t.Fatal("expected parseTime to fail")
	}
	legacy, err := parseTime("2026-03-30T15:00:00Z")
	if err != nil || !legacy.Equal(fixedTime()) {
		t.Fatalf("parseTime legacy format returned (%s, %v)", legacy, err)
	}
}

func TestNewDefaultsAndClosedStoreErrors(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	store := New(db, nil)
	if store.now == nil {
		t.Fatal("expected default clock to be set")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	methods := []struct {
		name string
		call func() error
	}{
		{"Migrate", func() error { return store.Migrate(context.Background()) }},
		{"EnsureSettings", func() error { return store.EnsureSettings(context.Background(), "") }},
		{"HasAdmin", func() error { _, err := store.HasAdmin(context.Background()); return err }},
		{"GetAdmin", func() error { _, err := store.GetAdmin(context.Background(), "admin"); return err }},
		{"EnsureAdmin", func() error { return store.EnsureAdmin(context.Background(), "admin", "hash") }},
		{"CreateSession", func() error { return store.CreateSession(context.Background(), "admin", "token", fixedTime()) }},
		{"GetSession", func() error { _, err := store.GetSession(context.Background(), "token", fixedTime()); return err }},
		{"DeleteSession", func() error { return store.DeleteSession(context.Background(), "token") }},
		{"GetSettings", func() error { _, err := store.GetSettings(context.Background()); return err }},
		{"SaveSettings", func() error { return store.SaveSettings(context.Background(), domain.Settings{Timezone: "UTC"}) }},
		{"ProtectSettings", func() error { return store.ProtectSettings(context.Background()) }},
		{"PruneRuleRuns", func() error {
			store.ruleRunRetention = time.Hour
			return store.PruneRuleRuns(context.Background())
		}},
		{"ListRules", func() error { _, err := store.ListRules(context.Background()); return err }},
		{"GetRule", func() error { _, err := store.GetRule(context.Background(), 1); return err }},
		{"SaveRuleInsert", func() error {
			_, err := store.SaveRule(context.Background(), domain.Rule{Name: "Rule", Schedule: "0 0 * * *", Action: domain.ActionPause, TargetKind: domain.TargetGlobal, TargetName: "All devices"})
			return err
		}},
		{"SaveRuleUpdate", func() error {
			_, err := store.SaveRule(context.Background(), domain.Rule{ID: 1, Name: "Rule", Schedule: "0 0 * * *", Action: domain.ActionPause, TargetKind: domain.TargetGlobal, TargetName: "All devices"})
			return err
		}},
		{"DeleteRule", func() error { return store.DeleteRule(context.Background(), 1) }},
		{"MarkRuleEvaluated", func() error { return store.MarkRuleEvaluated(context.Background(), 1, fixedTime()) }},
		{"RecordRuleRun", func() error { return store.RecordRuleRun(context.Background(), domain.RuleRun{RuleID: 1}) }},
		{"ListRecentRuns", func() error { _, err := store.ListRecentRuns(context.Background(), 0); return err }},
	}
	for _, method := range methods {
		t.Run(method.name, func(t *testing.T) {
			if method.name == "RecordRuleRun" {
				store.ruleRunRetention = 0
			}
			if err := method.call(); err == nil {
				t.Fatal("expected closed store call to fail")
			}
		})
	}
}

func TestAdditionalStoreBranches(t *testing.T) {
	store := newTestStore(t)
	bareDB, err := Open(filepath.Join(t.TempDir(), "bare.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	bareStore := New(bareDB, fixedTime)
	if err := bareStore.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate returned error: %v", err)
	}
	if _, err := bareStore.GetSettings(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from bare GetSettings, got %v", err)
	}
	_ = bareStore.Close()

	if err := store.EnsureSettings(context.Background(), ""); err != nil {
		t.Fatalf("EnsureSettings with blank timezone returned error: %v", err)
	}

	if _, err := store.SaveRule(context.Background(), domain.Rule{}); err == nil {
		t.Fatal("expected invalid rule to fail validation")
	}
	if _, err := store.SaveRule(context.Background(), domain.Rule{ID: 999, Name: "missing", Schedule: "0 0 * * *", Action: domain.ActionPause, TargetKind: domain.TargetGlobal}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound updating missing rule, got %v", err)
	}

	rows, err := store.db.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'index' ORDER BY name`)
	if err != nil {
		t.Fatalf("QueryContext returned error: %v", err)
	}
	defer rows.Close()
	var indexes []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("Scan returned error: %v", err)
		}
		indexes = append(indexes, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err returned error: %v", err)
	}
	joined := strings.Join(indexes, ",")
	if !strings.Contains(joined, "idx_sessions_expires_at") || !strings.Contains(joined, "idx_rule_runs_executed_at_id") {
		t.Fatalf("expected new indexes to exist, got %v", indexes)
	}
}

func TestSettingsSecretProtectionAndRetention(t *testing.T) {
	store := newTestStore(t, WithEncryptionKey(testEncryptionKey()), WithRuleRunRetention(24*time.Hour))
	rule, err := store.SaveRule(context.Background(), domain.Rule{
		Name:       "Retention rule",
		Schedule:   "0 0 * * *",
		Action:     domain.ActionPause,
		TargetKind: domain.TargetGlobal,
		TargetName: "All devices",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("SaveRule returned error: %v", err)
	}

	settings := domain.Settings{SyncthingURL: "http://syncthing:8384", SyncthingAPIKey: "secret-key", Timezone: "UTC"}
	if err := store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatalf("SaveSettings returned error: %v", err)
	}

	var rawSecret string
	if err := store.db.QueryRowContext(context.Background(), `SELECT syncthing_api_key FROM app_settings WHERE id = 1`).Scan(&rawSecret); err != nil {
		t.Fatalf("QueryRowContext returned error: %v", err)
	}
	if !strings.HasPrefix(rawSecret, encryptedSecretPrefix) {
		t.Fatalf("expected encrypted secret, got %q", rawSecret)
	}

	loaded, err := store.GetSettings(context.Background())
	if err != nil || loaded.SyncthingAPIKey != settings.SyncthingAPIKey {
		t.Fatalf("GetSettings returned (%+v, %v)", loaded, err)
	}

	if _, err := store.db.ExecContext(context.Background(), `
INSERT INTO rule_runs (rule_id, rule_name, action, target_kind, target_id, target_name, scheduled_for, executed_at, status, message)
VALUES (?, 'old', 'pause', 'global', '', '', ?, ?, 'success', 'old')
`, rule.ID, formatTime(fixedTime().Add(-48*time.Hour)), formatTime(fixedTime().Add(-48*time.Hour))); err != nil {
		t.Fatalf("ExecContext returned error: %v", err)
	}
	if err := store.RecordRuleRun(context.Background(), domain.RuleRun{
		RuleID:       rule.ID,
		RuleName:     "new",
		Action:       domain.ActionPause,
		TargetKind:   domain.TargetGlobal,
		ScheduledFor: fixedTime(),
		ExecutedAt:   fixedTime(),
		Status:       "success",
		Message:      "new",
	}); err != nil {
		t.Fatalf("RecordRuleRun returned error: %v", err)
	}
	var count int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM rule_runs`).Scan(&count); err != nil {
		t.Fatalf("QueryRowContext returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected old rule runs to be pruned, got %d rows", count)
	}
}

func TestProtectSettingsMigrationAndErrors(t *testing.T) {
	t.Run("rejects plaintext secret", func(t *testing.T) {
		store := newTestStore(t)
		if _, err := store.db.ExecContext(context.Background(), `UPDATE app_settings SET syncthing_api_key = 'legacy-key' WHERE id = 1`); err != nil {
			t.Fatalf("ExecContext returned error: %v", err)
		}
		if err := store.ProtectSettings(context.Background()); err == nil || !strings.Contains(err.Error(), "unsupported plaintext Syncthing API key") {
			t.Fatalf("unexpected ProtectSettings error: %v", err)
		}
	})

	t.Run("returns nil for blank secret", func(t *testing.T) {
		store := newTestStore(t, WithEncryptionKey(testEncryptionKey()))
		if err := store.ProtectSettings(context.Background()); err != nil {
			t.Fatalf("ProtectSettings returned error: %v", err)
		}
	})

	t.Run("allows missing settings row", func(t *testing.T) {
		db, err := Open(filepath.Join(t.TempDir(), "bare.db"))
		if err != nil {
			t.Fatalf("Open returned error: %v", err)
		}
		store := New(db, fixedTime)
		if err := store.Migrate(context.Background()); err != nil {
			t.Fatalf("Migrate returned error: %v", err)
		}
		defer func() { _ = store.Close() }()
		if err := store.ProtectSettings(context.Background()); err != nil {
			t.Fatalf("ProtectSettings returned error: %v", err)
		}
	})

	t.Run("accepts already encrypted secret", func(t *testing.T) {
		store := newTestStore(t, WithEncryptionKey(testEncryptionKey()))
		if err := store.SaveSettings(context.Background(), domain.Settings{
			SyncthingURL:    "http://syncthing:8384",
			SyncthingAPIKey: "secret-key",
			Timezone:        "UTC",
		}); err != nil {
			t.Fatalf("SaveSettings returned error: %v", err)
		}
		if err := store.ProtectSettings(context.Background()); err != nil {
			t.Fatalf("ProtectSettings returned error: %v", err)
		}
	})
}

func TestEncryptedSettingsErrorPaths(t *testing.T) {
	t.Run("get settings allows blank stored secret", func(t *testing.T) {
		store := newTestStore(t)
		settings, err := store.GetSettings(context.Background())
		if err != nil || settings.SyncthingAPIKey != "" {
			t.Fatalf("unexpected GetSettings result: (%+v, %v)", settings, err)
		}
	})

	t.Run("save settings requires encryption key", func(t *testing.T) {
		store := newTestStore(t)
		if err := store.SaveSettings(context.Background(), domain.Settings{
			SyncthingURL:    "http://syncthing:8384",
			SyncthingAPIKey: "secret-key",
			Timezone:        "UTC",
		}); err == nil || !strings.Contains(err.Error(), "SYNCTIMETHING_ENCRYPTION_KEY") {
			t.Fatalf("unexpected SaveSettings error: %v", err)
		}
	})

	t.Run("save settings rejects invalid key length", func(t *testing.T) {
		store := newTestStore(t, WithEncryptionKey([]byte("short")))
		if err := store.SaveSettings(context.Background(), domain.Settings{
			SyncthingURL:    "http://syncthing:8384",
			SyncthingAPIKey: "secret-key",
			Timezone:        "UTC",
		}); err == nil || !strings.Contains(err.Error(), "initialize encryption key") {
			t.Fatalf("unexpected SaveSettings error: %v", err)
		}
	})

	t.Run("get settings requires encryption key for encrypted secret", func(t *testing.T) {
		store := newTestStore(t, WithEncryptionKey(testEncryptionKey()))
		if err := store.SaveSettings(context.Background(), domain.Settings{
			SyncthingURL:    "http://syncthing:8384",
			SyncthingAPIKey: "secret-key",
			Timezone:        "UTC",
		}); err != nil {
			t.Fatalf("SaveSettings returned error: %v", err)
		}
		unkeyed := New(store.db, fixedTime)
		if _, err := unkeyed.GetSettings(context.Background()); err == nil || !strings.Contains(err.Error(), "SYNCTIMETHING_ENCRYPTION_KEY") {
			t.Fatalf("unexpected GetSettings error: %v", err)
		}
	})

	t.Run("get settings rejects legacy plaintext when present", func(t *testing.T) {
		store := newTestStore(t)
		if _, err := store.db.ExecContext(context.Background(), `UPDATE app_settings SET syncthing_api_key = 'legacy-key' WHERE id = 1`); err != nil {
			t.Fatalf("ExecContext returned error: %v", err)
		}
		if _, err := store.GetSettings(context.Background()); err == nil || !strings.Contains(err.Error(), "unsupported plaintext Syncthing API key") {
			t.Fatalf("unexpected GetSettings error: %v", err)
		}
	})

	t.Run("get settings rejects malformed ciphertext", func(t *testing.T) {
		store := newTestStore(t, WithEncryptionKey(testEncryptionKey()))
		if _, err := store.db.ExecContext(context.Background(), `UPDATE app_settings SET syncthing_api_key = ? WHERE id = 1`, encryptedSecretPrefix+"AA"); err != nil {
			t.Fatalf("ExecContext returned error: %v", err)
		}
		if _, err := store.GetSettings(context.Background()); err == nil || !strings.Contains(err.Error(), "malformed ciphertext") {
			t.Fatalf("unexpected GetSettings error: %v", err)
		}
	})

	t.Run("get settings rejects undecodable ciphertext", func(t *testing.T) {
		store := newTestStore(t, WithEncryptionKey(testEncryptionKey()))
		if _, err := store.db.ExecContext(context.Background(), `UPDATE app_settings SET syncthing_api_key = ? WHERE id = 1`, encryptedSecretPrefix+"***"); err != nil {
			t.Fatalf("ExecContext returned error: %v", err)
		}
		if _, err := store.GetSettings(context.Background()); err == nil || !strings.Contains(err.Error(), "decode encrypted secret") {
			t.Fatalf("unexpected GetSettings error: %v", err)
		}
	})
}

func TestSecretHelperBranches(t *testing.T) {
	if value, err := decryptSecret(testEncryptionKey(), "plain"); err != nil || value != "plain" {
		t.Fatalf("decryptSecret plain returned (%q, %v)", value, err)
	}
	if value, err := decryptSecret(testEncryptionKey(), ""); err != nil || value != "" {
		t.Fatalf("decryptSecret empty returned (%q, %v)", value, err)
	}
	if value, err := encryptSecret(testEncryptionKey(), ""); err != nil || value != "" {
		t.Fatalf("encryptSecret empty returned (%q, %v)", value, err)
	}
	if _, err := encryptSecret(nil, "secret"); err == nil || !strings.Contains(err.Error(), "SYNCTIMETHING_ENCRYPTION_KEY") {
		t.Fatalf("unexpected encryptSecret nil-key error: %v", err)
	}
	if _, err := newSecretCipher([]byte("short")); err == nil || !strings.Contains(err.Error(), "initialize encryption key") {
		t.Fatalf("unexpected newSecretCipher short-key error: %v", err)
	}
	originalNewGCMCipher := newGCMCipher
	newGCMCipher = func(cipher.Block) (cipher.AEAD, error) { return nil, errors.New("gcm") }
	if _, err := newSecretCipher(testEncryptionKey()); err == nil || !strings.Contains(err.Error(), "initialize encryption cipher") {
		t.Fatalf("unexpected newSecretCipher gcm error: %v", err)
	}
	newGCMCipher = originalNewGCMCipher

	originalReadSecretRandom := readSecretRandom
	readSecretRandom = func([]byte) (int, error) { return 0, errors.New("entropy") }
	if _, err := encryptSecret(testEncryptionKey(), "secret"); err == nil || !strings.Contains(err.Error(), "read secret nonce") {
		t.Fatalf("unexpected encryptSecret entropy error: %v", err)
	}
	readSecretRandom = originalReadSecretRandom

	encrypted, err := encryptSecret(testEncryptionKey(), "secret")
	if err != nil {
		t.Fatalf("encryptSecret returned error: %v", err)
	}
	if _, err := decryptSecret([]byte("abcdef0123456789abcdef0123456789"), encrypted); err == nil || !strings.Contains(err.Error(), "decrypt stored secret") {
		t.Fatalf("unexpected decryptSecret wrong-key error: %v", err)
	}
	if _, err := decryptSecret(testEncryptionKey(), encryptedSecretPrefix+"***"); err == nil || !strings.Contains(err.Error(), "decode encrypted secret") {
		t.Fatalf("unexpected decryptSecret decode error: %v", err)
	}
	if _, err := decryptSecret(testEncryptionKey(), encryptedSecretPrefix+base64.RawStdEncoding.EncodeToString([]byte("tiny"))); err == nil || !strings.Contains(err.Error(), "malformed ciphertext") {
		t.Fatalf("unexpected decryptSecret malformed error: %v", err)
	}
}

func TestRecordRuleRunRetentionError(t *testing.T) {
	store := newTestStore(t, WithRuleRunRetention(time.Hour))
	rule, err := store.SaveRule(context.Background(), domain.Rule{
		Name:       "Retention error rule",
		Schedule:   "0 0 * * *",
		Action:     domain.ActionPause,
		TargetKind: domain.TargetGlobal,
		TargetName: "All devices",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("SaveRule returned error: %v", err)
	}

	originalPruneRuleRuns := pruneRuleRuns
	pruneRuleRuns = func(context.Context, *sql.DB, time.Time) error {
		return errors.New("delete expired rule runs: boom")
	}
	defer func() { pruneRuleRuns = originalPruneRuleRuns }()

	if err := store.RecordRuleRun(context.Background(), domain.RuleRun{
		RuleID:       rule.ID,
		RuleName:     rule.Name,
		Action:       rule.Action,
		TargetKind:   rule.TargetKind,
		ScheduledFor: fixedTime(),
		ExecutedAt:   fixedTime(),
		Status:       "success",
		Message:      "ok",
	}); err == nil || !strings.Contains(err.Error(), "delete expired rule runs") {
		t.Fatalf("unexpected RecordRuleRun error: %v", err)
	}
}

type scanFunc func(dest ...any) error

func (fn scanFunc) Scan(dest ...any) error { return fn(dest...) }

type stubTx struct {
	execErr   error
	commitErr error
}

func (s *stubTx) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	if s.execErr != nil {
		return nil, s.execErr
	}
	return stubResult{}, nil
}

func (s *stubTx) Commit() error   { return s.commitErr }
func (s *stubTx) Rollback() error { return nil }

type stubResult struct {
	lastInsertIDErr error
	rowsAffectedErr error
}

func (s stubResult) LastInsertId() (int64, error) {
	if s.lastInsertIDErr != nil {
		return 0, s.lastInsertIDErr
	}
	return 1, nil
}

func (s stubResult) RowsAffected() (int64, error) {
	if s.rowsAffectedErr != nil {
		return 0, s.rowsAffectedErr
	}
	return 1, nil
}

type stubRows struct {
	remaining int
	scan      func(dest ...any) error
	err       error
}

func (s *stubRows) Next() bool {
	if s.remaining <= 0 {
		return false
	}
	s.remaining--
	return true
}

func (s *stubRows) Scan(dest ...any) error {
	if s.scan != nil {
		return s.scan(dest...)
	}
	return nil
}

func (s *stubRows) Err() error   { return s.err }
func (s *stubRows) Close() error { return nil }

func TestScanRuleErrorPaths(t *testing.T) {
	tests := []struct {
		name string
		fn   scanFunc
	}{
		{
			name: "scan error",
			fn:   func(dest ...any) error { return sql.ErrNoRows },
		},
		{
			name: "bad action",
			fn: func(dest ...any) error {
				*(dest[0].(*int64)) = 1
				*(dest[1].(*string)) = "name"
				*(dest[2].(*string)) = "0 0 * * *"
				*(dest[3].(*string)) = "nope"
				*(dest[4].(*string)) = "global"
				*(dest[5].(*string)) = ""
				*(dest[6].(*string)) = ""
				*(dest[7].(*int)) = 1
				*(dest[8].(*string)) = formatTime(fixedTime())
				*(dest[9].(*string)) = formatTime(fixedTime())
				return nil
			},
		},
		{
			name: "bad target kind",
			fn: func(dest ...any) error {
				*(dest[0].(*int64)) = 1
				*(dest[1].(*string)) = "name"
				*(dest[2].(*string)) = "0 0 * * *"
				*(dest[3].(*string)) = "pause"
				*(dest[4].(*string)) = "nope"
				*(dest[5].(*string)) = ""
				*(dest[6].(*string)) = ""
				*(dest[7].(*int)) = 1
				*(dest[8].(*string)) = formatTime(fixedTime())
				*(dest[9].(*string)) = formatTime(fixedTime())
				return nil
			},
		},
		{
			name: "bad timestamps",
			fn: func(dest ...any) error {
				*(dest[0].(*int64)) = 1
				*(dest[1].(*string)) = "name"
				*(dest[2].(*string)) = "0 0 * * *"
				*(dest[3].(*string)) = "pause"
				*(dest[4].(*string)) = "global"
				*(dest[5].(*string)) = ""
				*(dest[6].(*string)) = ""
				*(dest[7].(*int)) = 1
				*(dest[8].(*string)) = "bad"
				*(dest[9].(*string)) = formatTime(fixedTime())
				return nil
			},
		},
		{
			name: "bad updated timestamp",
			fn: func(dest ...any) error {
				*(dest[0].(*int64)) = 1
				*(dest[1].(*string)) = "name"
				*(dest[2].(*string)) = "0 0 * * *"
				*(dest[3].(*string)) = "pause"
				*(dest[4].(*string)) = "global"
				*(dest[5].(*string)) = ""
				*(dest[6].(*string)) = ""
				*(dest[7].(*int)) = 1
				*(dest[8].(*string)) = formatTime(fixedTime())
				*(dest[9].(*string)) = "bad"
				return nil
			},
		},
		{
			name: "bad last evaluated timestamp",
			fn: func(dest ...any) error {
				*(dest[0].(*int64)) = 1
				*(dest[1].(*string)) = "name"
				*(dest[2].(*string)) = "0 0 * * *"
				*(dest[3].(*string)) = "pause"
				*(dest[4].(*string)) = "global"
				*(dest[5].(*string)) = ""
				*(dest[6].(*string)) = ""
				*(dest[7].(*int)) = 1
				*(dest[8].(*string)) = formatTime(fixedTime())
				*(dest[9].(*string)) = formatTime(fixedTime())
				last := dest[10].(*sql.NullString)
				last.Valid = true
				last.String = "bad"
				return nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := scanRule(tt.fn); err == nil {
				t.Fatal("expected scanRule to fail")
			}
		})
	}
}

func TestListRecentRunsDefaults(t *testing.T) {
	store := newTestStore(t)
	rule, err := store.SaveRule(context.Background(), domain.Rule{Name: "Night", Schedule: "0 0 * * *", Action: domain.ActionPause, TargetKind: domain.TargetGlobal, TargetName: "All devices", Enabled: true})
	if err != nil {
		t.Fatalf("SaveRule returned error: %v", err)
	}
	if err := store.RecordRuleRun(context.Background(), domain.RuleRun{RuleID: rule.ID, RuleName: rule.Name, Action: rule.Action, TargetKind: rule.TargetKind, ScheduledFor: fixedTime(), ExecutedAt: fixedTime(), Status: "success", Message: "ok"}); err != nil {
		t.Fatalf("RecordRuleRun returned error: %v", err)
	}
	runs, err := store.ListRecentRuns(context.Background(), 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRecentRuns returned (%+v, %v)", runs, err)
	}
}

func TestInjectedOpenAndMigrationErrors(t *testing.T) {
	originalResolveAbsPath := resolveAbsPath
	resolveAbsPath = func(string) (string, error) { return "", errors.New("abs") }
	if _, err := Open("app.db"); err == nil || !strings.Contains(err.Error(), "resolve database path") {
		t.Fatalf("unexpected resolve error: %v", err)
	}
	resolveAbsPath = originalResolveAbsPath

	originalOpenSQLiteDB := openSQLiteDB
	openSQLiteDB = func(string) (*sql.DB, error) { return nil, errors.New("open") }
	if _, err := Open(filepath.Join(t.TempDir(), "app.db")); err == nil || !strings.Contains(err.Error(), "open sqlite database") {
		t.Fatalf("unexpected open error: %v", err)
	}
	openSQLiteDB = originalOpenSQLiteDB

	originalBeginStoreTx := beginStoreTx
	beginStoreTx = func(*sql.DB, context.Context) (transaction, error) { return &stubTx{execErr: errors.New("exec")}, nil }
	if err := (&Store{}).Migrate(context.Background()); err == nil || !strings.Contains(err.Error(), "execute migration statement") {
		t.Fatalf("unexpected migration exec error: %v", err)
	}
	beginStoreTx = func(*sql.DB, context.Context) (transaction, error) {
		return &stubTx{commitErr: errors.New("commit")}, nil
	}
	if err := (&Store{}).Migrate(context.Background()); err == nil || !strings.Contains(err.Error(), "commit migration transaction") {
		t.Fatalf("unexpected migration commit error: %v", err)
	}
	originalOptimizeSQLite := optimizeSQLite
	beginStoreTx = func(*sql.DB, context.Context) (transaction, error) { return &stubTx{}, nil }
	optimizeSQLite = func(context.Context, *sql.DB) error { return errors.New("optimize sqlite database: optimize") }
	if err := (&Store{}).Migrate(context.Background()); err == nil || !strings.Contains(err.Error(), "optimize sqlite database") {
		t.Fatalf("unexpected migration optimize error: %v", err)
	}
	optimizeSQLite = originalOptimizeSQLite
	beginStoreTx = originalBeginStoreTx
}

func TestSessionCleanupHelperErrors(t *testing.T) {
	store := newTestStore(t)

	originalPurgeExpiredSessions := purgeExpiredSessions
	purgeExpiredSessions = func(context.Context, *sql.DB, time.Time) error {
		return errors.New("delete expired sessions: boom")
	}
	defer func() { purgeExpiredSessions = originalPurgeExpiredSessions }()

	if err := store.CreateSession(context.Background(), "admin", "token", fixedTime().Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "delete expired sessions") {
		t.Fatalf("unexpected CreateSession cleanup error: %v", err)
	}

	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO sessions (token_hash, username, expires_at, created_at) VALUES ('expired-cleanup', 'admin', ?, ?)`, formatTime(fixedTime().Add(-time.Minute)), formatTime(fixedTime())); err != nil {
		t.Fatalf("ExecContext returned error: %v", err)
	}
	if _, err := store.GetSession(context.Background(), "expired-cleanup", fixedTime()); err == nil || !strings.Contains(err.Error(), "delete expired sessions") {
		t.Fatalf("unexpected GetSession cleanup error: %v", err)
	}
}

func TestCreateSessionInsertErrorAfterCleanup(t *testing.T) {
	store := newTestStore(t)

	originalPurgeExpiredSessions := purgeExpiredSessions
	purgeExpiredSessions = func(context.Context, *sql.DB, time.Time) error { return nil }
	defer func() { purgeExpiredSessions = originalPurgeExpiredSessions }()

	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if err := store.CreateSession(context.Background(), "admin", "token", fixedTime().Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "create session") {
		t.Fatalf("unexpected CreateSession insert error: %v", err)
	}
}

func TestCorruptStoredDataBranches(t *testing.T) {
	t.Run("admin updated at", func(t *testing.T) {
		store := newTestStore(t)
		if err := store.EnsureAdmin(context.Background(), "admin", "hash"); err != nil {
			t.Fatalf("EnsureAdmin returned error: %v", err)
		}
		if _, err := store.db.ExecContext(context.Background(), `UPDATE admin_users SET updated_at = 'bad' WHERE id = 1`); err != nil {
			t.Fatalf("ExecContext returned error: %v", err)
		}
		if _, err := store.GetAdmin(context.Background(), "admin"); err == nil {
			t.Fatal("expected GetAdmin to fail with invalid timestamp")
		}
	})

	t.Run("session timestamps", func(t *testing.T) {
		store := newTestStore(t)
		if _, err := store.db.ExecContext(context.Background(), `INSERT INTO sessions (token_hash, username, expires_at, created_at) VALUES ('bad-expiry', 'admin', 'bad', ?)`, formatTime(fixedTime())); err != nil {
			t.Fatalf("ExecContext returned error: %v", err)
		}
		if _, err := store.GetSession(context.Background(), "bad-expiry", fixedTime()); err == nil {
			t.Fatal("expected GetSession to fail for invalid expiry")
		}
		if _, err := store.db.ExecContext(context.Background(), `INSERT INTO sessions (token_hash, username, expires_at, created_at) VALUES ('bad-created', 'admin', ?, 'bad')`, formatTime(fixedTime().Add(time.Hour))); err != nil {
			t.Fatalf("ExecContext returned error: %v", err)
		}
		if _, err := store.GetSession(context.Background(), "bad-created", fixedTime()); err == nil {
			t.Fatal("expected GetSession to fail for invalid created_at")
		}
	})

	t.Run("settings updated at", func(t *testing.T) {
		store := newTestStore(t)
		if _, err := store.db.ExecContext(context.Background(), `UPDATE app_settings SET updated_at = 'bad' WHERE id = 1`); err != nil {
			t.Fatalf("ExecContext returned error: %v", err)
		}
		if _, err := store.GetSettings(context.Background()); err == nil {
			t.Fatal("expected GetSettings to fail with invalid timestamp")
		}
	})
}

func TestInjectedRuleAndRunIterationErrors(t *testing.T) {
	store := newTestStore(t)

	originalQueryRulesRows := queryRulesRows
	queryRulesRows = func(context.Context, *sql.DB) (rowIterator, error) {
		return &stubRows{
			remaining: 1,
			scan: func(dest ...any) error {
				return errors.New("scan")
			},
		}, nil
	}
	if _, err := store.ListRules(context.Background()); err == nil || !strings.Contains(err.Error(), "scan") {
		t.Fatalf("unexpected ListRules scan error: %v", err)
	}
	queryRulesRows = func(context.Context, *sql.DB) (rowIterator, error) {
		return &stubRows{err: errors.New("rows")}, nil
	}
	if _, err := store.ListRules(context.Background()); err == nil || !strings.Contains(err.Error(), "iterate rules") {
		t.Fatalf("unexpected ListRules rows error: %v", err)
	}
	queryRulesRows = originalQueryRulesRows

	originalQueryRecentRunsRows := queryRecentRunsRows
	queryRecentRunsRows = func(context.Context, *sql.DB, int) (rowIterator, error) {
		return &stubRows{
			remaining: 1,
			scan:      func(dest ...any) error { return errors.New("scan") },
		}, nil
	}
	if _, err := store.ListRecentRuns(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "scan rule run") {
		t.Fatalf("unexpected ListRecentRuns scan error: %v", err)
	}
	queryRecentRunsRows = func(context.Context, *sql.DB, int) (rowIterator, error) {
		return &stubRows{
			remaining: 1,
			scan: func(dest ...any) error {
				*(dest[0].(*int64)) = 1
				*(dest[1].(*int64)) = 1
				*(dest[2].(*string)) = "name"
				*(dest[3].(*string)) = "bad"
				*(dest[4].(*string)) = "global"
				*(dest[5].(*string)) = ""
				*(dest[6].(*string)) = ""
				*(dest[7].(*string)) = formatTime(fixedTime())
				*(dest[8].(*string)) = formatTime(fixedTime())
				*(dest[9].(*string)) = "error"
				*(dest[10].(*string)) = "boom"
				return nil
			},
		}, nil
	}
	if _, err := store.ListRecentRuns(context.Background(), 1); err == nil {
		t.Fatal("expected ListRecentRuns to fail for bad action")
	}
	queryRecentRunsRows = func(context.Context, *sql.DB, int) (rowIterator, error) {
		return &stubRows{
			remaining: 1,
			scan: func(dest ...any) error {
				*(dest[0].(*int64)) = 1
				*(dest[1].(*int64)) = 1
				*(dest[2].(*string)) = "name"
				*(dest[3].(*string)) = "pause"
				*(dest[4].(*string)) = "bad"
				*(dest[5].(*string)) = ""
				*(dest[6].(*string)) = ""
				*(dest[7].(*string)) = formatTime(fixedTime())
				*(dest[8].(*string)) = formatTime(fixedTime())
				*(dest[9].(*string)) = "error"
				*(dest[10].(*string)) = "boom"
				return nil
			},
		}, nil
	}
	if _, err := store.ListRecentRuns(context.Background(), 1); err == nil {
		t.Fatal("expected ListRecentRuns to fail for bad target kind")
	}
	queryRecentRunsRows = func(context.Context, *sql.DB, int) (rowIterator, error) {
		return &stubRows{
			remaining: 1,
			scan: func(dest ...any) error {
				*(dest[0].(*int64)) = 1
				*(dest[1].(*int64)) = 1
				*(dest[2].(*string)) = "name"
				*(dest[3].(*string)) = "pause"
				*(dest[4].(*string)) = "global"
				*(dest[5].(*string)) = ""
				*(dest[6].(*string)) = ""
				*(dest[7].(*string)) = "bad"
				*(dest[8].(*string)) = formatTime(fixedTime())
				*(dest[9].(*string)) = "error"
				*(dest[10].(*string)) = "boom"
				return nil
			},
		}, nil
	}
	if _, err := store.ListRecentRuns(context.Background(), 1); err == nil {
		t.Fatal("expected ListRecentRuns to fail for bad scheduled time")
	}
	queryRecentRunsRows = func(context.Context, *sql.DB, int) (rowIterator, error) {
		return &stubRows{
			remaining: 1,
			scan: func(dest ...any) error {
				*(dest[0].(*int64)) = 1
				*(dest[1].(*int64)) = 1
				*(dest[2].(*string)) = "name"
				*(dest[3].(*string)) = "pause"
				*(dest[4].(*string)) = "global"
				*(dest[5].(*string)) = ""
				*(dest[6].(*string)) = ""
				*(dest[7].(*string)) = formatTime(fixedTime())
				*(dest[8].(*string)) = "bad"
				*(dest[9].(*string)) = "error"
				*(dest[10].(*string)) = "boom"
				return nil
			},
		}, nil
	}
	if _, err := store.ListRecentRuns(context.Background(), 1); err == nil {
		t.Fatal("expected ListRecentRuns to fail for bad executed time")
	}
	queryRecentRunsRows = func(context.Context, *sql.DB, int) (rowIterator, error) {
		return &stubRows{err: errors.New("rows")}, nil
	}
	if _, err := store.ListRecentRuns(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "iterate rule runs") {
		t.Fatalf("unexpected ListRecentRuns rows error: %v", err)
	}
	queryRecentRunsRows = originalQueryRecentRunsRows
}

func TestInjectedResultErrors(t *testing.T) {
	store := newTestStore(t)

	originalLastInsertID := lastInsertID
	lastInsertID = func(sql.Result) (int64, error) { return 0, errors.New("last-insert-id") }
	_, err := store.SaveRule(context.Background(), domain.Rule{Name: "Rule", Schedule: "0 0 * * *", Action: domain.ActionPause, TargetKind: domain.TargetGlobal, TargetName: "All devices"})
	if err == nil || !strings.Contains(err.Error(), "inserted rule id") {
		t.Fatalf("unexpected LastInsertId error: %v", err)
	}
	lastInsertID = originalLastInsertID
}
