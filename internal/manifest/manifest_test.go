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
	m, err := Parse([]byte("name: renewal-desk\nport: 5000\norg: Acme.com\nscopes: [google.sheets.read]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "renewal-desk" || m.Port != 5000 || m.Org != "acme.com" || len(m.Scopes) != 1 {
		t.Errorf("Parse = %+v", m)
	}
	if m, err := Parse([]byte("name: renewal-desk\norg: u-you\n")); err != nil || m.Org != "u-you" {
		t.Errorf("Parse of a personal space = %+v, %v", m, err)
	}
	// What older CLIs wrote is accepted and ignored.
	if m, err := Parse([]byte("name: renewal-desk\nacc" + "ount: work\n")); err != nil || m.Org != "" {
		t.Errorf("Parse of an old manifest = %+v, %v; want no org", m, err)
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
		{"name: renewal-desk\norg: not a domain\n", "Remove the `org` line"},
		{"name: renewal-desk\norg: -acme.com\n", "Remove the `org` line"},
		{"name: renewal-desk\norg:\n", "Remove the `org` line"},
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
	if err := Write(dir, "renewal-desk", "u-you"); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, "other", "acme.com"); err == nil {
		t.Error("Write overwrote an existing manifest")
	}
	m, err := Load(dir)
	if err != nil || m.Name != "renewal-desk" || m.Org != "u-you" {
		t.Fatalf("Load = %+v, %v", m, err)
	}
}

// SetOrg appends one line and leaves the rest of the file as it was,
// comments and all, with or without a final newline.
func TestSetOrg(t *testing.T) {
	for _, before := range []string{
		"# My planner\nname: planner   # the URL\nport: 5000\n",
		"name: planner\nport: 5000",
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, FileName)
		if err := os.WriteFile(path, []byte(before), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := SetOrg(dir, "acme.com"); err != nil {
			t.Fatalf("SetOrg(%q): %v", before, err)
		}
		b, _ := os.ReadFile(path)
		if !strings.HasPrefix(string(b), before) || !strings.HasSuffix(string(b), "\norg: acme.com\n") {
			t.Errorf("SetOrg(%q) wrote %q", before, b)
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
			t.Errorf("SetOrg changed the mode to %v", fi.Mode().Perm())
		}
		if m, err := Load(dir); err != nil || m.Name != "planner" || m.Port != 5000 || m.Org != "acme.com" {
			t.Errorf("Load after SetOrg = %+v, %v", m, err)
		}
	}

	// The line older CLIs wrote goes, wherever it is, and nothing else does.
	for before, want := range map[string]string{
		"name: planner\nacc" + "ount: personal\nport: 5000\n": "name: planner\nport: 5000\norg: u-you\n",
		"name: planner\nacc" + "ount: work":                   "name: planner\norg: u-you\n",
		"# acc" + "ount: in a comment\nname: planner\n":       "# acc" + "ount: in a comment\nname: planner\norg: u-you\n",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(before), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := SetOrg(dir, "u-you"); err != nil {
			t.Fatalf("SetOrg(%q): %v", before, err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, FileName)); string(b) != want {
			t.Errorf("SetOrg(%q) wrote %q, want %q", before, b, want)
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
	if err := SetOrg(dir, "u-you"); !errors.As(err, &e) || e.Code != "invalid_manifest" || !strings.Contains(e.Hint, "org: u-you") {
		t.Errorf("SetOrg on a flow mapping = %v, want invalid_manifest asking for the line", err)
	}
	if b, _ := os.ReadFile(path); string(b) != before {
		t.Errorf("SetOrg on a flow mapping changed the file to %q", b)
	}
}
