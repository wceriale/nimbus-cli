package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/wceriale/nimbus-cli/internal/api"
	"github.com/wceriale/nimbus-cli/internal/config"
	"github.com/wceriale/nimbus-cli/internal/output"
)

func (a *App) loginCmd() *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in with your company account (the only step that needs a human)",
		Long: `Log in with a device code (RFC 8628). This is the only step that needs a human.

In a terminal, nimbus login opens the browser and waits until you approve.

When stdout isn't a terminal (an agent), it returns right away with
"status": "pending", a verification_url and a user_code. Show the user the
URL, and after they approve, run nimbus login again: it returns
"status": "logged_in". Running it while already logged in just returns
"logged_in".`,
		Example: `  # Agent, step 1: get a URL for the user
  $ nimbus login
  {"ok":true,"data":{"status":"pending","api_url":"https://api.nimbostrat.dev","verification_url":"https://auth.nimbostrat.dev/device?code=WDJB-MJHT","user_code":"WDJB-MJHT","expires_in":600,"browser_opened":false,"next":"Show the user verification_url ..."}}

  # Agent, step 2: after the user approves
  $ nimbus login
  {"ok":true,"data":{"status":"logged_in","email":"you@acme.com","org":"acme","api_url":"https://api.nimbostrat.dev","auth":"login"}}`,
		Args: noArgs,
		RunE: a.run(func(ctx context.Context, _ []string) (any, error) {
			return a.login(ctx, wait || a.StdoutIsTTY)
		}),
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for approval instead of returning a pending result (the default in a terminal)")
	return cmd
}

type loginResult struct {
	Status          string `json:"status"` // pending or logged_in
	Email           string `json:"email,omitempty"`
	Org             string `json:"org,omitempty"`
	APIURL          string `json:"api_url"`
	Auth            string `json:"auth,omitempty"`
	VerificationURL string `json:"verification_url,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	ExpiresIn       int    `json:"expires_in,omitempty"`
	BrowserOpened   *bool  `json:"browser_opened,omitempty"`
	Next            string `json:"next,omitempty"`
}

func (r *loginResult) Text() string {
	if r.Status == "pending" {
		return fmt.Sprintf("Open this URL and approve the code %s:\n  %s\nThen run `nimbus login` again.", r.UserCode, r.VerificationURL)
	}
	return fmt.Sprintf("Logged in to %s as %s (%s).", r.APIURL, r.Email, r.Org)
}

// errLoginExpired means the device code ran out; callers start a new one.
var errLoginExpired = errors.New("login code expired")

func (a *App) login(ctx context.Context, wait bool) (any, error) {
	store := a.store()
	creds := store.LoadCredentials()
	pending := store.LoadPending()
	var credsURL, pendingURL string
	if creds != nil {
		credsURL = creds.APIURL
	}
	if pending != nil {
		pendingURL = pending.APIURL
	}
	base, err := a.apiURL(credsURL, pendingURL)
	if err != nil {
		return nil, err
	}
	c := a.client(base)

	if tok := a.Getenv("NIMBUS_TOKEN"); tok != "" {
		c.Token = staticToken(tok)
		me, err := c.Me(ctx)
		if err != nil {
			return nil, err
		}
		return loggedIn(base, me, "NIMBUS_TOKEN"), nil
	}

	if creds != nil && creds.APIURL == base {
		c.Token = a.refreshingToken(c, store, creds)
		me, err := c.Me(ctx)
		if err == nil {
			return loggedIn(base, me, "login"), nil
		}
		if !isCode(err, "not_logged_in") {
			return nil, err
		}
		c.Token = nil
	}

	if pending != nil && pending.APIURL == base && a.Now().Before(pending.ExpiresAt) {
		res, err := a.pollLogin(ctx, c, store, pending, wait)
		if !errors.Is(err, errLoginExpired) {
			return res, err
		}
		// The old code expired: start a new one below.
	}

	dc, err := c.DeviceCode(ctx)
	if err != nil {
		return nil, err
	}
	pending = &config.PendingLogin{
		APIURL:          base,
		DeviceCode:      dc.DeviceCode,
		UserCode:        dc.UserCode,
		VerificationURL: firstNonEmpty(dc.VerificationURIComplete, dc.VerificationURI),
		ExpiresAt:       a.Now().Add(time.Duration(dc.ExpiresIn) * time.Second),
		Interval:        max(dc.Interval, 1),
	}
	if err := store.SavePending(pending); err != nil {
		return nil, err
	}
	opened := a.OpenBrowser(pending.VerificationURL) == nil
	if !wait {
		return a.pendingResult(pending, &opened), nil
	}
	a.printer().Progress("Open %s and approve the code %s. Waiting…", pending.VerificationURL, pending.UserCode)
	res, err := a.pollLogin(ctx, c, store, pending, true)
	if errors.Is(err, errLoginExpired) {
		return nil, output.Errorf("login_expired", "Run `nimbus login` again for a new code.", "The login code expired before it was approved.")
	}
	return res, err
}

// pollLogin checks a pending device code once, or until it resolves when
// wait is set.
func (a *App) pollLogin(ctx context.Context, c *api.Client, store config.Store, p *config.PendingLogin, wait bool) (any, error) {
	for {
		tok, err := c.DeviceToken(ctx, p.DeviceCode)
		if err == nil {
			return a.finishLogin(ctx, c, store, p, tok)
		}
		var oe *api.OAuthError
		if !errors.As(err, &oe) {
			return nil, err
		}
		switch oe.Code {
		case "authorization_pending":
		case "slow_down":
			p.Interval += 5
			_ = store.SavePending(p)
		case "expired_token":
			_ = store.DeletePending()
			return nil, errLoginExpired
		case "access_denied":
			_ = store.DeletePending()
			return nil, output.Errorf("login_denied", "Ask the user what happened, then run `nimbus login` again.",
				"The login was denied in the browser.")
		case "org_not_enabled":
			_ = store.DeletePending()
			return nil, output.Errorf("org_not_enabled", "Tell the user Nimbus is invite-only for now: their org has to be onboarded first.",
				"%s", firstNonEmpty(oe.Description, "This account's org isn't enabled on Nimbus."))
		default:
			_ = store.DeletePending()
			return nil, output.Errorf("login_failed", "Run `nimbus login` again.", "Login failed: %v.", oe)
		}
		if !wait {
			return a.pendingResult(p, nil), nil
		}
		if !a.Now().Before(p.ExpiresAt) {
			_ = store.DeletePending()
			return nil, errLoginExpired
		}
		a.Sleep(time.Duration(p.Interval) * time.Second)
	}
}

// finishLogin saves the tokens first, so a failure fetching the profile
// doesn't lose the login.
func (a *App) finishLogin(ctx context.Context, c *api.Client, store config.Store, p *config.PendingLogin, tok *api.Token) (any, error) {
	creds := &config.Credentials{
		APIURL:       p.APIURL,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    a.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
	}
	if err := store.SaveCredentials(creds); err != nil {
		return nil, err
	}
	_ = store.DeletePending()
	c.Token = a.refreshingToken(c, store, creds)
	me, err := c.Me(ctx)
	if err != nil {
		return nil, err
	}
	creds.Email, creds.Org = me.Email, me.Org.Slug
	if err := store.SaveCredentials(creds); err != nil {
		return nil, err
	}
	return loggedIn(p.APIURL, me, "login"), nil
}

func (a *App) pendingResult(p *config.PendingLogin, browserOpened *bool) *loginResult {
	return &loginResult{
		Status:          "pending",
		APIURL:          p.APIURL,
		VerificationURL: p.VerificationURL,
		UserCode:        p.UserCode,
		ExpiresIn:       max(int(p.ExpiresAt.Sub(a.Now()).Seconds()), 0),
		BrowserOpened:   browserOpened,
		Next: fmt.Sprintf("Show the user verification_url and ask them to approve code %s. "+
			"After they approve, run `nimbus login` again.", p.UserCode),
	}
}

func loggedIn(base string, me *api.Me, auth string) *loginResult {
	return &loginResult{Status: "logged_in", Email: me.Email, Org: me.Org.Slug, APIURL: base, Auth: auth}
}
