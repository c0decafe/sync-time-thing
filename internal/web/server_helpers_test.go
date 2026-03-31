package web

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mnm/sync-time-thing/internal/auth"
	"github.com/mnm/sync-time-thing/internal/domain"
)

type stubStore struct {
	admin            domain.AdminUser
	session          domain.Session
	settings         domain.Settings
	rules            []domain.Rule
	runs             []domain.RuleRun
	getAdminErr      error
	createSessionErr error
	getSessionErr    error
	getSettingsErr   error
	saveSettingsErr  error
	listRulesErr     error
	getRuleErr       error
	saveRuleErr      error
	deleteRuleErr    error
	listRunsErr      error
}

func (s *stubStore) GetAdmin(context.Context, string) (domain.AdminUser, error) {
	return s.admin, s.getAdminErr
}

func (s *stubStore) CreateSession(context.Context, string, string, time.Time) error {
	return s.createSessionErr
}

func (s *stubStore) GetSession(context.Context, string, time.Time) (domain.Session, error) {
	return s.session, s.getSessionErr
}

func (s *stubStore) DeleteSession(context.Context, string) error { return nil }
func (s *stubStore) GetSettings(context.Context) (domain.Settings, error) {
	return s.settings, s.getSettingsErr
}
func (s *stubStore) SaveSettings(context.Context, domain.Settings) error { return s.saveSettingsErr }
func (s *stubStore) ListRules(context.Context) ([]domain.Rule, error)    { return s.rules, s.listRulesErr }
func (s *stubStore) GetRule(context.Context, int64) (domain.Rule, error) {
	return domain.Rule{}, s.getRuleErr
}
func (s *stubStore) SaveRule(context.Context, domain.Rule) (domain.Rule, error) {
	return domain.Rule{}, s.saveRuleErr
}
func (s *stubStore) DeleteRule(context.Context, int64) error { return s.deleteRuleErr }
func (s *stubStore) ListRecentRuns(context.Context, int) ([]domain.RuleRun, error) {
	return s.runs, s.listRunsErr
}

type helperClient struct {
	pingErr    error
	devices    []domain.Device
	folders    []domain.Folder
	devicesErr error
	foldersErr error
}

func (c helperClient) Ping(context.Context) error { return c.pingErr }
func (c helperClient) ListDevices(context.Context) ([]domain.Device, error) {
	return c.devices, c.devicesErr
}
func (c helperClient) ListFolders(context.Context) ([]domain.Folder, error) {
	return c.folders, c.foldersErr
}

type entropyErrorReader struct{}

func (entropyErrorReader) Read([]byte) (int, error) { return 0, errors.New("entropy") }

type errorReadCloser struct{}

func (errorReadCloser) Read([]byte) (int, error) { return 0, errors.New("body") }
func (errorReadCloser) Close() error             { return nil }

func newStubServer(t *testing.T, store Store, factory ClientFactory, opts Options) *Server {
	t.Helper()
	server, err := New(store, factory, opts)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return server
}

func TestServerDefaultsAndHelpers(t *testing.T) {
	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	store := &stubStore{
		admin:    domain.AdminUser{Username: "admin", PasswordHash: hash},
		session:  domain.Session{Username: "admin", ExpiresAt: time.Now().Add(time.Hour)},
		settings: domain.Settings{Timezone: "UTC"},
	}
	server := newStubServer(t, store, nil, Options{})
	if server.cookieName == "" || server.sessionTTL == 0 || server.now == nil {
		t.Fatal("expected New to populate default options")
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if username, ok := server.authenticatedUsername(request); ok || username != "" {
		t.Fatal("expected anonymous request to have no username")
	}
	request.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	if username, ok := server.authenticatedUsername(request); !ok || username != "admin" {
		t.Fatalf("unexpected authenticated username: %q (ok=%v)", username, ok)
	}
	store.getSessionErr = errors.New("missing")
	if username, ok := server.authenticatedUsername(request); ok || username != "" {
		t.Fatal("expected invalid session to fail authentication")
	}

	catalog, connectionError := server.loadCatalog(context.Background(), domain.Settings{})
	if len(catalog.Devices) != 0 || connectionError != "" {
		t.Fatalf("unexpected empty catalog response: %+v %q", catalog, connectionError)
	}
	server = newStubServer(t, store, func(domain.Settings) (SyncthingClient, error) { return nil, errors.New("factory") }, Options{Now: time.Now})
	if _, connectionError := server.loadCatalog(context.Background(), domain.Settings{SyncthingURL: "http://x", SyncthingAPIKey: "y"}); !strings.Contains(connectionError, "Could not talk to Syncthing.") {
		t.Fatalf("unexpected factory error: %q", connectionError)
	}
	server = newStubServer(t, store, func(domain.Settings) (SyncthingClient, error) {
		return helperClient{devicesErr: errors.New("devices")}, nil
	}, Options{Now: time.Now})
	if _, connectionError := server.loadCatalog(context.Background(), domain.Settings{SyncthingURL: "http://x", SyncthingAPIKey: "y"}); !strings.Contains(connectionError, "Could not talk to Syncthing.") {
		t.Fatalf("unexpected devices error: %q", connectionError)
	}
	server = newStubServer(t, store, func(domain.Settings) (SyncthingClient, error) {
		return helperClient{devices: []domain.Device{{ID: "d", Name: "Device"}}, foldersErr: errors.New("folders")}, nil
	}, Options{Now: time.Now})
	if _, connectionError := server.loadCatalog(context.Background(), domain.Settings{SyncthingURL: "http://x", SyncthingAPIKey: "y"}); !strings.Contains(connectionError, "Could not talk to Syncthing.") {
		t.Fatalf("unexpected folders error: %q", connectionError)
	}

	if notice := settingsSavedNotice(httptest.NewRequest(http.MethodGet, "/settings?saved=1", nil), domain.Settings{}); notice != "Settings saved." {
		t.Fatalf("unexpected settings notice without syncthing config: %q", notice)
	}
	if notice := settingsSavedNotice(httptest.NewRequest(http.MethodGet, "/settings?saved=1", nil), domain.Settings{SyncthingURL: "http://x", SyncthingAPIKey: "k"}); notice != "Settings saved and Syncthing connection verified." {
		t.Fatalf("unexpected settings notice with syncthing config: %q", notice)
	}
	if notice := settingsSavedNotice(httptest.NewRequest(http.MethodGet, "/settings", nil), domain.Settings{}); notice != "" {
		t.Fatalf("unexpected settings notice without saved query: %q", notice)
	}
	if notice := rulesSavedNotice(httptest.NewRequest(http.MethodGet, "/rules?saved=created", nil)); notice != "Rule created." {
		t.Fatalf("unexpected created notice: %q", notice)
	}
	if notice := rulesSavedNotice(httptest.NewRequest(http.MethodGet, "/rules?saved=updated", nil)); notice != "Rule updated." {
		t.Fatalf("unexpected updated notice: %q", notice)
	}
	if notice := rulesSavedNotice(httptest.NewRequest(http.MethodGet, "/rules?saved=deleted", nil)); notice != "Rule deleted." {
		t.Fatalf("unexpected deleted notice: %q", notice)
	}
	if notice := rulesSavedNotice(httptest.NewRequest(http.MethodGet, "/rules", nil)); notice != "" {
		t.Fatalf("unexpected empty rules notice: %q", notice)
	}
	if message := friendlySyncthingError(errors.New("parse syncthing url: bad")); !strings.Contains(message, "Enter a valid Syncthing URL") {
		t.Fatalf("unexpected parse error message: %q", message)
	}
	if message := friendlySyncthingError(errors.New("401 unauthorized")); !strings.Contains(message, "rejected the API key") {
		t.Fatalf("unexpected auth error message: %q", message)
	}
	if message := friendlySyncthingError(errors.New("dial tcp 127.0.0.1:8384: connect: connection refused")); !strings.Contains(message, "Could not reach Syncthing") {
		t.Fatalf("unexpected dial error message: %q", message)
	}
	if message := friendlySyncthingError(nil); message != "" {
		t.Fatalf("unexpected nil syncthing error message: %q", message)
	}

	views := server.previewRules([]domain.Rule{{Name: "bad", Schedule: "invalid", Action: domain.ActionPause, TargetKind: domain.TargetGlobal}}, "Mars/Olympus")
	if len(views) != 1 || len(views[0].Preview) != 0 {
		t.Fatalf("unexpected preview output: %+v", views)
	}

	form := url.Values{"id": {"bad"}}
	req := httptest.NewRequest(http.MethodPost, "/rules", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatalf("ParseForm returned error: %v", err)
	}
	if _, err := server.ruleFromForm(req, targetCatalog{}, ""); err == nil {
		t.Fatal("expected invalid id to fail")
	}

	server.templates = template.New("broken")
	response := httptest.NewRecorder()
	server.render(response, http.StatusOK, "missing", pageData{})
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected render status: %d", response.Code)
	}
}

func TestWebErrorBranches(t *testing.T) {
	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	baseStore := &stubStore{
		admin:    domain.AdminUser{Username: "admin", PasswordHash: hash},
		session:  domain.Session{Username: "admin", ExpiresAt: time.Now().Add(time.Hour)},
		settings: domain.Settings{Timezone: "UTC", SyncthingURL: "http://syncthing:8384", SyncthingAPIKey: "secret"},
		rules:    []domain.Rule{},
		runs:     []domain.RuleRun{},
	}

	server := newStubServer(t, baseStore, func(domain.Settings) (SyncthingClient, error) {
		return helperClient{}, nil
	}, Options{Now: time.Now, Entropy: entropyErrorReader{}})
	handler := server.Handler()
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"username": {"admin"}, "password": {"secret"}}.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, login)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected login entropy status: %d", response.Code)
	}

	baseStore.createSessionErr = errors.New("store")
	server = newStubServer(t, baseStore, nil, Options{Now: time.Now, Entropy: strings.NewReader(strings.Repeat("a", 32))})
	handler = server.Handler()
	login = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"username": {"admin"}, "password": {"secret"}}.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, login)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected create session status: %d", response.Code)
	}
	baseStore.createSessionErr = nil

	baseStore.getSettingsErr = errors.New("settings")
	server = newStubServer(t, baseStore, nil, Options{Now: time.Now})
	response = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected dashboard settings error status: %d", response.Code)
	}
	baseStore.getSettingsErr = nil

	baseStore.listRulesErr = errors.New("rules")
	server = newStubServer(t, baseStore, nil, Options{Now: time.Now})
	response = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected dashboard rules error status: %d", response.Code)
	}
	baseStore.listRulesErr = nil

	baseStore.listRunsErr = errors.New("runs")
	server = newStubServer(t, baseStore, nil, Options{Now: time.Now})
	response = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected dashboard runs error status: %d", response.Code)
	}
	baseStore.listRunsErr = nil

	server = newStubServer(t, baseStore, func(domain.Settings) (SyncthingClient, error) { return nil, errors.New("factory") }, Options{Now: time.Now})
	response = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(url.Values{"timezone": {"UTC"}, "syncthing_url": {"http://x"}, "syncthing_api_key": {"k"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected settings factory error status: %d", response.Code)
	}

	baseStore.saveSettingsErr = errors.New("save")
	server = newStubServer(t, baseStore, func(domain.Settings) (SyncthingClient, error) { return helperClient{}, nil }, Options{Now: time.Now})
	response = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(url.Values{"timezone": {"UTC"}, "syncthing_url": {"http://x"}, "syncthing_api_key": {"k"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected save settings error status: %d", response.Code)
	}
	baseStore.saveSettingsErr = nil

	baseStore.getRuleErr = errors.New("missing")
	server = newStubServer(t, baseStore, nil, Options{Now: time.Now})
	response = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/rules?edit=1", nil)
	req.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unexpected get rule error status: %d", response.Code)
	}
	baseStore.getRuleErr = nil

	baseStore.saveRuleErr = errors.New("save-rule")
	server = newStubServer(t, baseStore, func(domain.Settings) (SyncthingClient, error) {
		return helperClient{devices: []domain.Device{{ID: "device-a", Name: "Laptop"}}}, nil
	}, Options{Now: time.Now})
	response = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/rules", strings.NewReader(url.Values{"name": {"Rule"}, "schedule": {"0 0 * * *"}, "action": {"pause"}, "target_kind": {"device"}, "target_id": {"device-a"}, "enabled": {"on"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected save rule error status: %d", response.Code)
	}
	baseStore.saveRuleErr = nil

	baseStore.deleteRuleErr = errors.New("delete")
	server = newStubServer(t, baseStore, nil, Options{Now: time.Now})
	response = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/rules/delete", strings.NewReader(url.Values{"id": {"1"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unexpected delete rule error status: %d", response.Code)
	}

	baseStore.getSettingsErr = errors.New("settings")
	server = newStubServer(t, baseStore, nil, Options{Now: time.Now})
	response = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/rules", nil)
	req.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	server.renderRulesPage(response, req, http.StatusOK, domain.Rule{}, false, "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected render rules settings error status: %d", response.Code)
	}
	baseStore.getSettingsErr = nil

	baseStore.listRulesErr = errors.New("rules")
	server = newStubServer(t, baseStore, nil, Options{Now: time.Now})
	response = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/rules", nil)
	req.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	server.renderRulesPage(response, req, http.StatusOK, domain.Rule{}, false, "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected render rules list error status: %d", response.Code)
	}
}

func TestNewTemplateParseError(t *testing.T) {
	originalAssets := assets
	assets = embed.FS{}
	t.Cleanup(func() { assets = originalAssets })

	if _, err := New(&stubStore{}, nil, Options{}); err == nil || !strings.Contains(err.Error(), "parse templates") {
		t.Fatalf("unexpected New error: %v", err)
	}
}

func TestDashboardFormattingAndCatalogSorting(t *testing.T) {
	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	now := time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)
	store := &stubStore{
		admin: domain.AdminUser{Username: "admin", PasswordHash: hash},
		session: domain.Session{
			Username:  "admin",
			ExpiresAt: now.Add(time.Hour),
		},
		settings: domain.Settings{
			Timezone:        "UTC",
			SyncthingURL:    "http://syncthing:8384",
			SyncthingAPIKey: "secret",
		},
		runs: []domain.RuleRun{
			{RuleName: "Zero", Status: "ok", Message: "empty"},
			{RuleName: "Filled", Status: "ok", ScheduledFor: now, ExecutedAt: now, Message: "done"},
		},
	}
	server := newStubServer(t, store, func(domain.Settings) (SyncthingClient, error) {
		return helperClient{
			devices: []domain.Device{{ID: "b", Name: "Zulu"}, {ID: "a", Name: "Alpha"}},
			folders: []domain.Folder{{ID: "b", Label: "Zulu"}, {ID: "a", Label: "Alpha"}},
		}, nil
	}, Options{Now: func() time.Time { return now }})

	catalog, connectionError := server.loadCatalog(context.Background(), store.settings)
	if connectionError != "" {
		t.Fatalf("unexpected catalog error: %q", connectionError)
	}
	if len(catalog.Devices) != 2 || catalog.Devices[0].Name != "Alpha" || catalog.Folders[0].Label != "Alpha" {
		t.Fatalf("expected catalog sorting, got %+v %+v", catalog.Devices, catalog.Folders)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: server.cookieName, Value: "token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "<td>-</td>") || !strings.Contains(body, now.Format("2006-01-02 15:04:05 MST")) {
		t.Fatalf("unexpected dashboard response: code=%d body=%s", response.Code, body)
	}
}

func TestFormParseAndValidationBranches(t *testing.T) {
	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	store := &stubStore{
		admin:    domain.AdminUser{Username: "admin", PasswordHash: hash},
		session:  domain.Session{Username: "admin", ExpiresAt: time.Now().Add(time.Hour)},
		settings: domain.Settings{Timezone: "UTC"},
	}
	server := newStubServer(t, store, func(domain.Settings) (SyncthingClient, error) { return helperClient{}, nil }, Options{Now: time.Now})
	cookie := &http.Cookie{Name: server.cookieName, Value: "token"}

	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Body = errorReadCloser{}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected login parse status: %d", response.Code)
	}

	store.getSettingsErr = errors.New("settings")
	request = httptest.NewRequest(http.MethodGet, "/settings", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected settings get error status: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/settings", nil)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Body = errorReadCloser{}
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected settings parse status: %d", response.Code)
	}
	store.getSettingsErr = nil

	request = httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(url.Values{
		"syncthing_url":     {"http://syncthing:8384"},
		"syncthing_api_key": {"secret"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected missing timezone status: %d", response.Code)
	}

	store.getSettingsErr = errors.New("settings")
	request = httptest.NewRequest(http.MethodPost, "/rules", strings.NewReader(url.Values{
		"name":        {"Rule"},
		"schedule":    {"0 0 * * *"},
		"action":      {"pause"},
		"target_kind": {"global"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected rules settings error status: %d", response.Code)
	}
	store.getSettingsErr = nil

	request = httptest.NewRequest(http.MethodPost, "/rules", nil)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Body = errorReadCloser{}
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected rules parse status: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/rules/delete", nil)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Body = errorReadCloser{}
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected rule delete parse status: %d", response.Code)
	}
}

func TestRuleFromFormAdditionalBranches(t *testing.T) {
	server := newStubServer(t, &stubStore{}, nil, Options{Now: time.Now})

	makeRequest := func(values url.Values) *http.Request {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/rules", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm returned error: %v", err)
		}
		return req
	}

	if _, err := server.ruleFromForm(makeRequest(url.Values{
		"name":        {"Rule"},
		"schedule":    {"0 0 * * *"},
		"action":      {"bad"},
		"target_kind": {"global"},
	}), targetCatalog{}, ""); err == nil {
		t.Fatal("expected invalid action to fail")
	}

	if _, err := server.ruleFromForm(makeRequest(url.Values{
		"name":        {"Rule"},
		"schedule":    {"0 0 * * *"},
		"action":      {"pause"},
		"target_kind": {"bad"},
	}), targetCatalog{}, ""); err == nil {
		t.Fatal("expected invalid target kind to fail")
	}

	if _, err := server.ruleFromForm(makeRequest(url.Values{
		"name":        {"Rule"},
		"schedule":    {"0 0 * * *"},
		"action":      {"pause"},
		"target_kind": {"device"},
		"target_id":   {"device-a"},
	}), targetCatalog{}, "devices unavailable"); err == nil || !strings.Contains(err.Error(), "cannot validate devices") {
		t.Fatalf("unexpected device connection error: %v", err)
	}

	if _, err := server.ruleFromForm(makeRequest(url.Values{
		"name":        {"Rule"},
		"schedule":    {"0 0 * * *"},
		"action":      {"pause"},
		"target_kind": {"folder"},
		"target_id":   {"folder-a"},
	}), targetCatalog{}, "folders unavailable"); err == nil || !strings.Contains(err.Error(), "cannot validate folders") {
		t.Fatalf("unexpected folder connection error: %v", err)
	}

	if _, err := server.ruleFromForm(makeRequest(url.Values{
		"name":        {"Rule"},
		"schedule":    {"0 0 * * *"},
		"action":      {"pause"},
		"target_kind": {"folder"},
		"target_id":   {"missing"},
	}), targetCatalog{folderNames: map[string]string{"folder-a": "Docs"}}, ""); err == nil || !strings.Contains(err.Error(), "unknown folder id") {
		t.Fatalf("unexpected unknown folder error: %v", err)
	}

	if _, err := server.ruleFromForm(makeRequest(url.Values{
		"schedule":    {"0 0 * * *"},
		"action":      {"pause"},
		"target_kind": {"global"},
	}), targetCatalog{}, ""); err == nil || !strings.Contains(err.Error(), "rule name is required") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestPreviewRulesBlankTimezone(t *testing.T) {
	server := newStubServer(t, &stubStore{}, nil, Options{Now: func() time.Time {
		return time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)
	}})
	views := server.previewRules([]domain.Rule{{
		Name:       "Night pause",
		Schedule:   "0 16 * * *",
		Action:     domain.ActionPause,
		TargetKind: domain.TargetGlobal,
		TargetName: "All devices",
	}}, "")
	if len(views) != 1 || len(views[0].Preview) != 3 {
		t.Fatalf("unexpected preview output: %+v", views)
	}
}
