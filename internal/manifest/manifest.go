// Package manifest reads and writes nimbus.yaml.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/wceriale/nimbus-cli/internal/output"
)

// FileName is the manifest's name inside the app directory.
const FileName = "nimbus.yaml"

// Manifest is the whole of nimbus.yaml in v0.1.
type Manifest struct {
	Name string `yaml:"name"`
	Port int    `yaml:"port,omitempty"`
	// Org is the workspace domain (acme.com) or personal space (u-you) the
	// project deploys to, or "" until the first deploy writes it.
	Org    string   `yaml:"org,omitempty"`
	Scopes []string `yaml:"scopes,omitempty"`
}

var (
	nameRE  = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)
	labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	scopeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]*$`)
	fields  = []string{"name", "port", "org", "scopes"}
	// legacyFields are accepted and ignored, so projects from older CLIs
	// still deploy. Writing org: removes them.
	legacyFields = []string{"account"}
	legacyLineRE = regexp.MustCompile(`(?m)^account[ \t]*:.*(\n|$)`)
)

// ValidName reports whether s can be an app name. Names become DNS labels.
func ValidName(s string) bool {
	return len(s) >= 3 && len(s) <= 40 && nameRE.MatchString(s)
}

// InvalidName is the error for a name that breaks the naming rule.
func InvalidName(name, hint string) *output.Error {
	if hint == "" {
		hint = "Use 3–40 lowercase letters, digits and hyphens, starting with a letter, e.g. `renewal-desk`."
	}
	return output.Errorf("invalid_name", hint, "%q isn't a valid app name.", name)
}

// NameFromDir turns a directory name into an app name, e.g. "Renewal_Desk"
// becomes "renewal-desk". The result may still be invalid (too short, say);
// check it with ValidName.
func NameFromDir(dir string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(filepath.Base(dir)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s != "" && s[0] >= '0' && s[0] <= '9' {
		s = "app-" + s
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// Load reads dir/nimbus.yaml. The error wraps fs.ErrNotExist if there is none.
func Load(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse decodes and validates a manifest. Unknown fields are errors, so a
// typo can't be silently ignored.
func Parse(b []byte) (*Manifest, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, invalid("Fix the YAML syntax in nimbus.yaml.", "nimbus.yaml isn't valid YAML: %v", err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if k == "share" {
			return nil, invalid("Remove `share:` from nimbus.yaml and run `nimbus share <email>` instead.",
				"nimbus.yaml has a `share:` field, but sharing isn't part of the manifest.")
		}
		if !slices.Contains(fields, k) && !slices.Contains(legacyFields, k) {
			return nil, invalid("Allowed fields: name, port, org, scopes. Run `nimbus docs` for the format.",
				"nimbus.yaml has an unknown field `%s`.", k)
		}
	}
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, invalid("Run `nimbus docs` for the format.", "nimbus.yaml has a field of the wrong type: %v", err)
	}
	if _, ok := raw["org"]; ok && m.Org == "" {
		return nil, invalidOrg("")
	}
	m.Org = strings.ToLower(strings.TrimSpace(m.Org))
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks the fields' values.
func (m *Manifest) Validate() error {
	if m.Name == "" {
		return invalid("Add `name: <app-name>` to nimbus.yaml.", "nimbus.yaml is missing `name`.")
	}
	if !ValidName(m.Name) {
		return invalid("Use 3–40 lowercase letters, digits and hyphens, starting with a letter.",
			"`name: %s` in nimbus.yaml isn't a valid app name.", m.Name)
	}
	if m.Port < 0 || m.Port > 65535 {
		return invalid("Use a port between 1 and 65535, or leave `port` out to get 8080.",
			"`port: %d` in nimbus.yaml is out of range.", m.Port)
	}
	if m.Org != "" && !ValidOrg(m.Org) {
		return invalidOrg(m.Org)
	}
	for _, s := range m.Scopes {
		if !scopeRE.MatchString(s) {
			return invalid("Scopes look like `google.sheets.read`. Run `nimbus docs` for the list.",
				"Scope %q in nimbus.yaml isn't valid.", s)
		}
	}
	return nil
}

// ValidOrg reports whether s can be an org's key: a domain (labels joined by
// dots) or a personal space's slug (one label).
func ValidOrg(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if len(l) > 63 || !labelRE.MatchString(l) {
			return false
		}
	}
	return true
}

// Write creates dir/nimbus.yaml for a new app. It never overwrites a file.
func Write(dir, name, org string) error {
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "# Nimbus app manifest. Run `nimbus docs` for the format.\nname: %s\norg: %s\n", name, org)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// SetOrg adds `org:` to an existing dir/nimbus.yaml that has none,
// appending a line so the rest of the file stays as it was, apart from a
// legacy `account` line, which it removes. If the line can't simply be
// appended (a flow-style mapping, say), it changes nothing and asks for the
// line to be added by hand.
func SetOrg(dir, org string) error {
	path := filepath.Join(dir, FileName)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	b = legacyLineRE.ReplaceAll(b, nil)
	if len(b) > 0 && b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	b = append(b, "org: "+org+"\n"...)
	if m, err := Parse(b); err != nil || m.Org != org {
		return invalid(fmt.Sprintf("Add the line `org: %s` to nimbus.yaml, then run `nimbus deploy` again.", org),
			"Couldn't add `org: %s` to nimbus.yaml automatically.", org)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, fi.Mode().Perm())
}

func invalidOrg(value string) *output.Error {
	return invalid("Remove the `org` line from nimbus.yaml, and `nimbus deploy` writes it again.",
		"`org: %s` in nimbus.yaml isn't a workspace domain or a personal space like u-you.", value)
}

func invalid(hint, format string, args ...any) *output.Error {
	return output.Errorf("invalid_manifest", hint, format, args...)
}
