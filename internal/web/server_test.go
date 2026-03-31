package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mnm/sync-time-thing/internal/auth"
	"github.com/mnm/sync-time-thing/internal/domain"
	"github.com/mnm/sync-time-thing/internal/store"
)

type fakeClient struct {
	pingErr error
	devices []domain.Device
	folders []domain.Folder
}

func (f fakeClient) Ping(context.Context) error                           { return f.pingErr }
func (f fakeClient) ListDevices(context.Context) ([]domain.Device, error) { return f.devices, nil }
func (f fakeClient) ListFolders(context.Context) ([]domain.Folder, error) { return f.folders, nil }

func newWebTestServer(t *testing.T, factory ClientFactory) (*Server, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	st := store.New(
		db,
		func() time.Time { return time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC) },
		store.WithEncryptionKey([]byte("0123456789abcdef0123456789abcdef")),
	)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate returned error: %v", err)
	}
	if err := st.EnsureSettings(context.Background(), "UTC"); err != nil {
		t.Fatalf("EnsureSettings returned error: %v", err)
	}
	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if err := st.EnsureAdmin(context.Background(), "admin", hash); err != nil {
		t.Fatalf("EnsureAdmin returned error: %v", err)
	}
	server, err := New(st, factory, Options{
		CookieName: "test-session",
		SessionTTL: time.Hour,
		Now:        func() time.Time { return time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC) },
		Entropy:    strings.NewReader(strings.Repeat("a", 32)),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return server, st
}

func authCookie(t *testing.T, st *store.Store) *http.Cookie {
	t.Helper()
	plain, hashed, err := auth.NewSessionToken(strings.NewReader(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatalf("NewSessionToken returned error: %v", err)
	}
	if err := st.CreateSession(context.Background(), "admin", hashed, time.Date(2026, time.March, 30, 16, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	return &http.Cookie{Name: "test-session", Value: plain}
}

func doRequest(t *testing.T, handler http.Handler, method, target string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body strings.Reader
	requestURL := target
	if form != nil {
		body = *strings.NewReader(form.Encode())
	} else {
		body = *strings.NewReader("")
	}
	req := httptest.NewRequest(method, requestURL, &body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func TestLoginAuthAndLogout(t *testing.T) {
	server, st := newWebTestServer(t, nil)
	handler := server.Handler()

	response := doRequest(t, handler, http.MethodGet, "/login", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Login") {
		t.Fatalf("unexpected GET /login response: code=%d body=%s", response.Code, response.Body.String())
	}

	response = doRequest(t, handler, http.MethodGet, "/", nil)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("unexpected GET / redirect: code=%d location=%s", response.Code, response.Header().Get("Location"))
	}

	response = doRequest(t, handler, http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {"wrong"}})
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected failed login status: %d", response.Code)
	}

	response = doRequest(t, handler, http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {"secret"}})
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard" {
		t.Fatalf("unexpected successful login response: code=%d location=%s", response.Code, response.Header().Get("Location"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected login to set a cookie")
	}

	response = doRequest(t, handler, http.MethodGet, "/dashboard", nil)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("expected anonymous dashboard access to redirect, got %d", response.Code)
	}

	response = doRequest(t, handler, http.MethodGet, "/", nil, cookies[0])
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard" {
		t.Fatalf("unexpected authenticated index redirect: code=%d location=%s", response.Code, response.Header().Get("Location"))
	}

	response = doRequest(t, handler, http.MethodGet, "/dashboard", nil, cookies[0])
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Dashboard") {
		t.Fatalf("unexpected dashboard response: code=%d body=%s", response.Code, response.Body.String())
	}

	response = doRequest(t, handler, http.MethodPost, "/logout", nil, cookies[0])
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("unexpected logout response: code=%d location=%s", response.Code, response.Header().Get("Location"))
	}

	response = doRequest(t, handler, http.MethodGet, "/dashboard", nil, cookies[0])
	if response.Code != http.StatusSeeOther {
		t.Fatalf("expected logged out cookie to redirect, got %d", response.Code)
	}

	response = doRequest(t, handler, http.MethodGet, "/missing", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected missing route to 404, got %d", response.Code)
	}

	response = doRequest(t, handler, http.MethodPost, "/logout", nil, authCookie(t, st))
	if response.Code != http.StatusSeeOther {
		t.Fatalf("unexpected logout with direct auth cookie: %d", response.Code)
	}
}

func TestSettingsRulesAndMethods(t *testing.T) {
	client := fakeClient{
		devices: []domain.Device{{ID: "device-a", Name: "Laptop"}},
		folders: []domain.Folder{{ID: "folder-a", Label: "Docs"}},
	}
	server, st := newWebTestServer(t, func(domain.Settings) (SyncthingClient, error) { return client, nil })
	handler := server.Handler()
	cookie := authCookie(t, st)

	response := doRequest(t, handler, http.MethodGet, "/readyz", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected readyz status: %d", response.Code)
	}
	response = doRequest(t, handler, http.MethodGet, "/healthz", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected healthz status: %d", response.Code)
	}

	response = doRequest(t, handler, http.MethodDelete, "/login", nil)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected login method status: %d", response.Code)
	}
	response = doRequest(t, handler, http.MethodDelete, "/settings", nil, cookie)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected settings method status: %d", response.Code)
	}
	response = doRequest(t, handler, http.MethodDelete, "/rules", nil, cookie)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected rules method status: %d", response.Code)
	}
	response = doRequest(t, handler, http.MethodGet, "/logout", nil, cookie)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected logout method status: %d", response.Code)
	}
	response = doRequest(t, handler, http.MethodGet, "/rules/delete", nil, cookie)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected delete method status: %d", response.Code)
	}

	response = doRequest(t, handler, http.MethodPost, "/settings", url.Values{"timezone": {"Mars/Olympus"}}, cookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected invalid timezone status: %d", response.Code)
	}
	response = doRequest(t, handler, http.MethodPost, "/settings", url.Values{"timezone": {"UTC"}, "syncthing_url": {"http://syncthing:8384"}}, cookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected partial syncthing config status: %d", response.Code)
	}

	server, st = newWebTestServer(t, func(domain.Settings) (SyncthingClient, error) {
		return fakeClient{pingErr: errors.New("unreachable")}, nil
	})
	handler = server.Handler()
	cookie = authCookie(t, st)
	response = doRequest(t, handler, http.MethodPost, "/settings", url.Values{"timezone": {"UTC"}, "syncthing_url": {"http://syncthing:8384"}, "syncthing_api_key": {"secret"}}, cookie)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("unexpected ping failure status: %d", response.Code)
	}

	server, st = newWebTestServer(t, func(domain.Settings) (SyncthingClient, error) { return client, nil })
	handler = server.Handler()
	cookie = authCookie(t, st)
	response = doRequest(t, handler, http.MethodPost, "/settings", url.Values{"timezone": {"UTC"}, "syncthing_url": {"http://syncthing:8384"}, "syncthing_api_key": {"secret"}}, cookie)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/settings?saved=1" {
		t.Fatalf("unexpected successful settings response: code=%d location=%s", response.Code, response.Header().Get("Location"))
	}
	response = doRequest(t, handler, http.MethodGet, "/settings?saved=1", nil, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "http://syncthing:8384") || !strings.Contains(response.Body.String(), "Settings saved and Syncthing connection verified.") {
		t.Fatalf("unexpected settings page response: code=%d body=%s", response.Code, response.Body.String())
	}

	response = doRequest(t, handler, http.MethodPost, "/rules", url.Values{"name": {"Broken"}, "schedule": {"not a cron"}, "action": {"pause"}, "target_kind": {"global"}}, cookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected invalid rule status: %d", response.Code)
	}
	response = doRequest(t, handler, http.MethodPost, "/rules", url.Values{"name": {"Unknown device"}, "schedule": {"0 0 * * *"}, "action": {"pause"}, "target_kind": {"device"}, "target_id": {"missing"}, "enabled": {"on"}}, cookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected unknown target status: %d", response.Code)
	}
	response = doRequest(t, handler, http.MethodPost, "/rules", url.Values{"name": {"Pause laptop"}, "schedule": {"0 0 * * *"}, "action": {"pause"}, "target_kind": {"device"}, "target_id": {"device-a"}, "enabled": {"on"}}, cookie)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/rules?saved=created" {
		t.Fatalf("unexpected create rule response: code=%d location=%s", response.Code, response.Header().Get("Location"))
	}
	response = doRequest(t, handler, http.MethodGet, "/rules?saved=created", nil, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Pause laptop") || !strings.Contains(response.Body.String(), "Rule created.") || !strings.Contains(response.Body.String(), "Delete this rule?") || !strings.Contains(response.Body.String(), "Use standard five-field cron syntax") {
		t.Fatalf("unexpected rules page response: code=%d body=%s", response.Code, response.Body.String())
	}
	response = doRequest(t, handler, http.MethodGet, "/rules?edit=bad", nil, cookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected invalid edit id status: %d", response.Code)
	}
	response = doRequest(t, handler, http.MethodGet, "/rules?edit=1", nil, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Update rule") || !strings.Contains(response.Body.String(), "Cancel") {
		t.Fatalf("unexpected edit page response: code=%d body=%s", response.Code, response.Body.String())
	}
	response = doRequest(t, handler, http.MethodPost, "/rules", url.Values{"id": {"1"}, "name": {"Pause docs"}, "schedule": {"0 1 * * *"}, "action": {"resume"}, "target_kind": {"folder"}, "target_id": {"folder-a"}, "enabled": {"on"}}, cookie)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/rules?saved=updated" {
		t.Fatalf("unexpected update rule response: code=%d location=%s", response.Code, response.Header().Get("Location"))
	}
	response = doRequest(t, handler, http.MethodPost, "/rules/delete", url.Values{"id": {"bad"}}, cookie)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Invalid rule id.") {
		t.Fatalf("unexpected bad delete response: code=%d body=%s", response.Code, response.Body.String())
	}
	response = doRequest(t, handler, http.MethodPost, "/rules/delete", url.Values{"id": {"1"}}, cookie)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/rules?saved=deleted" {
		t.Fatalf("unexpected delete response: code=%d location=%s", response.Code, response.Header().Get("Location"))
	}
}

func TestDashboardCatalogAndReadyzErrors(t *testing.T) {
	server, st := newWebTestServer(t, func(domain.Settings) (SyncthingClient, error) { return nil, errors.New("factory failed") })
	cookie := authCookie(t, st)
	handler := server.Handler()
	if err := st.SaveSettings(context.Background(), domain.Settings{SyncthingURL: "http://syncthing:8384", SyncthingAPIKey: "secret", Timezone: "UTC"}); err != nil {
		t.Fatalf("SaveSettings returned error: %v", err)
	}
	response := doRequest(t, handler, http.MethodGet, "/dashboard", nil, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Could not talk to Syncthing.") {
		t.Fatalf("unexpected dashboard response: code=%d body=%s", response.Code, response.Body.String())
	}

	db, err := store.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	bareStore := store.New(db, time.Now)
	if err := bareStore.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate returned error: %v", err)
	}
	bareServer, err := New(bareStore, nil, Options{Now: time.Now})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	response = doRequest(t, bareServer.Handler(), http.MethodGet, "/readyz", nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected bare readyz status: %d", response.Code)
	}
	_ = bareStore.Close()
}
