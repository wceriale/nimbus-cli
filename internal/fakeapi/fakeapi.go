// Package fakeapi is an in-memory Nimbus control plane for one org. It
// follows the same API contract as the real server, so the CLI's tests run
// against it, and `go run ./cmd/nimbus-fakeapi` serves it for trying the CLI
// by hand.
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
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxUpload = 100 << 20

var nameRE = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

// Fake is safe for concurrent use. Set the exported fields before serving;
// change behavior while serving only through methods.
type Fake struct {
	// Email is the user every device login signs in as. Its domain is the
	// org's domain.
	Email string
	// AutoApprove approves device codes on their first poll, skipping the browser.
	AutoApprove bool
	// AccessTTL is how long access tokens live.
	AccessTTL time.Duration

	mu        sync.Mutex
	rejectCLI bool
	failNext  *VersionError
	devices   map[string]*device
	access    map[string]session
	refresh   map[string]string
	apps      map[string]*app
	uploads   map[string]*upload
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
}

type app struct {
	name, owner      string
	shares           map[string]bool
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

// AddApp creates an app owned by someone, e.g. to test name_taken.
func (f *Fake) AddApp(name, owner string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
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

func (f *Fake) issue(w http.ResponseWriter, email string) {
	at, rt := randID("at"), randID("rt")
	f.access[at] = session{email: email, expires: time.Now().Add(f.AccessTTL)}
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
	slug := f.orgSlug()
	writeJSON(w, http.StatusOK, map[string]any{
		"email": email,
		"org":   map[string]string{"slug": slug, "name": strings.ToUpper(slug[:1]) + slug[1:]},
		"idp":   "google",
	})
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

func (f *Fake) ensureApp(w http.ResponseWriter, r *http.Request, email string) {
	name := r.PathValue("app")
	if len(name) < 3 || len(name) > 40 || !nameRE.MatchString(name) {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_name", fmt.Sprintf("%q isn't a valid app name.", name),
			"Use 3–40 lowercase letters, digits and hyphens, starting with a letter.", nil)
		return
	}
	if a, ok := f.apps[name]; ok {
		if a.owner != email {
			writeErr(w, http.StatusConflict, "name_taken", fmt.Sprintf("Someone else in your org already has an app called %q.", name),
				"Change `name` in nimbus.yaml and run `nimbus deploy` again.", nil)
			return
		}
		writeJSON(w, http.StatusOK, f.appJSON(a))
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
		if !strings.HasSuffix(e, "@"+f.domain()) {
			outside = append(outside, e)
		}
	}
	if len(outside) > 0 {
		writeErr(w, http.StatusUnprocessableEntity, "share_outside_org",
			fmt.Sprintf("These addresses aren't in your org (%s): %s.", f.domain(), strings.Join(outside, ", ")),
			"Nimbus v0.1 only shares inside your org.", map[string]any{"emails": outside})
		return
	}
	if slices.Contains(remove, a.owner) {
		writeErr(w, http.StatusUnprocessableEntity, "cannot_unshare_owner", "The owner always has access.", "Leave the owner out of `nimbus unshare`.", nil)
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
	a.updated = time.Now()
	writeJSON(w, http.StatusOK, map[string]any{"app": a.name, "url": f.appURL(a), "shared_with": a.sharedWith()})
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
	_, d, _ := strings.Cut(f.Email, "@")
	return d
}

func (f *Fake) orgSlug() string {
	s, _, _ := strings.Cut(f.domain(), ".")
	return s
}

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
