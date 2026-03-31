package syncthing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mnm/sync-time-thing/internal/domain"
)

type failingHTTPClient struct{}

func (failingHTTPClient) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("boom")
}

func TestNewClientValidation(t *testing.T) {
	if _, err := NewClient("", "key", nil); err == nil {
		t.Fatal("expected missing url to fail")
	}
	if _, err := NewClient("http://localhost", "", nil); err == nil {
		t.Fatal("expected missing api key to fail")
	}
	if _, err := NewClient("not-a-url", "key", nil); err == nil {
		t.Fatal("expected relative url to fail")
	}
	client, err := NewClient("http://localhost:8384", "key", nil)
	if err != nil || client == nil {
		t.Fatalf("NewClient returned (%v, %v)", client, err)
	}
}

func TestClientPingListAndExecute(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Header.Get("X-API-Key") != "secret" {
			t.Fatalf("missing api key header")
		}
		switch r.URL.Path {
		case "/rest/system/ping":
			_, _ = w.Write([]byte(`{"ping":"pong"}`))
		case "/rest/config/devices":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"deviceID": "device-a", "name": "Laptop", "paused": false}})
		case "/rest/config/folders":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "folder-a", "label": "Docs", "paused": true}})
		case "/rest/system/pause", "/rest/system/resume", "/rest/config/devices/device-a", "/rest/config/folders/folder-a":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
	devices, err := client.ListDevices(context.Background())
	if err != nil || len(devices) != 1 || devices[0].ID != "device-a" {
		t.Fatalf("ListDevices returned (%+v, %v)", devices, err)
	}
	folders, err := client.ListFolders(context.Background())
	if err != nil || len(folders) != 1 || folders[0].ID != "folder-a" {
		t.Fatalf("ListFolders returned (%+v, %v)", folders, err)
	}

	for _, rule := range []domain.Rule{
		{Action: domain.ActionPause, TargetKind: domain.TargetGlobal},
		{Action: domain.ActionResume, TargetKind: domain.TargetGlobal},
		{Action: domain.ActionPause, TargetKind: domain.TargetDevice, TargetID: "device-a"},
		{Action: domain.ActionResume, TargetKind: domain.TargetFolder, TargetID: "folder-a"},
	} {
		if err := client.Execute(context.Background(), rule); err != nil {
			t.Fatalf("Execute returned error for %+v: %v", rule, err)
		}
	}
	if len(requests) != 7 {
		t.Fatalf("expected 7 requests, got %d", len(requests))
	}
}

func TestClientErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/system/ping":
			_, _ = w.Write([]byte(`{"ping":""}`))
		case "/rest/config/devices":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("broken"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`invalid`))
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("unexpected Ping error: %v", err)
	}
	if _, err := client.ListDevices(context.Background()); err == nil || !strings.Contains(err.Error(), "returned 500") {
		t.Fatalf("unexpected ListDevices error: %v", err)
	}
	if _, err := client.ListFolders(context.Background()); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("unexpected ListFolders error: %v", err)
	}
	if err := client.Execute(context.Background(), domain.Rule{Action: domain.ActionPause, TargetKind: domain.TargetKind("wat")}); err == nil {
		t.Fatal("expected unsupported target to fail")
	}

	failingClient, err := NewClient(server.URL, "secret", failingHTTPClient{})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := failingClient.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "send GET") {
		t.Fatalf("unexpected transport error: %v", err)
	}
}

func TestRequestHelpers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.newRequest(context.Background(), http.MethodPost, "://bad", nil); err == nil {
		t.Fatal("expected invalid request path to fail")
	}
	if _, err := client.newRequest(context.Background(), http.MethodPost, "/rest/system/pause", map[string]any{"bad": func() {}}); err == nil {
		t.Fatal("expected invalid JSON payload to fail")
	}
	if err := client.doJSON(context.Background(), http.MethodPost, "/rest/system/pause", nil, nil); err != nil {
		t.Fatalf("doJSON returned error: %v", err)
	}
}

func TestAdditionalClientHelperErrors(t *testing.T) {
	if _, err := NewClient("http://%zz", "secret", nil); err == nil || !strings.Contains(err.Error(), "parse syncthing url") {
		t.Fatalf("unexpected url parse error: %v", err)
	}

	client, err := NewClient("http://localhost:8384", "secret", nil)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.doJSON(context.Background(), "GET\nBAD", "/rest/system/ping", nil, nil); err == nil || !strings.Contains(err.Error(), "build") {
		t.Fatalf("unexpected request build error: %v", err)
	}
}
