package cli

import (
	"bytes"
	"encoding/json"
	"errors"
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
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	f := fakeapi.New("you@acme.com")
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
		Stdout:      &stdout,
		Stderr:      &stderr,
		Getenv:      func(k string) string { return h.env[k] },
		WorkDir:     dir,
		ConfigDir:   h.cfg,
		HTTPClient:  h.srv.Client(),
		OpenBrowser: func(string) error { return errors.New("no browser in tests") },
		Sleep:       func(time.Duration) {},
		Now:         time.Now,
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
	d = h.ok(dir, "login")
	if d["status"] != "logged_in" || d["email"] != "you@acme.com" || d["org"] != "acme" {
		t.Fatalf("login after approval = %v", d)
	}
	fi, err := os.Stat(filepath.Join(h.cfg, "credentials.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials file: %v, mode %v; want 0600", err, fi.Mode().Perm())
	}
	if d := h.ok(dir, "login"); d["status"] != "logged_in" {
		t.Errorf("login while logged in = %v", d)
	}
	if d := h.ok(dir, "whoami"); d["email"] != "you@acme.com" || d["auth"] != "login" {
		t.Errorf("whoami = %v", d)
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
	if d["app"] != "renewal-desk" || d["state"] != "live" || d["version"] != 1.0 || d["manifest_created"] != true {
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
	if _, err := os.Stat(filepath.Join(dir, "nimbus.yaml")); err != nil {
		t.Errorf("nimbus.yaml wasn't written: %v", err)
	}

	again := h.ok(dir, "deploy")
	if again["unchanged"] != true || again["version"] != 1.0 || again["manifest_created"] != false {
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

func TestShare(t *testing.T) {
	h := newHarness(t)
	h.login()
	dir := appDir(t, "planner", map[string]string{"app.py": "x"})
	h.ok(dir, "deploy")

	d := h.ok(dir, "share", "Lena@Acme.com", "priya@acme.com")
	if got := d["shared_with"].([]any); len(got) != 3 || !slices.Contains(got, any("lena@acme.com")) {
		t.Errorf("share = %v", d)
	}
	e := h.fail("share_outside_org", dir, "share", "x@other.com")
	if !slices.Contains(e.Details["emails"].([]any), any("x@other.com")) {
		t.Errorf("share_outside_org details = %v", e.Details)
	}
	h.fail("invalid_email", dir, "share", "lena")
	d = h.ok(dir, "unshare", "lena@acme.com")
	if got := d["shared_with"].([]any); len(got) != 2 {
		t.Errorf("unshare = %v", d)
	}
	if st := h.ok(dir, "status"); len(st["shared_with"].([]any)) != 2 {
		t.Errorf("status shared_with = %v", st["shared_with"])
	}
}

func TestAppResolution(t *testing.T) {
	h := newHarness(t)
	h.login()
	empty := t.TempDir()
	h.fail("app_not_specified", empty, "status")
	h.fail("app_not_found", empty, "status", "nope")
	dir := appDir(t, "planner", map[string]string{"app.py": "x"})
	h.ok(dir, "deploy")
	if d := h.ok(empty, "status", "--app", "planner"); d["name"] != "planner" {
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
	for _, args := range [][]string{{"share"}, {"frobnicate"}, {"deploy", "--bogus"}, {"logs", "--limit", "0"}} {
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
	for _, want := range []string{"# Nimbus agent guide", "X-Viewer-Email", "## Command reference", "### `nimbus deploy [dir]`", "--no-wait"} {
		if !strings.Contains(md, want) {
			t.Errorf("docs output is missing %q", want)
		}
	}
}
