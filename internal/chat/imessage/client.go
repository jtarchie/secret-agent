package imessage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// client is a thin wrapper over BlueBubbles Server's REST API. Every request
// carries `?password=<server_password>`; secrets never appear in request
// bodies or logs.
type client struct {
	baseURL  string
	password string
	http     *http.Client
}

func newClient(baseURL, password string, h *http.Client) *client {
	return &client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		password: password,
		http:     h,
	}
}

// buildURL returns baseURL + pathSegment with the auth query parameter
// appended. pathSegment must start with "/".
func (c *client) buildURL(pathSegment string) string {
	return c.baseURL + pathSegment + "?password=" + url.QueryEscape(c.password)
}

// sendText calls POST /api/v1/message/text. chatGUID is the string verbatim
// from data.chats[0].guid on an inbound event (works for DM and group).
func (c *client) sendText(ctx context.Context, chatGUID, tempGUID, body string) error {
	payload := map[string]string{
		"chatGuid": chatGUID,
		"tempGuid": tempGUID,
		"message":  body,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.buildURL("/api/v1/message/text"), bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return nil
}

// downloadAttachment fetches an attachment blob by its GUID and writes it to
// dest. The path is the community-convention endpoint shape; confirm against
// the installed server version if this ever 404s.
func (c *client) downloadAttachment(ctx context.Context, guid, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.buildURL("/api/v1/attachment/"+url.PathEscape(guid)+"/download"), nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	err = os.MkdirAll(filepath.Dir(dest), 0o700)
	if err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("copy %s: %w", dest, err)
	}
	return nil
}
