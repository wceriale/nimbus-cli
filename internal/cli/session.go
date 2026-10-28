package cli

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/wceriale/nimbus-cli/internal/api"
	"github.com/wceriale/nimbus-cli/internal/config"
	"github.com/wceriale/nimbus-cli/internal/output"
)

// session is a logged-in API client for one run.
type session struct {
	client *api.Client
	apiURL string
	auth   string // "login" or "NIMBUS_TOKEN"
}

func (a *App) store() config.Store { return config.Store{Dir: a.ConfigDir} }

// apiURL picks the API to talk to: NIMBUS_API_URL, else the first non-empty
// remembered URL (from a login), else the built-in default.
func (a *App) apiURL(remembered ...string) (string, error) {
	raw := a.Getenv("NIMBUS_API_URL")
	if raw == "" {
		raw = firstNonEmpty(append(remembered, DefaultAPIURL)...)
	}
	if raw == "" {
		return "", output.Errorf("api_url_not_set",
			"Set NIMBUS_API_URL to your Nimbus API, e.g. `export NIMBUS_API_URL=https://api.<your-domain>`.",
			"No Nimbus API URL is configured.")
	}
	if _, err := api.CheckURL(raw); err != nil {
		return "", output.Errorf("invalid_api_url", "Fix NIMBUS_API_URL.", "NIMBUS_API_URL is invalid: %v.", err)
	}
	return strings.TrimRight(raw, "/"), nil
}

func (a *App) client(baseURL string) *api.Client {
	return &api.Client{
		BaseURL:   baseURL,
		HTTP:      a.HTTPClient,
		Version:   Version,
		UserAgent: fmt.Sprintf("nimbus-cli/%s (%s/%s)", Version, runtime.GOOS, runtime.GOARCH),
	}
}

// session returns a client with credentials, or not_logged_in. Stored
// credentials are only used for the API that issued them.
func (a *App) session() (*session, error) {
	store := a.store()
	creds := store.LoadCredentials()
	var credsURL string
	if creds != nil {
		credsURL = creds.APIURL
	}
	base, err := a.apiURL(credsURL)
	if err != nil {
		return nil, err
	}
	c := a.client(base)
	if tok := a.Getenv("NIMBUS_TOKEN"); tok != "" {
		c.Token = staticToken(tok)
		return &session{client: c, apiURL: base, auth: "NIMBUS_TOKEN"}, nil
	}
	if creds == nil || creds.APIURL != base {
		return nil, notLoggedIn(base)
	}
	c.Token = a.refreshingToken(c, store, creds)
	return &session{client: c, apiURL: base, auth: "login"}, nil
}

func notLoggedIn(base string) *output.Error {
	return output.Errorf("not_logged_in", "Run `nimbus login` and show the user the URL it prints.", "Not logged in to %s.", base)
}

// staticToken serves NIMBUS_TOKEN. It can't be refreshed, so a 401 is final.
func staticToken(tok string) func(context.Context, bool) (string, error) {
	return func(_ context.Context, force bool) (string, error) {
		if force {
			return "", output.Errorf("not_logged_in", "Set a valid NIMBUS_TOKEN, or unset it and run `nimbus login`.",
				"The server rejected NIMBUS_TOKEN.")
		}
		return tok, nil
	}
}

// refreshingToken serves the stored access token, refreshing it when it is
// about to expire or the server rejected it. Refresh tokens rotate, so every
// refresh is saved.
func (a *App) refreshingToken(c *api.Client, store config.Store, creds *config.Credentials) func(context.Context, bool) (string, error) {
	return func(ctx context.Context, force bool) (string, error) {
		if !force && a.Now().Before(creds.ExpiresAt.Add(-30*time.Second)) {
			return creds.AccessToken, nil
		}
		if creds.RefreshToken == "" {
			return "", notLoggedIn(creds.APIURL)
		}
		tok, err := c.RefreshToken(ctx, creds.RefreshToken)
		var oe *api.OAuthError
		if errors.As(err, &oe) {
			_ = store.DeleteCredentials()
			return "", notLoggedIn(creds.APIURL)
		}
		if err != nil {
			return "", err
		}
		creds.AccessToken = tok.AccessToken
		if tok.RefreshToken != "" {
			creds.RefreshToken = tok.RefreshToken
		}
		creds.ExpiresAt = a.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
		if err := store.SaveCredentials(creds); err != nil {
			return "", err
		}
		return creds.AccessToken, nil
	}
}
