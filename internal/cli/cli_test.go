package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wceriale/nimbus-cli/internal/fakeapi"
	"github.com/wceriale/nimbus-cli/internal/output"
)

// harness runs the CLI as an agent would: stdout isn't a terminal, so every
// command must print exactly one JSON object.
type harness struct {
	t    *testing.T
	fake *fakeapi.Fake
	srv  *httptest.Server
	cfg  string
	env  map[string]string

	opened  []string // URLs the CLI tried to open in a browser
	onSleep func()   // runs when the CLI sleeps between polls, if set
}

func newHarness(t *testing.T) *harness { return newHarnessFor(t, "you@acme.com") }

// newHarnessFor signs every login in as email. A @gmail.com address gets a
// personal account.
func newHarnessFor(t *testing.T, email string) *harness {
	t.Helper()
	f := fakeapi.New(email)
	srv := httptest.NewServer(f.Handler())
	t.Cleanup(srv.Close)
	return &harness{t: t, fake: f, srv: srv, cfg: t.TempDir(), env: map[string]string{"NIMBUS_API_URL": srv.URL}}
}

type result struct {
	code   int
	stdout string
	stderr string
	OK     bool           `json:"ok"`
	Data   map[string]any `json:"data"`
	Error  *output.Error  `json:"error"`
}

func (h *harness) run(dir string, args ...string) *result {
	h.t.Helper()
	var stdout, stderr bytes.Buffer
	a := &App{
		Stdout:     &stdout,
		Stderr:     &stderr,
		Getenv:     func(k string) string { return h.env[k] },
		WorkDir:    dir,
		ConfigDir:  h.cfg,
		HTTPClient: h.srv.Client(),
		OpenBrowser: func(u string) error {
			h.opened = append(h.opened, u)
			return errors.New("no browser in tests")
		},
		Sleep: func(time.Duration) {
			if h.onSleep != nil {
				h.onSleep()
			}
		},
		Now: time.Now,
	}
	r := &result{code: a.Run(args), stdout: stdout.String(), stderr: stderr.String()}
	dec := json.NewDecoder(&stdout)
	if err := dec.Decode(r); err != nil {
		h.t.Fatalf("nimbus %s: stdout isn't JSON: %v\n%s", strings.Join(args, " "), err, r.stdout)
	}
	if dec.More() {
		h.t.Fatalf("nimbus %s: stdout has more than one JSON object:\n%s", strings.Join(args, " "), r.stdout)
	}
	return r
}

// ok runs a command that must succeed.
func (h *harness) ok(dir string, args ...string) map[string]any {
	h.t.Helper()
	r := h.run(dir, args...)
	if r.code != 0 || !r.OK {
		h.t.Fatalf("nimbus %s failed (exit %d): %+v\nstderr: %s", strings.Join(args, " "), r.code, r.Error, r.stderr)
	}
	return r.Data
}

// fail runs a command that must fail with code.
func (h *harness) fail(code string, dir string, args ...string) *output.Error {
	h.t.Helper()
	r := h.run(dir, args...)
	if r.code == 0 || r.OK || r.Error == nil || r.Error.Code != code {
		h.t.Fatalf("nimbus %s: got exit %d, error %+v; want error %s", strings.Join(args, " "), r.code, r.Error, code)
	}
	if r.Error.Hint == "" {
		h.t.Errorf("nimbus %s: error %s has no hint", strings.Join(args, " "), code)
	}
	return r.Error
}

func (h *harness) login() {
	h.t.Helper()
	h.ok(h.t.TempDir(), "login")
	h.fake.ApproveAll()
	if d := h.ok(h.t.TempDir(), "login"); d["status"] != "logged_in" {
		h.t.Fatalf("login didn't finish: %v", d)
	}
}

// appDir makes an app directory named name with the given files.
func appDir(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	for f, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoginIsTwoStepForAgents(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()

	d := h.ok(dir, "login")
	if d["status"] != "pending" || d["user_code"] == "" || !strings.Contains(d["verification_url"].(string), "/device?code=") {
		t.Fatalf("first login = %v, want pending with a URL and code", d)
	}
	if !strings.Contains(d["next"].(string), "nimbus login") {
		t.Errorf("pending result doesn't say what to do next: %v", d["next"])
	}
	// Not approved yet: the same code again.
	if again := h.ok(dir, "login"); again["status"] != "pending" || again["user_code"] != d["user_code"] {
		t.Errorf("second login before approval = %v, want the same pending code", again)
	}

	h.fake.ApproveAll()
	if _, ok := d["org"]; ok {
		t.Errorf("pending login reports an org: %v", d)
	}
	// No org yet: the first deploy creates it.
	d = h.ok(dir, "login")
	if _, ok := d["org"]; d["status"] != "logged_in" || d["email"] != "you@acme.com" || d["domain"] != "acme.com" || ok {
		t.Fatalf("login after approval = %v", d)
	}
	fi, err := os.Stat(filepath.Join(h.cfg, "credentials.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials file: %v, mode %v; want 0600", err, fi.Mode().Perm())
	}
	if d := h.ok(dir, "login"); d["status"] != "logged_in" {
		t.Errorf("login while logged in = %v", d)
	}
	d = h.ok(dir, "whoami")
	if d["email"] != "you@acme.com" || d["auth"] != "login" || d["org"] != "" || !strings.Contains(d["next"].(string), "creates the acme.com workspace") {
		t.Errorf("whoami = %v", d)
	}
}

// A work domain that isn't enabled during private testing can't log in.
func TestLoginDomainNotEnabled(t *testing.T) {
	h := newHarness(t)
	h.fake.DomainNotEnabled = true
	h.ok(t.TempDir(), "login")
	h.fake.ApproveAll()
	e := h.fail("domain_not_enabled", t.TempDir(), "login")
	if !strings.Contains(e.Message, "private testing") || !strings.Contains(e.Hint, "enabled") {
		t.Errorf("domain_not_enabled = %+v", e)
	}
	// The code is gone: the next login starts over.
	if d := h.ok(t.TempDir(), "login"); d["status"] != "pending" {
		t.Errorf("login after refusal = %v", d)
	}
}

// A waiting login that resumes an earlier run's code shows the code again and
// reopens the browser, since that run's tab may be gone. If the code was
// already approved, it just finishes.
func TestLoginWaitResumesPendingCode(t *testing.T) {
	h := newHarness(t)
	first := h.ok(t.TempDir(), "login")
	h.opened = nil
	h.onSleep = h.fake.ApproveAll
	r := h.run(t.TempDir(), "login", "--wait")
	if !r.OK || r.Data["status"] != "logged_in" {
		t.Fatalf("resumed login = %+v, %+v", r.Data, r.Error)
	}
	url := first["verification_url"].(string)
	if !strings.Contains(r.stderr, url) || !strings.Contains(r.stderr, first["user_code"].(string)) {
		t.Errorf("stderr = %q, want the URL and code again", r.stderr)
	}
	if len(h.opened) != 1 || h.opened[0] != url {
		t.Errorf("opened %v, want %s once", h.opened, url)
	}

	h = newHarness(t)
	h.ok(t.TempDir(), "login")
	h.fake.ApproveAll()
	h.opened = nil
	r = h.run(t.TempDir(), "login", "--wait")
	if !r.OK || r.Data["status"] != "logged_in" || len(h.opened) != 0 || r.stderr != "" {
		t.Errorf("resuming an approved code = %+v, opened %v, stderr %q; want logged_in quietly", r.Data, h.opened, r.stderr)
	}
}

func TestLogout(t *testing.T) {
	h := newHarness(t)
	h.login()
	credsPath := filepath.Join(h.cfg, "credentials.json")
	var creds map[string]any
	if b, err := os.ReadFile(credsPath); err != nil || json.Unmarshal(b, &creds) != nil {
		t.Fatalf("credentials: %v", err)
	}

	d := h.ok(t.TempDir(), "logout")
	if d["status"] != "logged_out" || d["revoked"] != true || d["email"] != "you@acme.com" || d["api_url"] != h.srv.URL {
		t.Fatalf("logout = %v", d)
	}
	if _, err := os.Stat(credsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("credentials file after logout: %v", err)
	}
	req, _ := http.NewRequest("GET", h.srv.URL+"/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+creds["access_token"].(string))
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("old access token after logout: %s, want 401", resp.Status)
	}
	h.fail("not_logged_in", t.TempDir(), "whoami")

	if d := h.ok(t.TempDir(), "logout"); d["status"] != "logged_out" || d["revoked"] != false || d["api_url"] != nil {
		t.Errorf("logout while logged out = %v", d)
	}
}

func TestLogoutWithoutServer(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.srv.Close()
	d := h.ok(t.TempDir(), "logout")
	if d["revoked"] != false || !strings.Contains(d["next"].(string), "Couldn't reach the server") {
		t.Errorf("logout without a server = %v", d)
	}
	if _, err := os.Stat(filepath.Join(h.cfg, "credentials.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("credentials file after logout: %v", err)
	}
}

func TestLoginRestartsWhenCodeExpires(t *testing.T) {
	h := newHarness(t)
	first := h.ok(t.TempDir(), "login")
	h.fake.ExpireDeviceCodes()
	second := h.ok(t.TempDir(), "login")
	if second["status"] != "pending" || second["user_code"] == first["user_code"] {
		t.Errorf("login after expiry = %v, want a new pending code", second)
	}
}

func TestNotLoggedIn(t *testing.T) {
	h := newHarness(t)
	e := h.fail("not_logged_in", t.TempDir(), "whoami")
	if !strings.Contains(e.Hint, "nimbus login") {
		t.Errorf("hint = %q", e.Hint)
	}
}

func TestAPIURL(t *testing.T) {
	h := newHarness(t)
	delete(h.env, "NIMBUS_API_URL")
	saved := DefaultAPIURL
	DefaultAPIURL = ""
	t.Cleanup(func() { DefaultAPIURL = saved })
	h.fail("api_url_not_set", t.TempDir(), "login")

	h.env["NIMBUS_API_URL"] = "http://nimbus.dev"
	h.fail("invalid_api_url", t.TempDir(), "login")
}

// A login saved for another API, such as one from before the default moved,
// doesn't pick the API: the CLI uses the default and logs in again there.
func TestLoginForOtherAPIIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.login()

	moved := httptest.NewServer(fakeapi.New("you@acme.com").Handler())
	t.Cleanup(moved.Close)
	delete(h.env, "NIMBUS_API_URL")
	saved := DefaultAPIURL
	DefaultAPIURL = moved.URL
	t.Cleanup(func() { DefaultAPIURL = saved })

	h.fail("not_logged_in", t.TempDir(), "whoami")
	if d := h.ok(t.TempDir(), "login"); d["status"] != "pending" || d["api_url"] != moved.URL {
		t.Errorf("login = %v, want a new pending login at %s", d, moved.URL)
	}
}

func TestTokenRefresh(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.fake.ExpireAccessTokens()
	if d := h.ok(t.TempDir(), "whoami"); d["email"] != "you@acme.com" {
		t.Errorf("whoami after expiry = %v", d)
	}
}

func TestDeploy(t *testing.T) {
	h := newHarness(t)
	h.login()
	dir := appDir(t, "renewal-desk", map[string]string{
		"app.py":                  "print('hi')",
		".gitignore":              "*.log\n",
		"debug.log":               "noise",
		".env":                    "SECRET=1",
		"node_modules/x/index.js": "dep",
	})

	d := h.ok(dir, "deploy")
	if d["app"] != "renewal-desk" || d["org"] != "acme.com" || d["org_kind"] != "work" || d["state"] != "live" || d["version"] != 1.0 ||
		d["manifest_created"] != true || d["visibility"] != "private" || d["shares_used"] != 0.0 || d["shares_limit"] != 5.0 ||
		d["apps_used"] != 1.0 || d["apps_limit"] != 5.0 {
		t.Fatalf("deploy = %v", d)
	}
	if !strings.Contains(d["next"].(string), "nimbus share") {
		t.Errorf("deploy doesn't point at sharing: %v", d["next"])
	}
	if skipped := d["env_files_skipped"].([]any); len(skipped) != 1 || skipped[0] != ".env" {
		t.Errorf("env_files_skipped = %v", skipped)
	}
	if got, want := h.fake.UploadedFiles("renewal-desk"), []string{".gitignore", "app.py", "nimbus.yaml"}; !slices.Equal(got, want) {
		t.Errorf("uploaded %v, want %v", got, want)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "nimbus.yaml")); err != nil || !strings.HasSuffix(string(b), "\nname: renewal-desk\norg: acme.com\n") {
		t.Errorf("nimbus.yaml = %q, %v; want it to pin org: acme.com", b, err)
	}
	if d := h.ok(dir, "whoami"); d["org"] != "acme.com" || d["org_kind"] != "work" || d["role"] != "owner" || d["apps_used"] != 1.0 {
		t.Errorf("whoami after the first deploy = %v", d)
	}

	again := h.ok(dir, "deploy")
	if again["unchanged"] != true || again["version"] != 1.0 || again["manifest_created"] != false || again["org"] != "acme.com" {
		t.Errorf("redeploy of the same source = %v, want unchanged version 1", again)
	}

	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('v2')"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v2 := h.ok(dir, "deploy"); v2["version"] != 2.0 || v2["unchanged"] != false {
		t.Errorf("deploy after a change = %v, want version 2", v2)
	}
}

func TestDeployFailureCarriesLogTail(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.fake.SetFailNext(&fakeapi.VersionError{
		Code:    "start_failed",
		Message: "Container didn't listen on port 8080 within 60s",
		LogTail: []string{"Running on http://127.0.0.1:5000"},
	})
	dir := appDir(t, "planner", map[string]string{"app.py": "x"})
	e := h.fail("start_failed", dir, "deploy")
	if len(e.LogTail) != 1 || !strings.Contains(e.Hint, "$PORT") || !strings.Contains(e.Hint, "nimbus logs planner --build") {
		t.Errorf("failure = %+v", e)
	}
	st := h.ok(dir, "status")
	latest, _ := st["latest"].(map[string]any)
	if st["state"] != "failed" || latest == nil || latest["log_tail"] == nil {
		t.Errorf("status after failure = %v, want state failed with latest.log_tail", st)
	}
	if b := h.ok(dir, "logs", "--build"); len(b["lines"].([]any)) == 0 {
		t.Errorf("logs --build = %v", b)
	}
}

func TestDeployNoWaitThenStatus(t *testing.T) {
	h := newHarness(t)
	h.login()
	dir := appDir(t, "planner", map[string]string{"app.py": "x"})
	d := h.ok(dir, "deploy", "--no-wait")
	if d["state"] != "queued" || !strings.Contains(d["next"].(string), "nimbus status") {
		t.Fatalf("deploy --no-wait = %v", d)
	}
	var state any
	for range 5 {
		if state = h.ok(dir, "status")["state"]; state == "live" {
			break
		}
	}
	if state != "live" {
		t.Errorf("status never reached live, last state %v", state)
	}
}

func TestDeployNameRules(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.fake.AddApp("taken", "someone@acme.com")
	h.fail("name_taken", appDir(t, "taken", map[string]string{"app.py": "x"}), "deploy")
	h.fail("invalid_name", appDir(t, "x", map[string]string{"app.py": "x"}), "deploy")
	if d := h.ok(appDir(t, "x", map[string]string{"app.py": "x"}), "deploy", "--app", "my-tool"); d["app"] != "my-tool" {
		t.Errorf("deploy --app = %v", d)
	}
	h.fail("invalid_manifest", appDir(t, "shared", map[string]string{"nimbus.yaml": "name: shared\nshare: [a@acme.com]\n"}), "deploy")
}

// A nimbus.yaml without org: gets one on its next deploy, keeping the rest
// of the file, apart from a line older CLIs wrote, which goes.
func TestDeployPinsOrg(t *testing.T) {
	h := newHarness(t)
	h.login()
	legacy := "acc" + "ount: work\n" // what older CLIs wrote
	old := appDir(t, "old", map[string]string{"app.py": "x", "nimbus.yaml": "# Planner\nname: planner\n" + legacy + "port: 8080\n"})
	if d := h.ok(old, "deploy"); d["app"] != "planner" || d["org"] != "acme.com" || d["manifest_created"] != false ||
		!strings.Contains(d["next"].(string), "Commit nimbus.yaml") {
		t.Errorf("deploy of an old manifest = %v", d)
	}
	if b, _ := os.ReadFile(filepath.Join(old, "nimbus.yaml")); string(b) != "# Planner\nname: planner\nport: 8080\norg: acme.com\n" {
		t.Errorf("nimbus.yaml = %q, want the old content with org: acme.com instead of the old line", b)
	}
	if d := h.ok(old, "deploy"); d["unchanged"] != true || d["org"] != "acme.com" {
		t.Errorf("redeploy = %v", d)
	}
	h.fail("invalid_manifest", appDir(t, "bad", map[string]string{"nimbus.yaml": "name: bad-org\norg: not a domain\n"}), "deploy")
}

// A project pinned to another org than the login's fails with the server's
// wrong_account, before anything is written or uploaded.
func TestDeployWrongAccount(t *testing.T) {
	h := newHarness(t)
	h.login()
	manifest := "name: planner\norg: u-someone\n"
	dir := appDir(t, "planner", map[string]string{"app.py": "x", "nimbus.yaml": manifest})
	e := h.fail("wrong_account", dir, "deploy")
	if e.Message != "This app belongs to u-someone. You're signed in as you@acme.com (acme.com)." ||
		e.Hint != "Run `nimbus login` to switch accounts, or remove `org:` from nimbus.yaml to deploy a separate copy here." ||
		e.Details["app_org"] != "u-someone" || e.Details["account_org"] != "acme.com" {
		t.Errorf("wrong_account = %+v", e)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "nimbus.yaml")); string(b) != manifest {
		t.Errorf("nimbus.yaml = %q, want it untouched", b)
	}
	if got := h.fake.UploadedFiles("planner"); got != nil {
		t.Errorf("uploaded %v", got)
	}

	// A personal account with no space yet matches no pin.
	h = newHarnessFor(t, "you@gmail.com")
	h.login()
	dir = appDir(t, "planner", map[string]string{"app.py": "x", "nimbus.yaml": "name: planner\norg: acme.com\n"})
	if e := h.fail("wrong_account", dir, "deploy"); e.Message != "This app belongs to acme.com. You're signed in as you@gmail.com (personal)." {
		t.Errorf("wrong_account = %+v", e)
	}
}

func TestPersonalProject(t *testing.T) {
	h := newHarnessFor(t, "you@gmail.com")
	h.login()
	if d := h.ok(t.TempDir(), "whoami"); d["org"] != "" || d["domain"] != "" || !strings.Contains(d["next"].(string), "creates your personal space") {
		t.Errorf("whoami before deploying = %v", d)
	}

	dir := appDir(t, "planner", map[string]string{"app.py": "x"})
	d := h.ok(dir, "deploy")
	if d["org"] != "u-you" || d["org_kind"] != "personal" || d["public"] == true || d["visibility"] != "private" ||
		!strings.Contains(d["url"].(string), ".u-you.") || !strings.Contains(d["next"].(string), "nimbus share") {
		t.Fatalf("personal deploy = %v", d)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "nimbus.yaml")); !strings.HasSuffix(string(b), "\norg: u-you\n") {
		t.Errorf("nimbus.yaml = %q", b)
	}
	if d := h.ok(t.TempDir(), "login"); d["org"] != "u-you" || d["org_kind"] != "personal" {
		t.Errorf("login after deploying = %v", d)
	}

	// Personal apps share with any Google account, 5 people on the free plan.
	d = h.ok(dir, "share", "a@gmail.com", "b@outlook.com", "c@acme.com", "d@gmail.com", "e@gmail.com")
	if d["visibility"] != "shared" || d["shares_used"] != 5.0 || d["shares_limit"] != 5.0 {
		t.Errorf("share = %v", d)
	}
	if e := h.fail("share_limit", dir, "share", "f@gmail.com"); e.Details["shares_used"] != 5.0 {
		t.Errorf("share_limit = %+v", e)
	}
	if d := h.ok(dir, "share", "--public"); d["public"] != true || d["visibility"] != "public" || d["shares_used"] != 5.0 {
		t.Errorf("share --public = %v", d)
	}
	if d := h.ok(dir, "unshare", "--public"); d["public"] != false || d["visibility"] != "shared" {
		t.Errorf("unshare --public = %v", d)
	}
	h.fail("org_is_personal", t.TempDir(), "org", "external-sharing", "on")

	// 5 apps per org, public ones included.
	for i := range 4 {
		h.fake.AddApp(fmt.Sprintf("app-%d", i), "you@gmail.com")
	}
	e := h.fail("app_limit", appDir(t, "sixth", map[string]string{"app.py": "x"}), "deploy")
	if e.Message != "This workspace has 5 apps, the free limit. Delete one with `nimbus destroy <app>` or upgrade." || e.Details["apps_used"] != 5.0 {
		t.Errorf("app_limit = %+v", e)
	}
}

func TestShare(t *testing.T) {
	h := newHarness(t)
	h.login()
	dir := appDir(t, "planner", map[string]string{"app.py": "x"})
	h.ok(dir, "deploy")

	d := h.ok(dir, "share", "Lena@Acme.com", "priya@acme.com")
	if got := d["shared_with"].([]any); len(got) != 3 || !slices.Contains(got, any("lena@acme.com")) || d["shares_used"] != 2.0 || d["visibility"] != "shared" {
		t.Errorf("share = %v", d)
	}
	e := h.fail("external_sharing_disabled", dir, "share", "x@other.com")
	if !slices.Contains(e.Details["emails"].([]any), any("x@other.com")) || !strings.Contains(e.Hint, "nimbus org external-sharing on") {
		t.Errorf("external_sharing_disabled = %+v", e)
	}
	h.fail("invalid_email", dir, "share", "lena")
	d = h.ok(dir, "unshare", "lena@acme.com")
	if got := d["shared_with"].([]any); len(got) != 2 {
		t.Errorf("unshare = %v", d)
	}
	if st := h.ok(dir, "status"); len(st["shared_with"].([]any)) != 2 || st["public"] != false {
		t.Errorf("status = %v", st)
	}

	d = h.ok(dir, "share", "--public")
	if d["public"] != true || len(d["shared_with"].([]any)) != 2 || !strings.Contains(d["next"].(string), "unshare --public") {
		t.Errorf("share --public = %v", d)
	}
	if st := h.ok(dir, "status"); st["public"] != true || st["next"] != nil {
		t.Errorf("status of a public app = %v", st)
	}
	if d = h.ok(dir, "deploy"); d["public"] != true || d["next"] != nil {
		t.Errorf("deploy of a public app = %v", d)
	}
	d = h.ok(dir, "unshare", "--public")
	if d["public"] != false || len(d["shared_with"].([]any)) != 2 {
		t.Errorf("unshare --public = %v", d)
	}
	if st := h.ok(dir, "status"); st["public"] != false {
		t.Errorf("status after unshare --public = %v", st)
	}
}

// The workspace owner turns external sharing on to share outside the domain;
// turning it off removes those shares.
func TestExternalSharing(t *testing.T) {
	h := newHarness(t)
	h.login()
	if e := h.fail("no_org", t.TempDir(), "org", "external-sharing", "on"); !strings.Contains(e.Hint, "nimbus deploy") {
		t.Errorf("no_org = %+v", e)
	}
	dir := appDir(t, "planner", map[string]string{"app.py": "x"})
	h.ok(dir, "deploy")
	h.fail("external_sharing_disabled", dir, "share", "friend@gmail.com")

	if d := h.ok(t.TempDir(), "org", "external-sharing", "on"); d["org"] != "acme.com" || d["external_sharing"] != true || d["removed_shares"] != 0.0 {
		t.Errorf("external-sharing on = %v", d)
	}
	h.ok(dir, "share", "friend@gmail.com", "lena@acme.com")
	if d := h.ok(t.TempDir(), "org", "external-sharing", "off"); d["external_sharing"] != false || d["removed_shares"] != 1.0 {
		t.Errorf("external-sharing off = %v", d)
	}
	if st := h.ok(dir, "status"); fmt.Sprint(st["shared_with"]) != "[you@acme.com lena@acme.com]" {
		t.Errorf("shares after off = %v", st["shared_with"])
	}

	h.fake.Member = true
	h.fail("not_org_owner", t.TempDir(), "org", "external-sharing", "on")
}

func TestAppResolution(t *testing.T) {
	h := newHarness(t)
	h.login()
	empty := t.TempDir()
	h.fail("app_not_specified", empty, "status")
	h.fail("app_not_found", empty, "status", "nope")
	dir := appDir(t, "planner", map[string]string{"app.py": "x"})
	h.ok(dir, "deploy")
	if d := h.ok(empty, "status", "--app", "planner"); d["name"] != "planner" || d["org"] != "acme.com" || d["visibility"] != "private" {
		t.Errorf("status --app = %v", d)
	}
	if d := h.ok(empty, "list"); len(d["apps"].([]any)) != 1 {
		t.Errorf("list = %v", d)
	}
	if d := h.ok(empty, "logs", "planner"); len(d["entries"].([]any)) != 1 {
		t.Errorf("logs = %v", d)
	}
}

func TestUsageErrorsExit2(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"share"}, {"unshare"}, {"share", "--public=false"}, {"frobnicate"}, {"deploy", "--bogus"},
		{"deploy", "--work"}, {"logs", "--limit", "0"}, {"org"}, {"org", "external-sharing"}, {"org", "external-sharing", "maybe"}} {
		r := h.run(t.TempDir(), args...)
		if r.code != 2 || r.Error == nil || r.Error.Code != "usage" || r.Error.Hint == "" {
			t.Errorf("nimbus %v: exit %d, error %+v; want exit 2 with usage", args, r.code, r.Error)
		}
	}
}

func TestCLIOutdated(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.fake.SetRejectCLI(true)
	if e := h.fail("cli_outdated", t.TempDir(), "whoami"); !strings.Contains(e.Hint, "install.sh") {
		t.Errorf("hint = %q", e.Hint)
	}
}

func TestEnvToken(t *testing.T) {
	h := newHarness(t)
	h.env["NIMBUS_TOKEN"] = "bogus"
	if e := h.fail("not_logged_in", t.TempDir(), "whoami"); !strings.Contains(e.Hint, "NIMBUS_TOKEN") {
		t.Errorf("hint = %q", e.Hint)
	}
}

func TestDocsPrintsMarkdown(t *testing.T) {
	var stdout bytes.Buffer
	a := &App{Stdout: &stdout, Stderr: &bytes.Buffer{}, Getenv: func(string) string { return "" }}
	if code := a.Run([]string{"docs"}); code != 0 {
		t.Fatalf("docs exit %d", code)
	}
	md := stdout.String()
	for _, want := range []string{"# Nimbus agent guide", "X-Viewer-Email", "## Command reference", "### `nimbus deploy [dir]`", "--no-wait",
		"### `nimbus org external-sharing on|off`"} {
		if !strings.Contains(md, want) {
			t.Errorf("docs output is missing %q", want)
		}
	}
}

func TestDeployText(t *testing.T) {
	five, n := 5, 2
	work := &deployResult{App: "planner", URL: "https://planner.acme.example", Owner: "you@acme.com", Org: "acme.com", OrgKind: "work",
		Visibility: "private", AppsUsed: n, AppsLimit: &five, SharesLimit: &five, Version: 1, State: "live"}
	want := "Deployed to the acme.com workspace.\nhttps://planner.acme.example\nPrivate: only you can open it.\n" +
		"nimbus share sam@acme.com · nimbus share --public\nApps: 2/5 · Shares on this app: 0/5"
	if got := work.Text(); got != want {
		t.Errorf("work deploy text =\n%s\nwant\n%s", got, want)
	}
	personal := &deployResult{App: "blog", URL: "https://blog.u-you.example", Owner: "you@gmail.com", Org: "u-you", OrgKind: "personal",
		Visibility: "shared", SharesUsed: 1, Version: 2, State: "live", Unchanged: true}
	want = "Deployed to your personal space (you@gmail.com).\nhttps://blog.u-you.example\nShared with 1 person.\n" +
		"nimbus share friend@gmail.com · nimbus share --public\nVersion 2, unchanged."
	if got := personal.Text(); got != want {
		t.Errorf("personal deploy text =\n%s\nwant\n%s", got, want)
	}
}
