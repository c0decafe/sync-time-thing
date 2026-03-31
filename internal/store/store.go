package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/mnm/sync-time-thing/internal/domain"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("store: not found")

type transaction interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	Commit() error
	Rollback() error
}

type rowIterator interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

const rulesQuery = `
SELECT id, name, schedule, action, target_kind, target_id, target_name, enabled, created_at, updated_at, last_evaluated_at
FROM rules
ORDER BY name, id
`

const recentRunsQuery = `
SELECT id, rule_id, rule_name, action, target_kind, target_id, target_name, scheduled_for, executed_at, status, message
FROM rule_runs
ORDER BY executed_at DESC, id DESC
LIMIT ?
`

const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"
const encryptedSecretPrefix = "enc:v1:"

var (
	resolveAbsPath = filepath.Abs
	openSQLiteDB   = func(dsn string) (*sql.DB, error) {
		return sql.Open("sqlite", dsn)
	}
	beginStoreTx = func(db *sql.DB, ctx context.Context) (transaction, error) {
		return db.BeginTx(ctx, nil)
	}
	queryRulesRows = func(ctx context.Context, db *sql.DB) (rowIterator, error) {
		return db.QueryContext(ctx, rulesQuery)
	}
	queryRecentRunsRows = func(ctx context.Context, db *sql.DB, limit int) (rowIterator, error) {
		return db.QueryContext(ctx, recentRunsQuery, limit)
	}
	readSecretRandom     = rand.Read
	newGCMCipher         = cipher.NewGCM
	purgeExpiredSessions = func(ctx context.Context, db *sql.DB, cutoff time.Time) error {
		if db == nil {
			return fmt.Errorf("delete expired sessions: nil database")
		}
		_, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, formatTime(cutoff.UTC()))
		if err != nil {
			return fmt.Errorf("delete expired sessions: %w", err)
		}
		return nil
	}
	pruneRuleRuns = func(ctx context.Context, db *sql.DB, cutoff time.Time) error {
		if db == nil {
			return fmt.Errorf("delete expired rule runs: nil database")
		}
		_, err := db.ExecContext(ctx, `DELETE FROM rule_runs WHERE executed_at < ?`, formatTime(cutoff.UTC()))
		if err != nil {
			return fmt.Errorf("delete expired rule runs: %w", err)
		}
		return nil
	}
	optimizeSQLite = func(ctx context.Context, db *sql.DB) error {
		if db == nil {
			return fmt.Errorf("optimize sqlite database: nil database")
		}
		if _, err := db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
			return fmt.Errorf("optimize sqlite database: %w", err)
		}
		return nil
	}
	lastInsertID = func(result sql.Result) (int64, error) {
		return result.LastInsertId()
	}
	rowsAffected = func(result sql.Result) (int64, error) {
		return result.RowsAffected()
	}
)

type Option func(*Store)

type Store struct {
	db               *sql.DB
	now              func() time.Time
	encryptionKey    []byte
	ruleRunRetention time.Duration
}

func Open(path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("database path is required")
	}

	absolutePath, err := resolveAbsPath(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}

	query := url.Values{}
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Set("_txlock", "immediate")
	dsn := (&url.URL{Scheme: "file", Path: absolutePath, RawQuery: query.Encode()}).String()

	db, err := openSQLiteDB(dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}

	return db, nil
}

func New(db *sql.DB, now func() time.Time, options ...Option) *Store {
	if now == nil {
		now = time.Now
	}
	store := &Store{db: db, now: now}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	return store
}

func WithEncryptionKey(key []byte) Option {
	return func(store *Store) {
		store.encryptionKey = append([]byte(nil), key...)
	}
}

func WithRuleRunRetention(retention time.Duration) Option {
	return func(store *Store) {
		store.ruleRunRetention = retention
	}
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS admin_users (
id INTEGER PRIMARY KEY CHECK (id = 1),
username TEXT NOT NULL UNIQUE,
password_hash TEXT NOT NULL,
updated_at TEXT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS sessions (
token_hash TEXT PRIMARY KEY,
username TEXT NOT NULL,
expires_at TEXT NOT NULL,
created_at TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS app_settings (
id INTEGER PRIMARY KEY CHECK (id = 1),
syncthing_url TEXT NOT NULL DEFAULT '',
syncthing_api_key TEXT NOT NULL DEFAULT '',
timezone TEXT NOT NULL DEFAULT 'UTC',
updated_at TEXT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS rules (
id INTEGER PRIMARY KEY AUTOINCREMENT,
name TEXT NOT NULL,
schedule TEXT NOT NULL,
action TEXT NOT NULL,
target_kind TEXT NOT NULL,
target_id TEXT NOT NULL DEFAULT '',
target_name TEXT NOT NULL DEFAULT '',
enabled INTEGER NOT NULL DEFAULT 1,
created_at TEXT NOT NULL,
updated_at TEXT NOT NULL,
last_evaluated_at TEXT
)`,
		`CREATE TABLE IF NOT EXISTS rule_runs (
id INTEGER PRIMARY KEY AUTOINCREMENT,
rule_id INTEGER NOT NULL,
rule_name TEXT NOT NULL,
action TEXT NOT NULL,
target_kind TEXT NOT NULL,
target_id TEXT NOT NULL DEFAULT '',
target_name TEXT NOT NULL DEFAULT '',
scheduled_for TEXT NOT NULL,
executed_at TEXT NOT NULL,
status TEXT NOT NULL,
message TEXT NOT NULL,
FOREIGN KEY(rule_id) REFERENCES rules(id) ON DELETE CASCADE
)`,
		`CREATE INDEX IF NOT EXISTS idx_rule_runs_executed_at_id ON rule_runs(executed_at DESC, id DESC)`,
	}

	tx, err := beginStoreTx(s.db, ctx)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("execute migration statement: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration transaction: %w", err)
	}
	if err := optimizeSQLite(ctx, s.db); err != nil {
		return err
	}
	return nil
}

func (s *Store) EnsureSettings(ctx context.Context, timezone string) error {
	if strings.TrimSpace(timezone) == "" {
		timezone = "UTC"
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO app_settings (id, timezone, updated_at)
VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET
timezone = CASE WHEN app_settings.timezone = '' THEN excluded.timezone ELSE app_settings.timezone END,
updated_at = CASE WHEN app_settings.timezone = '' THEN excluded.updated_at ELSE app_settings.updated_at END
`, timezone, formatTime(s.now().UTC()))
	if err != nil {
		return fmt.Errorf("ensure app settings: %w", err)
	}
	return nil
}

func (s *Store) HasAdmin(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM admin_users`).Scan(&count); err != nil {
		return false, fmt.Errorf("count admin users: %w", err)
	}
	return count > 0, nil
}

func (s *Store) EnsureAdmin(ctx context.Context, username, passwordHash string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO admin_users (id, username, password_hash, updated_at)
VALUES (1, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
username = excluded.username,
password_hash = excluded.password_hash,
updated_at = excluded.updated_at
`, username, passwordHash, formatTime(s.now().UTC()))
	if err != nil {
		return fmt.Errorf("ensure admin user: %w", err)
	}
	return nil
}

func (s *Store) GetAdmin(ctx context.Context, username string) (domain.AdminUser, error) {
	var user domain.AdminUser
	var updated string
	err := s.db.QueryRowContext(ctx, `
SELECT id, username, password_hash, updated_at
FROM admin_users
WHERE username = ?
`, username).Scan(&user.ID, &user.Username, &user.PasswordHash, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.AdminUser{}, ErrNotFound
		}
		return domain.AdminUser{}, fmt.Errorf("query admin user: %w", err)
	}
	user.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return domain.AdminUser{}, err
	}
	return user, nil
}

func (s *Store) CreateSession(ctx context.Context, username, tokenHash string, expiresAt time.Time) error {
	if err := purgeExpiredSessions(ctx, s.db, s.now().UTC()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (token_hash, username, expires_at, created_at)
VALUES (?, ?, ?, ?)
`, tokenHash, username, formatTime(expiresAt.UTC()), formatTime(s.now().UTC()))
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *Store) GetSession(ctx context.Context, tokenHash string, now time.Time) (domain.Session, error) {
	var session domain.Session
	var expiresAt string
	var createdAt string
	err := s.db.QueryRowContext(ctx, `
SELECT token_hash, username, expires_at, created_at
FROM sessions
WHERE token_hash = ?
`, tokenHash).Scan(&session.TokenHash, &session.Username, &expiresAt, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Session{}, ErrNotFound
		}
		return domain.Session{}, fmt.Errorf("query session: %w", err)
	}

	session.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return domain.Session{}, err
	}
	session.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return domain.Session{}, err
	}
	if !session.ExpiresAt.After(now.UTC()) {
		if err := purgeExpiredSessions(ctx, s.db, now.UTC()); err != nil {
			return domain.Session{}, err
		}
		return domain.Session{}, ErrNotFound
	}
	return session, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s *Store) GetSettings(ctx context.Context) (domain.Settings, error) {
	var settings domain.Settings
	var storedAPIKey string
	var updated string
	err := s.db.QueryRowContext(ctx, `
SELECT syncthing_url, syncthing_api_key, timezone, updated_at
FROM app_settings
WHERE id = 1
`).Scan(&settings.SyncthingURL, &storedAPIKey, &settings.Timezone, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Settings{}, ErrNotFound
		}
		return domain.Settings{}, fmt.Errorf("query app settings: %w", err)
	}
	settings.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return domain.Settings{}, err
	}
	if isEncryptedSecret(storedAPIKey) {
		settings.SyncthingAPIKey, err = decryptSecret(s.encryptionKey, storedAPIKey)
		if err != nil {
			return domain.Settings{}, err
		}
	} else if strings.TrimSpace(storedAPIKey) == "" {
		settings.SyncthingAPIKey = storedAPIKey
	} else {
		return domain.Settings{}, fmt.Errorf("unsupported plaintext Syncthing API key in database; clear and re-save settings with SYNCTIMETHING_ENCRYPTION_KEY configured")
	}
	return settings, nil
}

func (s *Store) SaveSettings(ctx context.Context, settings domain.Settings) error {
	encryptedAPIKey, err := encryptSecret(s.encryptionKey, settings.SyncthingAPIKey)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO app_settings (id, syncthing_url, syncthing_api_key, timezone, updated_at)
VALUES (1, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
syncthing_url = excluded.syncthing_url,
syncthing_api_key = excluded.syncthing_api_key,
timezone = excluded.timezone,
updated_at = excluded.updated_at
`, settings.SyncthingURL, encryptedAPIKey, settings.Timezone, formatTime(s.now().UTC()))
	if err != nil {
		return fmt.Errorf("save app settings: %w", err)
	}
	return nil
}

func (s *Store) ProtectSettings(ctx context.Context) error {
	var storedAPIKey string
	err := s.db.QueryRowContext(ctx, `SELECT syncthing_api_key FROM app_settings WHERE id = 1`).Scan(&storedAPIKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load app settings secret: %w", err)
	}
	if strings.TrimSpace(storedAPIKey) == "" {
		return nil
	}
	if isEncryptedSecret(storedAPIKey) {
		_, err := decryptSecret(s.encryptionKey, storedAPIKey)
		return err
	}
	return fmt.Errorf("unsupported plaintext Syncthing API key in database; clear and re-save settings with SYNCTIMETHING_ENCRYPTION_KEY configured")
}

func (s *Store) ListRules(ctx context.Context) ([]domain.Rule, error) {
	rows, err := queryRulesRows(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("query rules: %w", err)
	}
	defer rows.Close()

	var rules []domain.Rule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rules: %w", err)
	}
	return rules, nil
}

func (s *Store) GetRule(ctx context.Context, id int64) (domain.Rule, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, schedule, action, target_kind, target_id, target_name, enabled, created_at, updated_at, last_evaluated_at
FROM rules
WHERE id = ?
`, id)
	rule, err := scanRule(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Rule{}, ErrNotFound
		}
		return domain.Rule{}, err
	}
	return rule, nil
}

func (s *Store) SaveRule(ctx context.Context, rule domain.Rule) (domain.Rule, error) {
	if err := rule.ValidateBasic(); err != nil {
		return domain.Rule{}, err
	}
	now := s.now().UTC()
	evaluated := formatTime(now)
	if rule.ID == 0 {
		result, err := s.db.ExecContext(ctx, `
INSERT INTO rules (name, schedule, action, target_kind, target_id, target_name, enabled, created_at, updated_at, last_evaluated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, rule.Name, rule.Schedule, string(rule.Action), string(rule.TargetKind), rule.TargetID, rule.TargetName, boolToInt(rule.Enabled), formatTime(now), formatTime(now), evaluated)
		if err != nil {
			return domain.Rule{}, fmt.Errorf("insert rule: %w", err)
		}
		ruleID, err := lastInsertID(result)
		if err != nil {
			return domain.Rule{}, fmt.Errorf("get inserted rule id: %w", err)
		}
		return s.GetRule(ctx, ruleID)
	}

	result, err := s.db.ExecContext(ctx, `
UPDATE rules
SET name = ?, schedule = ?, action = ?, target_kind = ?, target_id = ?, target_name = ?, enabled = ?, updated_at = ?, last_evaluated_at = ?
WHERE id = ?
`, rule.Name, rule.Schedule, string(rule.Action), string(rule.TargetKind), rule.TargetID, rule.TargetName, boolToInt(rule.Enabled), formatTime(now), evaluated, rule.ID)
	if err != nil {
		return domain.Rule{}, fmt.Errorf("update rule: %w", err)
	}
	if affected, _ := rowsAffected(result); affected == 0 {
		return domain.Rule{}, ErrNotFound
	}
	return s.GetRule(ctx, rule.ID)
}

func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete rule: %w", err)
	}
	if affected, _ := rowsAffected(result); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkRuleEvaluated(ctx context.Context, id int64, evaluatedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE rules
SET last_evaluated_at = ?
WHERE id = ?
`, formatTime(evaluatedAt.UTC()), id)
	if err != nil {
		return fmt.Errorf("mark rule evaluated: %w", err)
	}
	return nil
}

func (s *Store) RecordRuleRun(ctx context.Context, run domain.RuleRun) error {
	if err := s.PruneRuleRuns(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO rule_runs (rule_id, rule_name, action, target_kind, target_id, target_name, scheduled_for, executed_at, status, message)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, run.RuleID, run.RuleName, string(run.Action), string(run.TargetKind), run.TargetID, run.TargetName, formatTime(run.ScheduledFor.UTC()), formatTime(run.ExecutedAt.UTC()), run.Status, run.Message)
	if err != nil {
		return fmt.Errorf("record rule run: %w", err)
	}
	return nil
}

func (s *Store) PruneRuleRuns(ctx context.Context) error {
	if s.ruleRunRetention <= 0 {
		return nil
	}
	return pruneRuleRuns(ctx, s.db, s.now().UTC().Add(-s.ruleRunRetention))
}

func (s *Store) ListRecentRuns(ctx context.Context, limit int) ([]domain.RuleRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := queryRecentRunsRows(ctx, s.db, limit)
	if err != nil {
		return nil, fmt.Errorf("query rule runs: %w", err)
	}
	defer rows.Close()

	var runs []domain.RuleRun
	for rows.Next() {
		var run domain.RuleRun
		var action string
		var targetKind string
		var scheduledFor string
		var executedAt string
		if err := rows.Scan(&run.ID, &run.RuleID, &run.RuleName, &action, &targetKind, &run.TargetID, &run.TargetName, &scheduledFor, &executedAt, &run.Status, &run.Message); err != nil {
			return nil, fmt.Errorf("scan rule run: %w", err)
		}
		parsedAction, err := domain.ParseAction(action)
		if err != nil {
			return nil, err
		}
		parsedKind, err := domain.ParseTargetKind(targetKind)
		if err != nil {
			return nil, err
		}
		run.Action = parsedAction
		run.TargetKind = parsedKind
		run.ScheduledFor, err = parseTime(scheduledFor)
		if err != nil {
			return nil, err
		}
		run.ExecutedAt, err = parseTime(executedAt)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rule runs: %w", err)
	}
	return runs, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanRule(scanner scannable) (domain.Rule, error) {
	var rule domain.Rule
	var action string
	var targetKind string
	var enabled int
	var createdAt string
	var updatedAt string
	var lastEvaluated sql.NullString
	if err := scanner.Scan(&rule.ID, &rule.Name, &rule.Schedule, &action, &targetKind, &rule.TargetID, &rule.TargetName, &enabled, &createdAt, &updatedAt, &lastEvaluated); err != nil {
		return domain.Rule{}, err
	}
	parsedAction, err := domain.ParseAction(action)
	if err != nil {
		return domain.Rule{}, err
	}
	parsedKind, err := domain.ParseTargetKind(targetKind)
	if err != nil {
		return domain.Rule{}, err
	}
	rule.Action = parsedAction
	rule.TargetKind = parsedKind
	rule.Enabled = enabled == 1
	rule.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return domain.Rule{}, err
	}
	rule.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return domain.Rule{}, err
	}
	if lastEvaluated.Valid {
		parsed, err := parseTime(lastEvaluated.String)
		if err != nil {
			return domain.Rule{}, err
		}
		rule.LastEvaluatedAt = &parsed
	}
	return rule, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatTime(ts time.Time) string {
	return ts.UTC().Format(timestampLayout)
}

func parseTime(raw string) (time.Time, error) {
	parsed, err := time.Parse(timestampLayout, raw)
	if err == nil {
		return parsed, nil
	}
	parsed, err = time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", raw, err)
	}
	return parsed, nil
}

func isEncryptedSecret(value string) bool {
	return strings.HasPrefix(value, encryptedSecretPrefix)
}

func encryptSecret(key []byte, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	gcm, err := newSecretCipher(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := readSecretRandom(nonce); err != nil {
		return "", fmt.Errorf("read secret nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return encryptedSecretPrefix + base64.RawStdEncoding.EncodeToString(append(nonce, ciphertext...)), nil
}

func decryptSecret(key []byte, encoded string) (string, error) {
	if encoded == "" || !isEncryptedSecret(encoded) {
		return encoded, nil
	}
	gcm, err := newSecretCipher(key)
	if err != nil {
		return "", err
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encoded, encryptedSecretPrefix))
	if err != nil {
		return "", fmt.Errorf("decode encrypted secret: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(payload) < nonceSize {
		return "", fmt.Errorf("decode encrypted secret: malformed ciphertext")
	}
	plaintext, err := gcm.Open(nil, payload[:nonceSize], payload[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt stored secret: %w", err)
	}
	return string(plaintext), nil
}

func newSecretCipher(key []byte) (cipher.AEAD, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("SYNCTIMETHING_ENCRYPTION_KEY is required to store or read a Syncthing API key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize encryption key: %w", err)
	}
	gcm, err := newGCMCipher(block)
	if err != nil {
		return nil, fmt.Errorf("initialize encryption cipher: %w", err)
	}
	return gcm, nil
}
