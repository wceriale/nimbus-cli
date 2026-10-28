package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
		res, err := a.pollLogin(ctx, c, store, pending, wait, true)
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
	res, err := a.pollLogin(ctx, c, store, pending, true, false)
	if errors.Is(err, errLoginExpired) {
		return nil, output.Errorf("login_expired", "Run `nimbus login` again for a new code.", "The login code expired before it was approved.")
	}
	return res, err
}

// pollLogin checks a pending device code once, or until it resolves when
// wait is set. With remind, a code that's still pending is shown again and
// the browser reopened before waiting, because the code came from an earlier
// run whose browser tab may be gone.
func (a *App) pollLogin(ctx context.Context, c *api.Client, store config.Store, p *config.PendingLogin, wait, remind bool) (any, error) {
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
		if remind {
			remind = false
			a.OpenBrowser(p.VerificationURL)
			a.printer().Progress("Open %s and approve the code %s. Waiting…", p.VerificationURL, p.UserCode)
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

func (a *App) logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Log out: revoke the stored login and delete it from this machine",
		Long: `Revoke the stored login on the server, then delete it from this machine,
along with any login that's waiting for approval.

The local login is deleted even if the server can't be reached ("revoked":
false). Running it while logged out succeeds and does nothing. It doesn't
affect NIMBUS_TOKEN.`,
		Example: `  $ nimbus logout
  {"ok":true,"data":{"status":"logged_out","api_url":"https://api.nimbostrat.dev","email":"you@acme.com","revoked":true}}`,
		Args: noArgs,
		RunE: a.run(func(ctx context.Context, _ []string) (any, error) {
			return a.logout(ctx)
		}),
	}
}

type logoutResult struct {
	Status  string `json:"status"` // logged_out
	APIURL  string `json:"api_url,omitempty"`
	Email   string `json:"email,omitempty"`
	Revoked bool   `json:"revoked"` // the server revoked the login
	Next    string `json:"next,omitempty"`
}

func (r *logoutResult) Text() string {
	var s string
	switch {
	case r.APIURL == "":
		s = "Not logged in."
	case r.Revoked:
		s = fmt.Sprintf("Logged out of %s (%s).", r.APIURL, r.Email)
	default:
		s = fmt.Sprintf("Deleted the login for %s (%s) from this machine.", r.APIURL, r.Email)
	}
	if r.Next != "" {
		s += "\n" + r.Next
	}
	return s
}

func (a *App) logout(ctx context.Context) (any, error) {
	store := a.store()
	res := &logoutResult{Status: "logged_out"}
	var notes []string
	if creds := store.LoadCredentials(); creds != nil {
		res.APIURL, res.Email = creds.APIURL, creds.Email
		// Only ever to the API that issued the token, whatever NIMBUS_API_URL says.
		res.Revoked = a.client(creds.APIURL).Revoke(ctx, creds.RefreshToken) == nil
		if !res.Revoked {
			notes = append(notes, "Couldn't reach the server to revoke the login, so it was only deleted from this machine.")
		}
	}
	if err := store.DeleteCredentials(); err != nil {
		return nil, err
	}
	if err := store.DeletePending(); err != nil {
		return nil, err
	}
	if a.Getenv("NIMBUS_TOKEN") != "" {
		notes = append(notes, "NIMBUS_TOKEN is still set, so commands still run as that token. Unset it to log out completely.")
	}
	res.Next = strings.Join(notes, " ")
	return res, nil
}
