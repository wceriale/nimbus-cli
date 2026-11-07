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
	Name    string   `yaml:"name"`
	Port    int      `yaml:"port,omitempty"`
	Account string   `yaml:"account,omitempty"` // Personal or Work; "" until the user has said
	Scopes  []string `yaml:"scopes,omitempty"`
}

// The values of account. A personal project runs under a personal Google
// account and is always public; a work project runs in a company's org,
// private to the people it's shared with.
const (
	Personal = "personal"
	Work     = "work"
)

var (
	nameRE  = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)
	scopeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]*$`)
	fields  = []string{"name", "port", "account", "scopes"}
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
		if !slices.Contains(fields, k) {
			return nil, invalid("Allowed fields: name, port, account, scopes. Run `nimbus docs` for the format.",
				"nimbus.yaml has an unknown field `%s`.", k)
		}
	}
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, invalid("Run `nimbus docs` for the format.", "nimbus.yaml has a field of the wrong type: %v", err)
	}
	if _, ok := raw["account"]; ok && m.Account == "" {
		return nil, invalidAccount("")
	}
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
	if m.Account != "" && m.Account != Personal && m.Account != Work {
		return invalidAccount(m.Account)
	}
	for _, s := range m.Scopes {
		if !scopeRE.MatchString(s) {
			return invalid("Scopes look like `google.sheets.read`. Run `nimbus docs` for the list.",
				"Scope %q in nimbus.yaml isn't valid.", s)
		}
	}
	return nil
}

// Write creates dir/nimbus.yaml for a new app. It never overwrites a file.
func Write(dir, name, account string) error {
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "# Nimbus app manifest. Run `nimbus docs` for the format.\nname: %s\naccount: %s\n", name, account)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// SetAccount adds `account:` to an existing dir/nimbus.yaml that has none,
// appending a line so the rest of the file stays exactly as it was. If the
// line can't simply be appended (a flow-style mapping, say), it changes
// nothing and asks for the line to be added by hand.
func SetAccount(dir, account string) error {
	path := filepath.Join(dir, FileName)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	b = append(b, "account: "+account+"\n"...)
	if m, err := Parse(b); err != nil || m.Account != account {
		return invalid(fmt.Sprintf("Add the line `account: %s` to nimbus.yaml, then run `nimbus deploy` again.", account),
			"Couldn't add `account: %s` to nimbus.yaml automatically.", account)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, fi.Mode().Perm())
}

func invalidAccount(value string) *output.Error {
	return invalid("Use `account: personal` or `account: work`, or remove the line to be asked again.",
		"`account: %s` in nimbus.yaml isn't valid.", value)
}

func invalid(hint, format string, args ...any) *output.Error {
	return output.Errorf("invalid_manifest", hint, format, args...)
}
