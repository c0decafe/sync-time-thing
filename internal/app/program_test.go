package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mnm/sync-time-thing/internal/auth"
	"github.com/mnm/sync-time-thing/internal/config"
	"github.com/mnm/sync-time-thing/internal/domain"
	"github.com/mnm/sync-time-thing/internal/store"
	"github.com/mnm/sync-time-thing/internal/syncthing"
	"github.com/mnm/sync-time-thing/internal/web"
)

type fakeTicker struct{ channel chan time.Time }

func (f fakeTicker) Chan() <-chan time.Time { return f.channel }
func (f fakeTicker) Stop()                  {}

type errorListener struct{}

func (errorListener) Accept() (net.Conn, error) { return nil, errors.New("accept") }
func (errorListener) Close() error              { return nil }
func (errorListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0} }

type closedServerListener struct{}

func (closedServerListener) Accept() (net.Conn, error) { return nil, http.ErrServerClosed }
func (closedServerListener) Close() error              { return nil }
func (closedServerListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0} }

type stubSyncthingClient struct {
	pingCalls    int
	listCalls    int
	executeCalls int
	executeErr   error
}

func (s *stubSyncthingClient) Ping(context.Context) error { s.pingCalls++; return nil }
func (s *stubSyncthingClient) ListDevices(context.Context) ([]domain.Device, error) {
	s.listCalls++
	return []domain.Device{{ID: "device-a", Name: "Laptop"}}, nil
}
func (s *stubSyncthingClient) ListFolders(context.Context) ([]domain.Folder, error) {
	s.listCalls++
	return []domain.Folder{{ID: "folder-a", Label: "Docs"}}, nil
}
func (s *stubSyncthingClient) Execute(context.Context, domain.Rule) error {
	s.executeCalls++
	return s.executeErr
}

func testConfig(dir string) config.Config {
	return config.Config{
		ListenAddr:        "127.0.0.1:0",
		DataDir:           dir,
		DBPath:            filepath.Join(dir, "app.db"),
		SessionCookieName: "test-session",
		SessionTTL:        time.Hour,
		AdminUsername:     "admin",
		AdminPassword:     "secret",
		Timezone:          "UTC",
		EncryptionKey:     []byte("0123456789abcdef0123456789abcdef"),
		RuleRunRetention:  90 * 24 * time.Hour,
	}
}

func TestNewRequiresBootstrapPassword(t *testing.T) {
	cfg := testConfig(t.TempDir())
	cfg.AdminPassword = ""
	_, err := New(context.Background(), cfg, Dependencies{Now: time.Now})
	if err == nil || !strings.Contains(err.Error(), "first boot") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewPropagatesHashErrors(t *testing.T) {
	cfg := testConfig(t.TempDir())
	_, err := New(context.Background(), cfg, Dependencies{HashPassword: func(string) (string, error) { return "", errors.New("hash") }})
	if err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestServeAndListenError(t *testing.T) {
	cfg := testConfig(t.TempDir())
	program, err := New(context.Background(), cfg, Dependencies{Now: time.Now})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := program.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	program, err = New(context.Background(), testConfig(t.TempDir()), Dependencies{
		Now:           time.Now,
		Listen:        func(string, string) (net.Listener, error) { return nil, errors.New("listen") },
		SignalContext: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := program.Serve(context.Background()); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("unexpected Serve error: %v", err)
	}
}

func TestServeLifecycle(t *testing.T) {
	cfg := testConfig(t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	defer listener.Close()

	program, err := New(context.Background(), cfg, Dependencies{
		Now:           time.Now,
		Listen:        func(string, string) (net.Listener, error) { return listener, nil },
		NewTicker:     func(time.Duration) ticker { return fakeTicker{channel: make(chan time.Time)} },
		SignalContext: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if program.Handler() == nil {
		t.Fatal("expected handler to be initialized")
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- program.Serve(ctx) }()

	response, err := http.Get("http://" + listener.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz returned error: %v", err)
	}
	_ = response.Body.Close()
	cancel()

	if err := <-errCh; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

func TestTickerAndServerErrorPaths(t *testing.T) {
	rt := realTicker{inner: time.NewTicker(time.Millisecond)}
	<-rt.Chan()
	rt.Stop()

	program, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{
		Now:           time.Now,
		Listen:        func(string, string) (net.Listener, error) { return errorListener{}, nil },
		NewTicker:     func(time.Duration) ticker { return fakeTicker{channel: make(chan time.Time)} },
		SignalContext: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := program.Serve(context.Background()); err == nil || !strings.Contains(err.Error(), "accept") {
		t.Fatalf("unexpected Serve error: %v", err)
	}
}

func TestNewAdditionalBranches(t *testing.T) {
	cfg := testConfig(t.TempDir())

	originalMkdirAll := mkdirAll
	mkdirAll = func(string, os.FileMode) error { return errors.New("mkdir") }
	if _, err := New(context.Background(), cfg, Dependencies{Now: time.Now}); err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Fatalf("unexpected mkdir error: %v", err)
	}
	mkdirAll = originalMkdirAll

	originalOpenStoreDB := openStoreDB
	openStoreDB = func(string) (*sql.DB, error) { return nil, errors.New("open") }
	if _, err := New(context.Background(), cfg, Dependencies{Now: time.Now}); err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("unexpected open error: %v", err)
	}
	openStoreDB = originalOpenStoreDB
}

func TestNewInjectedFailureBranches(t *testing.T) {
	t.Run("migrate", func(t *testing.T) {
		original := migrateStore
		migrateStore = func(context.Context, *store.Store) error { return errors.New("migrate") }
		t.Cleanup(func() { migrateStore = original })

		_, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{Now: time.Now})
		if err == nil || !strings.Contains(err.Error(), "migrate") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("ensure settings", func(t *testing.T) {
		original := ensureStoreSettings
		ensureStoreSettings = func(context.Context, *store.Store, string) error { return errors.New("settings") }
		t.Cleanup(func() { ensureStoreSettings = original })

		_, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{Now: time.Now})
		if err == nil || !strings.Contains(err.Error(), "settings") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("has admin", func(t *testing.T) {
		original := hasStoreAdmin
		hasStoreAdmin = func(context.Context, *store.Store) (bool, error) { return false, errors.New("has-admin") }
		t.Cleanup(func() { hasStoreAdmin = original })

		_, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{Now: time.Now})
		if err == nil || !strings.Contains(err.Error(), "has-admin") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("ensure admin", func(t *testing.T) {
		original := ensureStoreAdmin
		ensureStoreAdmin = func(context.Context, *store.Store, string, string) error { return errors.New("ensure-admin") }
		t.Cleanup(func() { ensureStoreAdmin = original })

		_, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{Now: time.Now})
		if err == nil || !strings.Contains(err.Error(), "ensure-admin") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("new web server", func(t *testing.T) {
		original := newWebServer
		newWebServer = func(web.Store, web.ClientFactory, web.Options) (*web.Server, error) {
			return nil, errors.New("web")
		}
		t.Cleanup(func() { newWebServer = original })

		_, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{Now: time.Now})
		if err == nil || !strings.Contains(err.Error(), "web") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("protect settings", func(t *testing.T) {
		original := protectStoreSettings
		protectStoreSettings = func(context.Context, *store.Store) error { return errors.New("protect") }
		t.Cleanup(func() { protectStoreSettings = original })

		_, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{Now: time.Now})
		if err == nil || !strings.Contains(err.Error(), "protect") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("prune rule runs", func(t *testing.T) {
		original := pruneStoreRuleRuns
		pruneStoreRuleRuns = func(context.Context, *store.Store) error { return errors.New("prune") }
		t.Cleanup(func() { pruneStoreRuleRuns = original })

		_, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{Now: time.Now})
		if err == nil || !strings.Contains(err.Error(), "prune") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestProgramDefaultTickerAndClientClosures(t *testing.T) {
	originalClient := newSyncthingClient
	client := &stubSyncthingClient{}
	newSyncthingClient = func(string, string, syncthing.HTTPClient) (syncthingAdminClient, error) {
		return client, nil
	}
	t.Cleanup(func() { newSyncthingClient = originalClient })

	now := time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)
	program, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	defer func() { _ = program.Close() }()

	ticker := program.newTicker(time.Millisecond)
	defer ticker.Stop()
	<-ticker.Chan()

	if err := program.store.SaveSettings(context.Background(), domain.Settings{
		SyncthingURL:    "http://syncthing:8384",
		SyncthingAPIKey: "secret",
		Timezone:        "UTC",
	}); err != nil {
		t.Fatalf("SaveSettings returned error: %v", err)
	}
	plain, hashed, err := auth.NewSessionToken(strings.NewReader(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatalf("NewSessionToken returned error: %v", err)
	}
	if err := program.store.CreateSession(context.Background(), "admin", hashed, now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	request.AddCookie(&http.Cookie{Name: "test-session", Value: plain})
	response := httptest.NewRecorder()
	program.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || client.listCalls == 0 {
		t.Fatalf("unexpected dashboard response: code=%d listCalls=%d", response.Code, client.listCalls)
	}

	_, err = program.store.SaveRule(context.Background(), domain.Rule{
		Name:       "Pause laptop",
		Schedule:   "0 15 * * *",
		Action:     domain.ActionPause,
		TargetKind: domain.TargetGlobal,
		TargetName: "All devices",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("SaveRule returned error: %v", err)
	}
	if err := program.store.MarkRuleEvaluated(context.Background(), 1, now.Add(-time.Hour)); err != nil {
		t.Fatalf("MarkRuleEvaluated returned error: %v", err)
	}
	if err := program.scheduler.Tick(context.Background()); err != nil {
		t.Fatalf("Tick returned error: %v", err)
	}
	if client.executeCalls == 0 {
		t.Fatal("expected scheduler client to execute at least once")
	}
}

func TestServeLogsSchedulerErrorsAndClosedServerBranch(t *testing.T) {
	now := time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)
	tickCh := make(chan time.Time, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	defer listener.Close()

	var stderr bytes.Buffer
	program, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{
		Now:           func() time.Time { return now },
		Listen:        func(string, string) (net.Listener, error) { return listener, nil },
		NewTicker:     func(time.Duration) ticker { return fakeTicker{channel: tickCh} },
		SignalContext: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
		Stderr:        &stderr,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	_, err = program.store.SaveRule(context.Background(), domain.Rule{
		Name:       "Pause laptop",
		Schedule:   "not a cron",
		Action:     domain.ActionPause,
		TargetKind: domain.TargetGlobal,
		TargetName: "All devices",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("SaveRule returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- program.Serve(ctx) }()
	response, _ := http.Get("http://" + listener.Addr().String() + "/healthz")
	if response != nil {
		_ = response.Body.Close()
	}
	tickCh <- now.Add(time.Minute)
	deadline := time.Now().Add(time.Second)
	for strings.Count(stderr.String(), "rule 1") < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	if count := strings.Count(stderr.String(), "rule 1"); count < 2 {
		t.Fatalf("expected scheduler errors to be logged twice, got %q", stderr.String())
	}

	program, err = New(context.Background(), testConfig(t.TempDir()), Dependencies{
		Now:           time.Now,
		Listen:        func(string, string) (net.Listener, error) { return closedServerListener{}, nil },
		NewTicker:     func(time.Duration) ticker { return fakeTicker{channel: make(chan time.Time)} },
		SignalContext: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := program.Serve(context.Background()); err != nil {
		t.Fatalf("expected closed server branch to return nil, got %v", err)
	}
}

func TestProgramUsesDefaultSyncthingClient(t *testing.T) {
	now := time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/config/devices":
			_, _ = w.Write([]byte(`[{"deviceID":"device-a","name":"Laptop","paused":false}]`))
		case "/rest/config/folders":
			_, _ = w.Write([]byte(`[{"id":"folder-a","label":"Docs","paused":false}]`))
		case "/rest/system/pause":
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	program, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{
		Now:        func() time.Time { return now },
		HTTPClient: api.Client(),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	defer func() { _ = program.Close() }()

	if err := program.store.SaveSettings(context.Background(), domain.Settings{
		SyncthingURL:    api.URL,
		SyncthingAPIKey: "secret",
		Timezone:        "UTC",
	}); err != nil {
		t.Fatalf("SaveSettings returned error: %v", err)
	}
	plain, hashed, err := auth.NewSessionToken(strings.NewReader(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatalf("NewSessionToken returned error: %v", err)
	}
	if err := program.store.CreateSession(context.Background(), "admin", hashed, now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	request.AddCookie(&http.Cookie{Name: "test-session", Value: plain})
	response := httptest.NewRecorder()
	program.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected dashboard status: %d", response.Code)
	}

	rule, err := program.store.SaveRule(context.Background(), domain.Rule{
		Name:       "Pause everything",
		Schedule:   "0 15 * * *",
		Action:     domain.ActionPause,
		TargetKind: domain.TargetGlobal,
		TargetName: "All devices",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("SaveRule returned error: %v", err)
	}
	if err := program.store.MarkRuleEvaluated(context.Background(), rule.ID, now.Add(-time.Hour)); err != nil {
		t.Fatalf("MarkRuleEvaluated returned error: %v", err)
	}
	if err := program.scheduler.Tick(context.Background()); err != nil {
		t.Fatalf("Tick returned error: %v", err)
	}
}

func TestProgramSchedulerClientConstructionError(t *testing.T) {
	originalClient := newSyncthingClient
	newSyncthingClient = func(string, string, syncthing.HTTPClient) (syncthingAdminClient, error) {
		return nil, errors.New("client")
	}
	t.Cleanup(func() { newSyncthingClient = originalClient })

	now := time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)
	program, err := New(context.Background(), testConfig(t.TempDir()), Dependencies{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	defer func() { _ = program.Close() }()

	if err := program.store.SaveSettings(context.Background(), domain.Settings{
		SyncthingURL:    "http://syncthing:8384",
		SyncthingAPIKey: "secret",
		Timezone:        "UTC",
	}); err != nil {
		t.Fatalf("SaveSettings returned error: %v", err)
	}
	rule, err := program.store.SaveRule(context.Background(), domain.Rule{
		Name:       "Pause everything",
		Schedule:   "0 15 * * *",
		Action:     domain.ActionPause,
		TargetKind: domain.TargetGlobal,
		TargetName: "All devices",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("SaveRule returned error: %v", err)
	}
	if err := program.store.MarkRuleEvaluated(context.Background(), rule.ID, now.Add(-time.Hour)); err == nil {
		if err := program.scheduler.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "client") {
			t.Fatalf("unexpected Tick error: %v", err)
		}
	} else {
		t.Fatalf("MarkRuleEvaluated returned error: %v", err)
	}
}
