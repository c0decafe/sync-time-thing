package syncthing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mnm/sync-time-thing/internal/domain"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Client struct {
	baseURL    *url.URL
	apiKey     string
	httpClient HTTPClient
}

type pingResponse struct {
	Ping string `json:"ping"`
}

type deviceConfig struct {
	DeviceID string `json:"deviceID"`
	Name     string `json:"name"`
	Paused   bool   `json:"paused"`
}

type folderConfig struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Paused bool   `json:"paused"`
}

func NewClient(baseURL, apiKey string, httpClient HTTPClient) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("syncthing url is required")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("syncthing api key is required")
	}
	parsedURL, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse syncthing url: %w", err)
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		return nil, fmt.Errorf("syncthing url must be absolute")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: parsedURL, apiKey: apiKey, httpClient: httpClient}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	var response pingResponse
	if err := c.doJSON(ctx, http.MethodGet, "/rest/system/ping", nil, &response); err != nil {
		return err
	}
	if response.Ping == "" {
		return fmt.Errorf("syncthing ping returned an empty response")
	}
	return nil
}

func (c *Client) ListDevices(ctx context.Context) ([]domain.Device, error) {
	var payload []deviceConfig
	if err := c.doJSON(ctx, http.MethodGet, "/rest/config/devices", nil, &payload); err != nil {
		return nil, err
	}
	devices := make([]domain.Device, 0, len(payload))
	for _, item := range payload {
		devices = append(devices, domain.Device{ID: item.DeviceID, Name: item.Name, Paused: item.Paused})
	}
	return devices, nil
}

func (c *Client) ListFolders(ctx context.Context) ([]domain.Folder, error) {
	var payload []folderConfig
	if err := c.doJSON(ctx, http.MethodGet, "/rest/config/folders", nil, &payload); err != nil {
		return nil, err
	}
	folders := make([]domain.Folder, 0, len(payload))
	for _, item := range payload {
		folders = append(folders, domain.Folder{ID: item.ID, Label: item.Label, Paused: item.Paused})
	}
	return folders, nil
}

func (c *Client) Execute(ctx context.Context, rule domain.Rule) error {
	switch rule.TargetKind {
	case domain.TargetGlobal:
		return c.doJSON(ctx, http.MethodPost, "/rest/system/"+string(rule.Action), nil, nil)
	case domain.TargetDevice:
		return c.doJSON(ctx, http.MethodPatch, "/rest/config/devices/"+url.PathEscape(rule.TargetID), map[string]bool{"paused": rule.Action.PausedValue()}, nil)
	case domain.TargetFolder:
		return c.doJSON(ctx, http.MethodPatch, "/rest/config/folders/"+url.PathEscape(rule.TargetID), map[string]bool{"paused": rule.Action.PausedValue()}, nil)
	default:
		return fmt.Errorf("unsupported target kind %q", rule.TargetKind)
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, input any, output any) error {
	request, err := c.newRequest(ctx, method, path, input)
	if err != nil {
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send %s %s: %w", method, path, err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("syncthing api %s %s returned %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(body)))
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, input any) (*http.Request, error) {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("marshal %s %s request: %w", method, path, err)
		}
		body = bytes.NewReader(payload)
	}

	relativeURL, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("parse request path: %w", err)
	}
	requestURL := c.baseURL.ResolveReference(relativeURL)

	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return nil, fmt.Errorf("build %s %s request: %w", method, path, err)
	}
	request.Header.Set("X-API-Key", c.apiKey)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}
