// Package fakeapi is an in-memory Nimbus control plane for one user and
// their org. It follows the same API contract as the real server, so the
// CLI's tests run against it, and `go run ./cmd/nimbus-fakeapi` serves it for
// trying the CLI by hand.
//
// Like the real server, the user's org doesn't exist until their first
// deploy: the work workspace of their email's domain, or for a Gmail address
// a personal space. Every new app is private.
//
// Versions move one step through queued → building → deploying → live (or
// failed) each time a client reads them, so clients have to poll.
package fakeapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	maxUpload = 100 << 20
	// Free-plan limits, the same for both kinds of org.
	appLimit   = 5 // apps per org, public ones included
	shareLimit = 5 // email shares per app; public isn't one
	orgID      = 1 // the fake has one org
)

var nameRE = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

// Fake is safe for concurrent use. Set the exported fields before serving;
// change behavior while serving only through methods.
type Fake struct {
	// Email is the user every device login signs in as. Its domain is the
	// account's verified domain and its org's, unless Personal is set.
	Email string
	// Personal makes the account a personal one, like Gmail's, with no
	// verified domain and a personal space u-<local part> as its org. New
	// sets it for @gmail.com addresses.
	Personal bool
	// Member makes the user a member of their workspace, not its owner.
	Member bool
	// DomainNotEnabled refuses approved logins with domain_not_enabled, as
	// for a work domain that isn't enabled during private testing.
	DomainNotEnabled bool
	// AutoApprove approves device codes on their first poll, skipping the browser.
	AutoApprove bool
	// AccessTTL is how long access tokens live.
	AccessTTL time.Duration

	mu              sync.Mutex
	orgExists       bool // created by the first deploy
	externalSharing bool
	rejectCLI       bool
	failNext        *VersionError
	devices         map[string]*device
	access          map[string]session
	refresh         map[string]string
	apps            map[string]*app
	uploads         map[string]*upload
}

// VersionError is how a version fails: a build or startup error.
type VersionError struct {
	Code    string
	Message string
	Hint    string
	LogTail []string
}

type device struct {
	code, userCode   string
	expires          time.Time
	approved, denied bool
}

type session struct {
	email   string
	expires time.Time
	refresh string // the refresh token issued with it
}

type app struct {
	name, owner      string
	shares           map[string]bool
	public           bool
	versions         []*version
	lastUpload       *upload
	created, updated time.Time
}

type version struct {
	number   int
	state    string
	sha      string
	created  time.Time
	finished *time.Time
	fail     *VersionError
}

type upload struct {
	id, app, sha string
	size         int
	data         []byte
}

func New(email string) *Fake {
	return &Fake{
		Email:     email,
		Personal:  strings.HasSuffix(email, "@gmail.com"),
		AccessTTL: time.Hour,
		devices:   map[string]*device{},
		access:    map[string]session{},
		refresh:   map[string]string{},
		apps:      map[string]*app{},
		uploads:   map[string]*upload{},
	}
}

// ApproveAll approves every pending device code, as if the user clicked Approve.
func (f *Fake) ApproveAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devices {
		d.approved = true
	}
}

// ExpireDeviceCodes makes every pending device code expired.
func (f *Fake) ExpireDeviceCodes() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devices {
		d.expires = time.Now().Add(-time.Second)
	}
}

// ExpireAccessTokens makes every access token invalid, forcing a refresh.
func (f *Fake) ExpireAccessTokens() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, s := range f.access {
		s.expires = time.Now().Add(-time.Second)
		f.access[k] = s
	}
}

// SetRejectCLI makes every endpoint answer 426 cli_outdated.
func (f *Fake) SetRejectCLI(reject bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectCLI = reject
}

// SetFailNext makes the next new version fail with e.
func (f *Fake) SetFailNext(e *VersionError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = e
}

// AddApp creates an app in the org, owned by someone, e.g. to test
// name_taken or the app limit.
func (f *Fake) AddApp(name, owner string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	f.orgExists = true
	f.apps[name] = &app{name: name, owner: owner, shares: map[string]bool{}, created: now, updated: now}
}

// UploadedFiles lists the paths in the most recent tarball sent for an app.
func (f *Fake) UploadedFiles(appName string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.apps[appName]
	if !ok || a.lastUpload == nil {
		return nil
	}
	files, _ := tarFiles(a.lastUpload.data)
	return files
}

func (f *Fake) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/device/code", f.deviceCode)
	mux.HandleFunc("POST /oauth/token", f.token)
	mux.HandleFunc("POST /oauth/revoke", f.revoke)
	mux.HandleFunc("GET /device", f.devicePage)
	mux.HandleFunc("POST /device", f.deviceDecide)
	mux.HandleFunc("PUT /_upload/{id}", f.receiveUpload)
	mux.HandleFunc("GET /v1/me", f.authed(f.me))
	mux.HandleFunc("GET /v1/apps", f.authed(f.listApps))
	mux.HandleFunc("PUT /v1/apps/{app}", f.authed(f.ensureApp))
	mux.HandleFunc("GET /v1/apps/{app}", f.authed(f.getApp))
	mux.HandleFunc("POST /v1/apps/{app}/uploads", f.authed(f.createUpload))
	mux.HandleFunc("POST /v1/apps/{app}/versions", f.authed(f.createVersion))
	mux.HandleFunc("GET /v1/apps/{app}/versions/{n}", f.authed(f.getVersion))
	mux.HandleFunc("GET /v1/apps/{app}/versions/{n}/build-log", f.authed(f.buildLog))
	mux.HandleFunc("GET /v1/apps/{app}/logs", f.authed(f.logs))
	mux.HandleFunc("PATCH /v1/apps/{app}/shares", f.authed(f.updateShares))
	mux.HandleFunc("PATCH /v1/orgs/{id}", f.authed(f.updateOrg))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.rejectCLI {
			writeErr(w, http.StatusUpgradeRequired, "cli_outdated", "This nimbus CLI is too old for the server.",
				"Re-run the installer: curl -fsSL https://raw.githubusercontent.com/wceriale/nimbus-cli/main/install.sh | sh", nil)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// OAuth device flow (RFC 8628) and refresh.

func (f *Fake) deviceCode(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("client_id") != "nimbus-cli" {
		oauthErr(w, "invalid_client")
		return
	}
	d := &device{code: randID("dc"), userCode: userCode(), expires: time.Now().Add(10 * time.Minute)}
	f.devices[d.code] = d
	base := "http://" + r.Host
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               d.code,
		"user_code":                 d.userCode,
		"verification_uri":          base + "/device",
		"verification_uri_complete": base + "/device?code=" + d.userCode,
		"expires_in":                600,
		"interval":                  5,
	})
}

func (f *Fake) token(w http.ResponseWriter, r *http.Request) {
	switch r.FormValue("grant_type") {
	case "urn:ietf:params:oauth:grant-type:device_code":
		d := f.devices[r.FormValue("device_code")]
		switch {
		case d == nil:
			oauthErr(w, "invalid_grant")
		case time.Now().After(d.expires):
			delete(f.devices, d.code)
			oauthErr(w, "expired_token")
		case d.denied:
			delete(f.devices, d.code)
			oauthErr(w, "access_denied")
		case !d.approved && !f.AutoApprove:
			oauthErr(w, "authorization_pending")
		case f.DomainNotEnabled:
			delete(f.devices, d.code)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain_not_enabled",
				"error_description": "nimby is in private testing, and this account's domain isn't enabled yet. Ask to have it enabled."})
		default:
			delete(f.devices, d.code)
			f.issue(w, f.Email)
		}
	case "refresh_token":
		rt := r.FormValue("refresh_token")
		email, ok := f.refresh[rt]
		if !ok {
			oauthErr(w, "invalid_grant")
			return
		}
		delete(f.refresh, rt)
		f.issue(w, email)
	default:
		oauthErr(w, "unsupported_grant_type")
	}
}

// revoke forgets a refresh token and the access tokens issued with it. An
// unknown token gets 200 too (RFC 7009).
func (f *Fake) revoke(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("client_id") != "nimbus-cli" {
		oauthErr(w, "invalid_client")
		return
	}
	rt := r.FormValue("token")
	delete(f.refresh, rt)
	for at, s := range f.access {
		if s.refresh == rt {
			delete(f.access, at)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (f *Fake) issue(w http.ResponseWriter, email string) {
	at, rt := randID("at"), randID("rt")
	f.access[at] = session{email: email, expires: time.Now().Add(f.AccessTTL), refresh: rt}
	f.refresh[rt] = email
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  at,
		"token_type":    "Bearer",
		"expires_in":    int(f.AccessTTL / time.Second),
		"refresh_token": rt,
	})
}

const devicePageHTML = `<!doctype html>
<meta charset="utf-8"><title>Nimbus login (fake server)</title>
<h1>Approve nimbus login</h1>
<p>This fake server signs you in as <b>%s</b>.</p>
<form method="post">
  <input name="code" value="%s" placeholder="ABCD-EFGH">
  <button name="decision" value="approve">Approve</button>
  <button name="decision" value="deny">Deny</button>
</form>`

func (f *Fake) devicePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, devicePageHTML, html.EscapeString(f.Email), html.EscapeString(r.URL.Query().Get("code")))
}

func (f *Fake) deviceDecide(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(strings.TrimSpace(r.FormValue("code")))
	for _, d := range f.devices {
		if d.userCode == code {
			if r.FormValue("decision") == "deny" {
				d.denied = true
			} else {
				d.approved = true
			}
			fmt.Fprintln(w, "Done. Go back to your terminal or agent.")
			return
		}
	}
	http.Error(w, "Unknown or expired code.", http.StatusNotFound)
}

// /v1 endpoints.

func (f *Fake) authed(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		s, found := f.access[tok]
		if !ok || !found || time.Now().After(s.expires) {
			writeErr(w, http.StatusUnauthorized, "not_logged_in", "Your login has expired.", "Run `nimbus login`.", nil)
			return
		}
		h(w, r, s.email)
	}
}

func (f *Fake) me(w http.ResponseWriter, _ *http.Request, email string) {
	var domain, org any
	if !f.Personal {
		domain = f.domain()
	}
	if f.orgExists {
		org = f.orgJSON()
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": email, "domain": domain, "org": org, "idp": "google"})
}

// orgJSON is the Org object of the user's org.
func (f *Fake) orgJSON() map[string]any {
	domain, name, role := any(f.domain()), f.domain(), "owner"
	if f.Personal {
		domain, name = nil, f.Email
	}
	if f.Member {
		role = "member"
	}
	return map[string]any{
		"id": orgID, "key": f.orgKey(), "slug": f.orgSlug(), "kind": f.orgKind(), "domain": domain, "name": name, "role": role,
		"external_sharing": f.externalSharing || f.Personal, "plan": "free", "apps_used": len(f.apps), "apps_limit": appLimit,
	}
}

func (f *Fake) listApps(w http.ResponseWriter, _ *http.Request, email string) {
	var owned []*app
	for _, a := range f.apps {
		if a.owner == email {
			owned = append(owned, a)
		}
	}
	sort.Slice(owned, func(i, j int) bool {
		if !owned[i].updated.Equal(owned[j].updated) {
			return owned[i].updated.After(owned[j].updated)
		}
		return owned[i].name < owned[j].name
	})
	list := []map[string]any{}
	for _, a := range owned {
		list = append(list, f.appJSON(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": list})
}

// ensureApp is the deploy handler: it checks the org pinned in nimbus.yaml,
// creates the user's org on their first deploy, then the app if it's new.
func (f *Fake) ensureApp(w http.ResponseWriter, r *http.Request, email string) {
	name := r.PathValue("app")
	if len(name) < 3 || len(name) > 40 || !nameRE.MatchString(name) {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_name", fmt.Sprintf("%q isn't a valid app name.", name),
			"Use 3–40 lowercase letters, digits and hyphens, starting with a letter.", nil)
		return
	}
	var in struct {
		Org string `json:"org"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, &in) {
		return
	}
	if in.Org != "" && !f.pinMatches(in.Org) {
		f.wrongAccount(w, in.Org, email)
		return
	}
	f.orgExists = true
	if a, ok := f.apps[name]; ok {
		if a.owner != email {
			writeErr(w, http.StatusConflict, "name_taken", fmt.Sprintf("Someone else in your org already has an app called %q.", name),
				"Change `name` in nimbus.yaml and run `nimbus deploy` again.", nil)
			return
		}
		writeJSON(w, http.StatusOK, f.appJSON(a))
		return
	}
	if len(f.apps) >= appLimit {
		writeErr(w, http.StatusForbidden, "app_limit",
			fmt.Sprintf("This workspace has %d apps, the free limit. Delete one with `nimbus destroy <app>` or upgrade.", appLimit),
			"Delete one with `nimbus destroy <app>` or upgrade. `nimbus list` shows your apps, and `nimbus deploy --app <name>` redeploys one instead.",
			map[string]any{"apps_used": len(f.apps), "apps_limit": appLimit})
		return
	}
	now := time.Now()
	a := &app{name: name, owner: email, shares: map[string]bool{}, created: now, updated: now}
	f.apps[name] = a
	writeJSON(w, http.StatusCreated, f.appJSON(a))
}

func (f *Fake) getApp(w http.ResponseWriter, r *http.Request, email string) {
	if a := f.ownedApp(w, r, email); a != nil {
		writeJSON(w, http.StatusOK, f.appJSON(a))
	}
}

func (f *Fake) createUpload(w http.ResponseWriter, r *http.Request, email string) {
	a := f.ownedApp(w, r, email)
	if a == nil {
		return
	}
	var in struct {
		SHA256 string `json:"sha256"`
		Size   int    `json:"size_bytes"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Size > maxUpload {
		writeErr(w, http.StatusRequestEntityTooLarge, "upload_too_large", "The upload is over 100 MB.",
			"Add paths the app doesn't need to .gitignore.", nil)
		return
	}
	for _, u := range f.uploads {
		if u.app == a.name && u.sha == in.SHA256 && u.data != nil {
			writeJSON(w, http.StatusOK, map[string]any{"upload_id": u.id, "already_uploaded": true})
			return
		}
	}
	u := &upload{id: randID("upl"), app: a.name, sha: in.SHA256, size: in.Size}
	f.uploads[u.id] = u
	writeJSON(w, http.StatusCreated, map[string]any{
		"upload_id":        u.id,
		"already_uploaded": false,
		"upload_url":       "http://" + r.Host + "/_upload/" + u.id,
		"upload_method":    "PUT",
		"upload_headers":   map[string]string{"Content-Type": "application/gzip"},
		"expires_at":       time.Now().Add(15 * time.Minute),
	})
}

// receiveUpload stands in for the signed storage URL. Like a real one, it
// must not be sent the API token.
func (f *Fake) receiveUpload(w http.ResponseWriter, r *http.Request) {
	u, ok := f.uploads[r.PathValue("id")]
	if !ok {
		http.Error(w, "unknown upload", http.StatusNotFound)
		return
	}
	if r.Header.Get("Authorization") != "" {
		http.Error(w, "signed upload URLs must not receive the API token", http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxUpload+1))
	sum := sha256.Sum256(data)
	if err != nil || len(data) != u.size || hex.EncodeToString(sum[:]) != u.sha {
		http.Error(w, "body doesn't match the declared sha256 and size", http.StatusBadRequest)
		return
	}
	u.data = data
	if a := f.apps[u.app]; a != nil {
		a.lastUpload = u
	}
	w.WriteHeader(http.StatusOK)
}

func (f *Fake) createVersion(w http.ResponseWriter, r *http.Request, email string) {
	a := f.ownedApp(w, r, email)
	if a == nil {
		return
	}
	var in struct {
		UploadID string `json:"upload_id"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	u, ok := f.uploads[in.UploadID]
	if !ok || u.app != a.name || u.data == nil {
		writeErr(w, http.StatusNotFound, "upload_not_found", "That upload doesn't exist or was never sent.", "Run `nimbus deploy` again.", nil)
		return
	}
	files, err := tarFiles(u.data)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_upload", "The upload isn't a valid .tar.gz: "+err.Error(), "Run `nimbus deploy` again.", nil)
		return
	}
	if !slices.Contains(files, "nimbus.yaml") {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_manifest", "The upload has no nimbus.yaml.", "Run `nimbus deploy` from the app's directory.", nil)
		return
	}
	if pinned := uploadedOrg(u.data); pinned != "" && !f.pinMatches(pinned) {
		f.wrongAccount(w, pinned, email)
		return
	}
	if live := a.live(); live != nil && live.sha == u.sha {
		writeJSON(w, http.StatusOK, map[string]any{"version": versionJSON(live), "unchanged": true})
		return
	}
	v := &version{number: len(a.versions) + 1, state: "queued", sha: u.sha, created: time.Now(), fail: f.failNext}
	f.failNext = nil
	a.versions = append(a.versions, v)
	a.updated = time.Now()
	writeJSON(w, http.StatusAccepted, map[string]any{"version": versionJSON(v), "unchanged": false})
}

func (f *Fake) getVersion(w http.ResponseWriter, r *http.Request, email string) {
	a := f.ownedApp(w, r, email)
	if a == nil {
		return
	}
	if v := findVersion(w, r, a); v != nil {
		advance(a, v)
		writeJSON(w, http.StatusOK, versionJSON(v))
	}
}

func (f *Fake) buildLog(w http.ResponseWriter, r *http.Request, email string) {
	a := f.ownedApp(w, r, email)
	if a == nil {
		return
	}
	v := findVersion(w, r, a)
	if v == nil {
		return
	}
	lines := []string{"Detecting runtime…", "Building image…", "Pushing image…"}
	if v.state == "failed" {
		lines = append(lines, v.fail.LogTail...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v.number, "lines": lines, "truncated": false})
}

func (f *Fake) logs(w http.ResponseWriter, r *http.Request, email string) {
	a := f.ownedApp(w, r, email)
	if a == nil {
		return
	}
	q := r.URL.Query()
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 1000 {
			writeErr(w, http.StatusUnprocessableEntity, "invalid_request", "limit must be between 1 and 1000.", "", nil)
			return
		}
	}
	if s := q.Get("since"); s != "" {
		_, derr := time.ParseDuration(s)
		_, terr := time.Parse(time.RFC3339, s)
		if derr != nil && terr != nil {
			writeErr(w, http.StatusUnprocessableEntity, "invalid_request", "since must be a duration like 15m or 1h, or an RFC 3339 time.",
				"Pass e.g. `--since 1h`.", nil)
			return
		}
	}
	entries := []map[string]any{}
	if v := a.live(); v != nil {
		entries = append(entries, map[string]any{"time": *v.finished, "severity": "INFO", "message": "Listening on 0.0.0.0:8080", "version": v.number})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "truncated": false})
}

func (f *Fake) updateShares(w http.ResponseWriter, r *http.Request, email string) {
	a := f.ownedApp(w, r, email)
	if a == nil {
		return
	}
	var in struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
		Public *bool    `json:"public"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	norm := func(list []string) []string {
		out := make([]string, len(list))
		for i, e := range list {
			out[i] = strings.ToLower(strings.TrimSpace(e))
		}
		return out
	}
	add, remove := norm(in.Add), norm(in.Remove)
	var invalid, outside []string
	for _, e := range append(slices.Clone(add), remove...) {
		local, domain, ok := strings.Cut(e, "@")
		if !ok || local == "" || !strings.Contains(domain, ".") {
			invalid = append(invalid, e)
		}
	}
	if len(invalid) > 0 {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_email", "These aren't email addresses: "+strings.Join(invalid, ", ")+".",
			"Pass full addresses, e.g. `nimbus share lena@acme.com`.", map[string]any{"emails": invalid})
		return
	}
	for _, e := range add {
		if !f.Personal && !f.externalSharing && !strings.HasSuffix(e, "@"+f.domain()) {
			outside = append(outside, e)
		}
	}
	if len(outside) > 0 {
		writeErr(w, http.StatusUnprocessableEntity, "external_sharing_disabled",
			fmt.Sprintf("The %s workspace only shares inside the domain, and these addresses are outside it: %s.", f.domain(), strings.Join(outside, ", ")),
			"Share with @"+f.domain()+" addresses, or the workspace owner can enable external sharing with `nimbus org external-sharing on`.",
			map[string]any{"emails": outside})
		return
	}
	if slices.Contains(remove, a.owner) {
		writeErr(w, http.StatusUnprocessableEntity, "cannot_unshare_owner", "The owner always has access.", "Leave the owner out of `nimbus unshare`.", nil)
		return
	}
	after := maps.Clone(a.shares)
	for _, e := range add {
		if e != a.owner {
			after[e] = true
		}
	}
	for _, e := range remove {
		delete(after, e)
	}
	if len(after) > shareLimit && len(after) > len(a.shares) {
		writeErr(w, http.StatusForbidden, "share_limit",
			fmt.Sprintf("An app can be shared with %d people on the free plan, and this one has %d.", shareLimit, len(a.shares)),
			"Remove someone with `nimbus unshare <email>`, or use `nimbus share --public`, which doesn't count.",
			map[string]any{"shares_used": len(a.shares), "shares_limit": shareLimit})
		return
	}
	for _, e := range add {
		if e != a.owner {
			a.shares[e] = true
		}
	}
	for _, e := range remove {
		delete(a.shares, e)
	}
	if in.Public != nil {
		a.public = *in.Public
	}
	a.updated = time.Now()
	writeJSON(w, http.StatusOK, map[string]any{"app": a.name, "url": f.appURL(a), "shared_with": a.sharedWith(), "public": a.public,
		"visibility": a.visibility(), "shares_used": len(a.shares), "shares_limit": shareLimit})
}

// updateOrg is PATCH /v1/orgs/{id}: external sharing, owner of a work org only.
// Turning it off deletes the shares outside the domain.
func (f *Fake) updateOrg(w http.ResponseWriter, r *http.Request, _ string) {
	id := r.PathValue("id")
	if !f.orgExists || id != strconv.Itoa(orgID) && id != f.orgSlug() {
		writeErr(w, http.StatusNotFound, "org_not_found", fmt.Sprintf("No workspace %q.", id), "Run `nimbus whoami` to see your workspace.", nil)
		return
	}
	var in struct {
		ExternalSharing *bool `json:"external_sharing"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	switch {
	case in.ExternalSharing == nil:
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", "The request has no external_sharing.", `Send {"external_sharing": true} or false.`, nil)
		return
	case f.Personal:
		writeErr(w, http.StatusUnprocessableEntity, "org_is_personal", "External sharing is a workspace setting; a personal space always shares outside.",
			"Personal apps can already be shared with any email address, e.g. `nimbus share friend@gmail.com`.", nil)
		return
	case f.Member:
		writeErr(w, http.StatusForbidden, "not_org_owner", fmt.Sprintf("Only the owner of the %s workspace can change external sharing.", f.domain()),
			"Ask the workspace owner to run `nimbus org external-sharing`.", nil)
		return
	}
	f.externalSharing = *in.ExternalSharing
	removed := 0
	if !f.externalSharing {
		for _, a := range f.apps {
			for e := range a.shares {
				if !strings.HasSuffix(e, "@"+f.domain()) {
					delete(a.shares, e)
					removed++
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"org": f.orgJSON(), "removed_shares": removed})
}

// pinMatches reports whether nimbus.yaml's org: names the user's org. Before
// the first deploy, a work account's org is its domain, and a personal
// space, which doesn't exist yet, matches no pin.
func (f *Fake) pinMatches(pinned string) bool {
	key := f.orgKey()
	if !f.orgExists && f.Personal {
		key = ""
	}
	return strings.EqualFold(strings.TrimSpace(pinned), key)
}

func (f *Fake) wrongAccount(w http.ResponseWriter, pinned, email string) {
	mine, accountOrg := f.orgKey(), f.orgKey()
	if !f.orgExists && f.Personal {
		mine, accountOrg = "personal", ""
	}
	writeErr(w, http.StatusConflict, "wrong_account",
		fmt.Sprintf("This app belongs to %s. You're signed in as %s (%s).", pinned, email, mine),
		"Run `nimbus login` to switch accounts, or remove `org:` from nimbus.yaml to deploy a separate copy here.",
		map[string]any{"app_org": pinned, "account_org": accountOrg, "email": email})
}

// uploadedOrg is the org: in a tarball's nimbus.yaml, or "".
func uploadedOrg(data []byte) string {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return ""
		}
		if h.Name != "nimbus.yaml" {
			continue
		}
		var m struct {
			Org string `yaml:"org"`
		}
		b, _ := io.ReadAll(tr)
		_ = yaml.Unmarshal(b, &m)
		return strings.ToLower(strings.TrimSpace(m.Org))
	}
}

// ownedApp finds an app the caller owns. Like the real server, it answers
// 404 for apps the caller can't manage, so names don't leak.
func (f *Fake) ownedApp(w http.ResponseWriter, r *http.Request, email string) *app {
	name := r.PathValue("app")
	a, ok := f.apps[name]
	if !ok || a.owner != email {
		writeErr(w, http.StatusNotFound, "app_not_found", fmt.Sprintf("No app called %q.", name), "Run `nimbus list` to see your apps.", nil)
		return nil
	}
	return a
}

func findVersion(w http.ResponseWriter, r *http.Request, a *app) *version {
	n := r.PathValue("n")
	i := len(a.versions)
	if n != "latest" {
		var err error
		if i, err = strconv.Atoi(n); err != nil {
			i = -1
		}
	}
	if i < 1 || i > len(a.versions) {
		writeErr(w, http.StatusNotFound, "version_not_found", fmt.Sprintf("App %q has no version %s.", a.name, n),
			"Run `nimbus deploy` to create one.", nil)
		return nil
	}
	return a.versions[i-1]
}

// advance moves a version one step through its lifecycle.
func advance(a *app, v *version) {
	switch v.state {
	case "queued":
		v.state = "building"
	case "building":
		v.state = "deploying"
	case "deploying":
		now := time.Now()
		v.finished, a.updated = &now, now
		if v.fail != nil {
			v.state = "failed"
			return
		}
		if old := a.live(); old != nil {
			old.state = "retired"
		}
		v.state = "live"
	}
}

func (a *app) live() *version {
	for i := len(a.versions) - 1; i >= 0; i-- {
		if a.versions[i].state == "live" {
			return a.versions[i]
		}
	}
	return nil
}

func (a *app) state() string {
	if len(a.versions) == 0 {
		return "new"
	}
	switch a.versions[len(a.versions)-1].state {
	case "queued", "building", "deploying":
		return "building"
	case "failed":
		return "failed"
	}
	return "live"
}

func (a *app) visibility() string {
	switch {
	case a.public:
		return "public"
	case len(a.shares) > 0:
		return "shared"
	}
	return "private"
}

// sharedWith lists everyone with access, owner first.
func (a *app) sharedWith() []string {
	var others []string
	for e := range a.shares {
		others = append(others, e)
	}
	sort.Strings(others)
	return append([]string{a.owner}, others...)
}

func (f *Fake) appJSON(a *app) map[string]any {
	var live, latest any
	if v := a.live(); v != nil {
		live = v.number
	}
	if n := len(a.versions); n > 0 {
		latest = n
	}
	return map[string]any{
		"name":           a.name,
		"url":            f.appURL(a),
		"owner":          a.owner,
		"state":          a.state(),
		"live_version":   live,
		"latest_version": latest,
		"shared_with":    a.sharedWith(),
		"public":         a.public,
		"org":            f.orgKey(),
		"org_kind":       f.orgKind(),
		"visibility":     a.visibility(),
		"shares_used":    len(a.shares),
		"shares_limit":   shareLimit,
		"apps_used":      len(f.apps),
		"apps_limit":     appLimit,
		"created_at":     a.created,
		"updated_at":     a.updated,
	}
}

func versionJSON(v *version) map[string]any {
	m := map[string]any{"number": v.number, "state": v.state, "source_sha256": v.sha, "created_at": v.created}
	if v.finished != nil {
		m["finished_at"] = *v.finished
	}
	if v.state == "failed" {
		m["error"] = map[string]string{"code": v.fail.Code, "message": v.fail.Message, "hint": v.fail.Hint}
		m["log_tail"] = v.fail.LogTail
	}
	return m
}

func (f *Fake) appURL(a *app) string {
	return fmt.Sprintf("https://%s.%s.nimbusapps.example", a.name, f.orgSlug())
}

func (f *Fake) domain() string {
	_, d, _ := strings.Cut(strings.ToLower(f.Email), "@")
	return d
}

// orgSlug is the org's part of app URLs: the domain minus its TLD for a
// workspace (acme.com → acme), and u- with the email's local part, dots
// dropped, for a personal space (j.doe@gmail.com → u-jdoe).
func (f *Fake) orgSlug() string {
	if f.Personal {
		local, _, _ := strings.Cut(strings.ToLower(f.Email), "@")
		return "u-" + slugify(strings.ReplaceAll(local, ".", ""))
	}
	labels := strings.Split(f.domain(), ".")
	return slugify(strings.Join(labels[:max(len(labels)-1, 1)], "-"))
}

func (f *Fake) orgKind() string {
	if f.Personal {
		return "personal"
	}
	return "work"
}

// orgKey is how nimbus.yaml's org: names the org: the domain of a
// workspace, the slug of a personal space.
func (f *Fake) orgKey() string {
	if f.Personal {
		return f.orgSlug()
	}
	return f.domain()
}

var nonLabel = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string { return strings.Trim(nonLabel.ReplaceAllString(s, "-"), "-") }

func tarFiles(data []byte) ([]string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	var files []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		files = append(files, h.Name)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "The request body isn't valid JSON: "+err.Error(), "", nil)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message, hint string, details map[string]any) {
	e := map[string]any{"code": code, "message": message}
	if hint != "" {
		e["hint"] = hint
	}
	if details != nil {
		e["details"] = details
	}
	writeJSON(w, status, map[string]any{"error": e})
}

func oauthErr(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

func randID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func userCode() string {
	const letters = "BCDFGHJKLMNPQRSTVWXZ"
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}
