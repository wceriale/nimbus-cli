package manifest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wceriale/nimbus-cli/internal/output"
)

func TestNameFromDir(t *testing.T) {
	for dir, want := range map[string]string{
		"/x/renewal-desk":               "renewal-desk",
		"/x/Renewal_Desk":               "renewal-desk",
		"/x/My  App!!":                  "my-app",
		"/x/2024-dashboard":             "app-2024-dashboard",
		"/x/" + strings.Repeat("a", 50): strings.Repeat("a", 40),
	} {
		if got := NameFromDir(dir); got != want {
			t.Errorf("NameFromDir(%q) = %q, want %q", dir, got, want)
		}
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"renewal-desk": true,
		"abc":          true,
		"ab":           false,
		"-abc":         false,
		"abc-":         false,
		"Abc":          false,
		"1abc":         false,
		"a_b_c":        false,
	} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParse(t *testing.T) {
	m, err := Parse([]byte("name: renewal-desk\nport: 5000\naccount: work\nscopes: [google.sheets.read]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "renewal-desk" || m.Port != 5000 || m.Account != Work || len(m.Scopes) != 1 {
		t.Errorf("Parse = %+v", m)
	}
	if m, err := Parse([]byte("name: renewal-desk\n")); err != nil || m.Account != "" {
		t.Errorf("Parse without account = %+v, %v; want no account", m, err)
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct{ yaml, hint string }{
		{"name: x\nshare:\n  - a@b.com\n", "nimbus share"},
		{"name: renewal-desk\nruntime: docker\n", "Allowed fields"},
		{"port: 8080\n", "Add `name"},
		{"name: Bad_Name\n", "lowercase"},
		{"name: renewal-desk\nport: 99999\n", "between 1 and 65535"},
		{"name: renewal-desk\nport: eighty\n", "format"},
		{"name: [unclosed\n", "YAML syntax"},
		{"name: renewal-desk\naccount: home\n", "account: personal"},
		{"name: renewal-desk\naccount: Personal\n", "account: personal"},
		{"name: renewal-desk\naccount:\n", "account: personal"},
	} {
		_, err := Parse([]byte(tc.yaml))
		var e *output.Error
		if !errors.As(err, &e) || e.Code != "invalid_manifest" {
			t.Errorf("Parse(%q) error = %v, want invalid_manifest", tc.yaml, err)
			continue
		}
		if !strings.Contains(e.Hint, tc.hint) {
			t.Errorf("Parse(%q) hint = %q, want it to mention %q", tc.yaml, e.Hint, tc.hint)
		}
	}
}

func TestWriteThenLoad(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load on empty dir = %v, want ErrNotExist", err)
	}
	if err := Write(dir, "renewal-desk", Personal); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, "other", Work); err == nil {
		t.Error("Write overwrote an existing manifest")
	}
	m, err := Load(dir)
	if err != nil || m.Name != "renewal-desk" || m.Account != Personal {
		t.Fatalf("Load = %+v, %v", m, err)
	}
}

// SetAccount appends one line and leaves the rest of the file as it was,
// comments and all, with or without a final newline.
func TestSetAccount(t *testing.T) {
	for _, before := range []string{
		"# My planner\nname: planner   # the URL\nport: 5000\n",
		"name: planner\nport: 5000",
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, FileName)
		if err := os.WriteFile(path, []byte(before), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := SetAccount(dir, Work); err != nil {
			t.Fatalf("SetAccount(%q): %v", before, err)
		}
		b, _ := os.ReadFile(path)
		if !strings.HasPrefix(string(b), before) || !strings.HasSuffix(string(b), "\naccount: work\n") {
			t.Errorf("SetAccount(%q) wrote %q", before, b)
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
			t.Errorf("SetAccount changed the mode to %v", fi.Mode().Perm())
		}
		if m, err := Load(dir); err != nil || m.Name != "planner" || m.Port != 5000 || m.Account != Work {
			t.Errorf("Load after SetAccount = %+v, %v", m, err)
		}
	}

	// A flow-style mapping can't take an appended line: nothing changes.
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	before := "{name: planner, port: 5000}\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	var e *output.Error
	if err := SetAccount(dir, Personal); !errors.As(err, &e) || e.Code != "invalid_manifest" || !strings.Contains(e.Hint, "account: personal") {
		t.Errorf("SetAccount on a flow mapping = %v, want invalid_manifest asking for the line", err)
	}
	if b, _ := os.ReadFile(path); string(b) != before {
		t.Errorf("SetAccount on a flow mapping changed the file to %q", b)
	}
}
