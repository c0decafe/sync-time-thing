package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mnm/sync-time-thing/internal/auth"
	"github.com/mnm/sync-time-thing/internal/config"
	"github.com/mnm/sync-time-thing/internal/domain"
	"github.com/mnm/sync-time-thing/internal/scheduler"
	"github.com/mnm/sync-time-thing/internal/store"
	"github.com/mnm/sync-time-thing/internal/syncthing"
	"github.com/mnm/sync-time-thing/internal/web"
)

type ticker interface {
	Chan() <-chan time.Time
	Stop()
}

type realTicker struct {
	inner *time.Ticker
}

func (t realTicker) Chan() <-chan time.Time {
	return t.inner.C
}

func (t realTicker) Stop() {
	t.inner.Stop()
}

type syncthingAdminClient interface {
	web.SyncthingClient
	Execute(context.Context, domain.Rule) error
}

var (
	mkdirAll     = os.MkdirAll
	openStoreDB  = store.Open
	migrateStore = func(ctx context.Context, st *store.Store) error {
		return st.Migrate(ctx)
	}
	ensureStoreSettings = func(ctx context.Context, st *store.Store, timezone string) error {
		return st.EnsureSettings(ctx, timezone)
	}
	protectStoreSettings = func(ctx context.Context, st *store.Store) error {
		return st.ProtectSettings(ctx)
	}
	pruneStoreRuleRuns = func(ctx context.Context, st *store.Store) error {
		return st.PruneRuleRuns(ctx)
	}
	hasStoreAdmin = func(ctx context.Context, st *store.Store) (bool, error) {
		return st.HasAdmin(ctx)
	}
	ensureStoreAdmin = func(ctx context.Context, st *store.Store, username, passwordHash string) error {
		return st.EnsureAdmin(ctx, username, passwordHash)
	}
	newWebServer = func(st web.Store, factory web.ClientFactory, options web.Options) (*web.Server, error) {
		return web.New(st, factory, options)
	}
	newSyncthingClient = func(baseURL, apiKey string, httpClient syncthing.HTTPClient) (syncthingAdminClient, error) {
		return syncthing.NewClient(baseURL, apiKey, httpClient)
	}
)

type Dependencies struct {
	Now           func() time.Time
	HashPassword  func(string) (string, error)
	HTTPClient    *http.Client
	Listen        func(network, address string) (net.Listener, error)
	NewTicker     func(time.Duration) ticker
	SignalContext func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	Stderr        io.Writer
}

type Program struct {
	cfg           config.Config
	store         *store.Store
	handler       http.Handler
	scheduler     *scheduler.Service
	listen        func(network, address string) (net.Listener, error)
	newTicker     func(time.Duration) ticker
	signalContext func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	stderr        io.Writer
}

func New(ctx context.Context, cfg config.Config, deps Dependencies) (*Program, error) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.HashPassword == nil {
		deps.HashPassword = auth.HashPassword
	}
	if deps.Listen == nil {
		deps.Listen = net.Listen
	}
	if deps.NewTicker == nil {
		deps.NewTicker = func(interval time.Duration) ticker {
			return realTicker{inner: time.NewTicker(interval)}
		}
	}
	if deps.SignalContext == nil {
		deps.SignalContext = signal.NotifyContext
	}
	if deps.Stderr == nil {
		deps.Stderr = os.Stderr
	}
	if deps.HTTPClient == nil {
		deps.HTTPClient = http.DefaultClient
	}

	if err := mkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	database, err := openStoreDB(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	dataStore := store.New(
		database,
		deps.Now,
		store.WithEncryptionKey(cfg.EncryptionKey),
		store.WithRuleRunRetention(cfg.RuleRunRetention),
	)
	if err := migrateStore(ctx, dataStore); err != nil {
		_ = dataStore.Close()
		return nil, err
	}
	if err := ensureStoreSettings(ctx, dataStore, cfg.Timezone); err != nil {
		_ = dataStore.Close()
		return nil, err
	}
	if err := protectStoreSettings(ctx, dataStore); err != nil {
		_ = dataStore.Close()
		return nil, err
	}
	if err := pruneStoreRuleRuns(ctx, dataStore); err != nil {
		_ = dataStore.Close()
		return nil, err
	}

	hasAdmin, err := hasStoreAdmin(ctx, dataStore)
	if err != nil {
		_ = dataStore.Close()
		return nil, err
	}
	switch {
	case cfg.AdminPassword != "":
		hash, err := deps.HashPassword(cfg.AdminPassword)
		if err != nil {
			_ = dataStore.Close()
			return nil, err
		}
		if err := ensureStoreAdmin(ctx, dataStore, cfg.AdminUsername, hash); err != nil {
			_ = dataStore.Close()
			return nil, err
		}
	case !hasAdmin:
		_ = dataStore.Close()
		return nil, fmt.Errorf("SYNCTIMETHING_ADMIN_PASSWORD is required on first boot")
	}

	factory := func(settings domain.Settings) (web.SyncthingClient, error) {
		return newSyncthingClient(settings.SyncthingURL, settings.SyncthingAPIKey, deps.HTTPClient)
	}
	server, err := newWebServer(dataStore, factory, web.Options{
		CookieName:    cfg.SessionCookieName,
		SessionTTL:    cfg.SessionTTL,
		SecureCookies: cfg.SecureCookies,
		Now:           deps.Now,
		Entropy:       nil,
	})
	if err != nil {
		_ = dataStore.Close()
		return nil, err
	}
	jobScheduler := scheduler.New(dataStore, scheduler.ExecutorFunc(func(ctx context.Context, settings domain.Settings, rule domain.Rule) error {
		client, err := newSyncthingClient(settings.SyncthingURL, settings.SyncthingAPIKey, deps.HTTPClient)
		if err != nil {
			return err
		}
		return client.Execute(ctx, rule)
	}), deps.Now)

	return &Program{
		cfg:           cfg,
		store:         dataStore,
		handler:       server.Handler(),
		scheduler:     jobScheduler,
		listen:        deps.Listen,
		newTicker:     deps.NewTicker,
		signalContext: deps.SignalContext,
		stderr:        deps.Stderr,
	}, nil
}

func (p *Program) Handler() http.Handler {
	return p.handler
}

func (p *Program) Close() error {
	return p.store.Close()
}

func (p *Program) Serve(ctx context.Context) error {
	defer p.Close()
	ctx, stop := p.signalContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := p.listen("tcp", p.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", p.cfg.ListenAddr, err)
	}
	defer listener.Close()

	httpServer := &http.Server{Handler: p.handler}
	serverErrors := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			serverErrors <- err
		}
		close(serverErrors)
	}()

	if err := p.scheduler.Tick(ctx); err != nil {
		fmt.Fprintln(p.stderr, err)
	}

	ticker := p.newTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return httpServer.Shutdown(shutdownCtx)
		case <-ticker.Chan():
			if err := p.scheduler.Tick(ctx); err != nil {
				fmt.Fprintln(p.stderr, err)
			}
		case err, ok := <-serverErrors:
			if !ok {
				return nil
			}
			return err
		}
	}
}
