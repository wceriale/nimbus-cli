// Package api is the CLI's client for the Nimbus control-plane API. The
// endpoint list is in DESIGN.md; internal/fakeapi implements the same
// contract for tests.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wceriale/nimbus-cli/internal/output"
)

// ClientID identifies the CLI to the OAuth endpoints. The CLI is a public
// client, so there is no secret.
const ClientID = "nimbus-cli"

const (
	requestTimeout = 60 * time.Second
	uploadTimeout  = 15 * time.Minute
)

type Client struct {
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
	Version   string
	// Token returns the bearer token for /v1 calls. After a 401 it is called
	// again with force set, to get a fresh token for the one retry.
	Token func(ctx context.Context, force bool) (string, error)
}

// CheckURL accepts https URLs, and plain http only for this machine.
func CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%q isn't a URL", raw)
	}
	switch h := u.Hostname(); {
	case u.Scheme == "https":
		return u, nil
	case u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1"):
		return u, nil
	}
	return nil, fmt.Errorf("%q must use https (plain http is only allowed for localhost)", raw)
}

func (c *Client) DeviceCode(ctx context.Context) (*DeviceCode, error) {
	var dc DeviceCode
	if err := c.postForm(ctx, "/oauth/device/code", url.Values{"client_id": {ClientID}}, &dc); err != nil {
		return nil, err
	}
	return &dc, nil
}

func (c *Client) DeviceToken(ctx context.Context, deviceCode string) (*Token, error) {
	return c.token(ctx, url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
		"client_id":   {ClientID},
	})
}

func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (*Token, error) {
	return c.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {ClientID},
	})
}

func (c *Client) token(ctx context.Context, form url.Values) (*Token, error) {
	var t Token
	if err := c.postForm(ctx, "/oauth/token", form, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (c *Client) Me(ctx context.Context) (*Me, error) {
	var me Me
	if err := c.do(ctx, http.MethodGet, "/v1/me", nil, &me); err != nil {
		return nil, err
	}
	return &me, nil
}

func (c *Client) ListApps(ctx context.Context) ([]App, error) {
	var r struct {
		Apps []App `json:"apps"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/apps", nil, &r); err != nil {
		return nil, err
	}
	return r.Apps, nil
}

// EnsureApp creates the app if it doesn't exist yet.
func (c *Client) EnsureApp(ctx context.Context, name string) (*App, error) {
	var app App
	if err := c.do(ctx, http.MethodPut, appPath(name), struct{}{}, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (c *Client) GetApp(ctx context.Context, name string) (*App, error) {
	var app App
	if err := c.do(ctx, http.MethodGet, appPath(name), nil, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (c *Client) CreateUpload(ctx context.Context, name, sha256 string, size int) (*Upload, error) {
	var u Upload
	in := map[string]any{"sha256": sha256, "size_bytes": size}
	if err := c.do(ctx, http.MethodPost, appPath(name)+"/uploads", in, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// PutUpload sends the tarball to the signed storage URL. It deliberately
// doesn't send the API token: the URL itself is the credential.
func (c *Client) PutUpload(ctx context.Context, u *Upload, data []byte) error {
	if _, err := CheckURL(u.UploadURL); err != nil {
		return output.Errorf("unexpected_response", "This is a server problem. Try `nimbus deploy` again.", "The server gave an unusable upload URL: %v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	method := u.UploadMethod
	if method == "" {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, u.UploadURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	for k, v := range u.UploadHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return networkError(req.URL.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return output.Errorf("upload_failed", "Run `nimbus deploy` again.", "Uploading the source failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// CreateVersion builds and deploys an upload. unchanged is true when the
// upload matches the live version, in which case the live version is returned.
func (c *Client) CreateVersion(ctx context.Context, name, uploadID string) (v *Version, unchanged bool, err error) {
	var r struct {
		Version   Version `json:"version"`
		Unchanged bool    `json:"unchanged"`
	}
	if err := c.do(ctx, http.MethodPost, appPath(name)+"/versions", map[string]string{"upload_id": uploadID}, &r); err != nil {
		return nil, false, err
	}
	return &r.Version, r.Unchanged, nil
}

// GetVersion fetches version n, which is a number or "latest".
func (c *Client) GetVersion(ctx context.Context, name, n string) (*Version, error) {
	var v Version
	if err := c.do(ctx, http.MethodGet, appPath(name)+"/versions/"+url.PathEscape(n), nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (c *Client) BuildLog(ctx context.Context, name, n string) (*BuildLog, error) {
	var l BuildLog
	if err := c.do(ctx, http.MethodGet, appPath(name)+"/versions/"+url.PathEscape(n)+"/build-log", nil, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

func (c *Client) Logs(ctx context.Context, name, since string, limit int) (*Logs, error) {
	q := url.Values{"since": {since}, "limit": {strconv.Itoa(limit)}}
	var l Logs
	if err := c.do(ctx, http.MethodGet, appPath(name)+"/logs?"+q.Encode(), nil, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// UpdateShares adds and removes viewers in one all-or-nothing call.
func (c *Client) UpdateShares(ctx context.Context, name string, add, remove []string) (*Shares, error) {
	in := map[string][]string{"add": nonNil(add), "remove": nonNil(remove)}
	var s Shares
	if err := c.do(ctx, http.MethodPatch, appPath(name)+"/shares", in, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// do sends a JSON request to a /v1 endpoint. On a 401 it retries once with a
// freshly refreshed token.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		retry, err := c.attempt(ctx, method, path, body, in != nil, attempt, out)
		if !retry {
			return err
		}
	}
}

func (c *Client) attempt(ctx context.Context, method, path string, body []byte, isJSON bool, attempt int, out any) (retry bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	c.setHeaders(req)
	if isJSON {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != nil {
		tok, err := c.Token(ctx, attempt > 0)
		if err != nil {
			return false, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, networkError(c.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && c.Token != nil {
		return true, nil
	}
	return false, decode(resp, out)
}

// postForm calls an /oauth endpoint. Those take form bodies and answer
// errors as {"error": "<code>"} per RFC 6749, which comes back as *OAuthError.
func (c *Client) postForm(ctx context.Context, path string, form url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return networkError(c.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var oe OAuthError
		if json.Unmarshal(b, &oe) == nil && oe.Code != "" {
			return &oe
		}
		resp.Body = io.NopCloser(bytes.NewReader(b))
	}
	return decode(resp, out)
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Nimbus-CLI-Version", c.Version)
	req.Header.Set("Accept", "application/json")
}

// decode reads a response into out, or turns the error envelope into an
// *output.Error whose code, message and hint go to the agent unchanged.
func decode(resp *http.Response, out any) error {
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return networkError(resp.Request.URL.Host, err)
	}
	if resp.StatusCode/100 == 2 {
		if out == nil || len(bytes.TrimSpace(b)) == 0 {
			return nil
		}
		if err := json.Unmarshal(b, out); err != nil {
			return unexpected(resp.StatusCode, "couldn't parse the response: "+err.Error())
		}
		return nil
	}
	var env struct {
		Error *output.Error `json:"error"`
	}
	if json.Unmarshal(b, &env) == nil && env.Error != nil && env.Error.Code != "" {
		return env.Error
	}
	return unexpected(resp.StatusCode, string(b))
}

func unexpected(status int, body string) error {
	body = strings.TrimSpace(body)
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	return output.Errorf("unexpected_response", "This is likely a server problem. Try again; if it keeps happening, report this message.",
		"The server answered %d: %s", status, body)
}

func networkError(target string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return output.Errorf("network_error", "Try again. If it keeps happening, check the connection.", "%s didn't answer in time.", target)
	}
	return output.Errorf("network_error", "Check the connection and NIMBUS_API_URL.", "Couldn't reach %s: %v", target, err)
}

func appPath(name string) string { return "/v1/apps/" + url.PathEscape(name) }

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
