package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wceriale/nimbus-cli/internal/api"
	"github.com/wceriale/nimbus-cli/internal/fakeapi"
)

const testCA = "-----BEGIN CERTIFICATE-----\nMIIBfake\n-----END CERTIFICATE-----\n"

func TestConnectors(t *testing.T) {
	h := newHarness(t)
	h.login()
	if e := h.fail("no_org", t.TempDir(), "connector", "list"); !strings.Contains(e.Hint, "nimbus deploy") {
		t.Errorf("no_org = %+v", e)
	}
	app := appDir(t, "planner", map[string]string{"app.py": "x"})
	h.ok(app, "deploy")

	if d := h.ok(t.TempDir(), "connector", "list"); len(d["connectors"].([]any)) != 0 {
		t.Fatalf("empty list = %v", d)
	}

	// Cloud SQL over its public IP: verified TLS with the instance's CA.
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "server-ca.pem"), []byte(testCA), 0o644)
	d := h.ok(work, "connector", "add", "warehouse", "--host", "35.1.2.3", "--database", "analytics",
		"--network", "public", "--sslmode", "verify-ca", "--server-ca-file", "server-ca.pem")
	cfg, _ := d["config"].(map[string]any)
	if d["name"] != "warehouse" || d["driver"] != "postgres" || d["auth"] != "viewer" || d["network"] != "public" ||
		cfg["port"] != 5432.0 || cfg["server_ca"] != testCA || len(d["allow_from"].([]any)) != 1 {
		t.Fatalf("add = %v", d)
	}
	// Adding again replaces it; only the flags given are sent.
	d = h.ok(work, "connector", "add", "warehouse", "--host", "35.1.2.4", "--database", "analytics",
		"--network", "public", "--sslmode", "verify-full")
	if cfg, _ := d["config"].(map[string]any); cfg["host"] != "35.1.2.4" || cfg["server_ca"] != nil {
		t.Fatalf("replace = %v", d)
	}
	if d := h.ok(t.TempDir(), "connector", "show", "warehouse"); d["name"] != "warehouse" {
		t.Fatalf("show = %v", d)
	}
	if d := h.ok(t.TempDir(), "connector", "list"); len(d["connectors"].([]any)) != 1 {
		t.Fatalf("list = %v", d)
	}

	// The server's refusals come through unchanged.
	if e := h.fail("invalid_connector_config", t.TempDir(), "connector", "add", "billing", "--database", "x"); e.Details["field"] != "host" {
		t.Errorf("no host = %+v", e)
	}
	h.fail("unsupported_network", t.TempDir(), "connector", "add", "billing", "--host", "10.0.0.5", "--database", "x", "--network", "vpn")
	h.fail("invalid_name", t.TempDir(), "connector", "add", "Bad_Name", "--host", "10.0.0.5", "--database", "x")
	h.fail("usage", t.TempDir(), "connector", "add", "billing", "--host", "10.0.0.5", "--database", "x", "--server-ca-file", "nope.pem")
	h.fail("usage", t.TempDir(), "connector", "add")
	h.fail("usage", t.TempDir(), "connector")

	// An app declares the connectors it reads; a missing one fails the deploy.
	reader := appDir(t, "reports", map[string]string{"app.py": "x", "nimbus.yaml": "name: reports\nconnectors: [warehouse]\n"})
	h.ok(reader, "deploy")
	missing := appDir(t, "other", map[string]string{"app.py": "x", "nimbus.yaml": "name: other\nconnectors: [billing]\n"})
	if e := h.fail("unknown_connector", missing, "deploy"); !strings.Contains(e.Hint, "nimbus connector list") {
		t.Errorf("unknown_connector = %+v", e)
	}
	h.fail("invalid_manifest", appDir(t, "bad", map[string]string{"nimbus.yaml": "name: bad-one\nconnectors: [Bad_Name]\n"}), "deploy")

	// Only the owner changes connectors; members can still read them.
	h.fake.Member = true
	h.fail("not_org_owner", t.TempDir(), "connector", "remove", "warehouse")
	h.ok(t.TempDir(), "connector", "list")
	h.fake.Member = false

	if d := h.ok(t.TempDir(), "connector", "remove", "warehouse"); d["name"] != "warehouse" || d["removed"] != true {
		t.Fatalf("remove = %v", d)
	}
	h.fail("connector_not_found", t.TempDir(), "connector", "remove", "warehouse")
	h.fail("connector_not_found", t.TempDir(), "connector", "show", "warehouse")
}

func TestConnectorText(t *testing.T) {
	cfg := json.RawMessage(`{"host":"35.1.2.3","port":5432,"database":"analytics","sslmode":"verify-ca"}`)
	now := time.Now()
	c := api.Connector{Name: "warehouse", Driver: "postgres", Auth: "viewer", Network: "public", Config: cfg,
		AllowFrom: []string{fakeapi.EgressIP}, CreatedBy: "you@acme.com", CreatedAt: now, UpdatedAt: now}
	if got := (&connectorAdded{Connector: c, created: true}).Text(); got !=
		"Connector warehouse added: postgres at 35.1.2.3:5432/analytics. Reads run as each viewer.\nAllow these Nimbus IPs on the database: 203.0.113.7" {
		t.Errorf("added = %q", got)
	}
	if got := (&connectorAdded{Connector: c}).Text(); !strings.HasPrefix(got, "Connector warehouse updated:") {
		t.Errorf("updated = %q", got)
	}
	if got := (&connectorList{Connectors: []api.Connector{c}}).Text(); got != "warehouse  postgres  35.1.2.3:5432/analytics  public  as each viewer" {
		t.Errorf("list = %q", got)
	}
	if got := (&connectorList{}).Text(); !strings.HasPrefix(got, "No connectors yet.") {
		t.Errorf("empty list = %q", got)
	}
	if got := (*connectorShown)(&c).Text(); !strings.Contains(got, "warehouse: postgres at 35.1.2.3:5432/analytics, over a public network.") ||
		!strings.Contains(got, "Allow these Nimbus IPs") {
		t.Errorf("show = %q", got)
	}
	if got := (&connectorRemoved{Name: "warehouse"}).Text(); got != "Connector warehouse removed." {
		t.Errorf("removed = %q", got)
	}
}
