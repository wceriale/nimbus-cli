package manifest

import (
	"errors"
	"io/fs"
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
	m, err := Parse([]byte("name: renewal-desk\nport: 5000\nscopes: [google.sheets.read]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "renewal-desk" || m.Port != 5000 || len(m.Scopes) != 1 {
		t.Errorf("Parse = %+v", m)
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
	if err := Write(dir, "renewal-desk"); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, "other"); err == nil {
		t.Error("Write overwrote an existing manifest")
	}
	m, err := Load(dir)
	if err != nil || m.Name != "renewal-desk" {
		t.Fatalf("Load = %+v, %v", m, err)
	}
}
