package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/wceriale/nimbus-cli/internal/api"
	"github.com/wceriale/nimbus-cli/internal/output"
)

// connectorCmd is `nimbus connector`: the workspace's data connectors, which
// let its apps read an outside data source as the person viewing them.
func (a *App) connectorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connector",
		Short: "Set up data sources your apps read as the viewer (owner only)",
		Long: `Connectors let your workspace's apps read an outside data source, such as
Postgres, as the person viewing the app. Each query logs in as that viewer,
so they see exactly what the data source lets them see, and runs read-only.
The workspace owner sets connectors up; any member can list them. An app
declares the ones it reads in nimbus.yaml: connectors: [warehouse].`,
		Args: noArgs,
		RunE: a.run(func(context.Context, []string) (any, error) {
			return nil, output.Usage("`nimbus connector` needs a subcommand.", "Run `nimbus connector list`, or `nimbus connector --help`.")
		}),
	}
	cmd.AddCommand(a.connectorListCmd(), a.connectorShowCmd(), a.connectorAddCmd(), a.connectorRemoveCmd())
	return cmd
}

// orgFor returns the signed-in account's org id, or no_org.
func (a *App) orgFor(ctx context.Context, sess *session) (string, error) {
	me, err := sess.client.Me(ctx)
	if err != nil {
		return "", err
	}
	if me.Org == nil {
		return "", output.Errorf("no_org", "Run `nimbus deploy` first: it creates "+firstPlace(me.Domain)+".",
			"You don't have a workspace yet.")
	}
	return strconv.FormatInt(me.Org.ID, 10), nil
}

func (a *App) connectorListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the workspace's connectors",
		Example: `  $ nimbus connector list
  {"ok":true,"data":{"connectors":[{"name":"warehouse","driver":"postgres","auth":"viewer","network":"public","read_only":true,"config":{"host":"35.1.2.3","port":5432,"database":"analytics","sslmode":"verify-ca"}}]}}`,
		Args: noArgs,
		RunE: a.run(func(ctx context.Context, _ []string) (any, error) {
			sess, err := a.session()
			if err != nil {
				return nil, err
			}
			org, err := a.orgFor(ctx, sess)
			if err != nil {
				return nil, err
			}
			list, err := sess.client.Connectors(ctx, org)
			if err != nil {
				return nil, err
			}
			if list == nil {
				list = []api.Connector{}
			}
			return &connectorList{Connectors: list}, nil
		}),
	}
}

type connectorList struct {
	Connectors []api.Connector `json:"connectors"`
}

func (r *connectorList) Text() string {
	if len(r.Connectors) == 0 {
		return "No connectors yet. The workspace owner adds one with `nimbus connector add`."
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, c := range r.Connectors {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\tas each viewer\n", c.Name, c.Driver, where(c), c.Network)
	}
	tw.Flush()
	return strings.TrimRight(b.String(), "\n")
}

func (a *App) connectorShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show one connector",
		Args:  func(cmd *cobra.Command, args []string) error { return argCount(cmd, args, 1, 1) },
		RunE: a.run(func(ctx context.Context, args []string) (any, error) {
			sess, err := a.session()
			if err != nil {
				return nil, err
			}
			org, err := a.orgFor(ctx, sess)
			if err != nil {
				return nil, err
			}
			c, err := sess.client.GetConnector(ctx, org, args[0])
			if err != nil {
				return nil, err
			}
			return (*connectorShown)(c), nil
		}),
	}
}

type connectorShown api.Connector

func (r *connectorShown) Text() string {
	c := (*api.Connector)(r)
	lines := []string{
		fmt.Sprintf("%s: %s at %s, over a %s network.", c.Name, c.Driver, where(*c), c.Network),
		"Reads run as each viewer, read-only.",
	}
	if len(c.AllowFrom) > 0 {
		lines = append(lines, "Allow these Nimbus IPs on the database: "+strings.Join(c.AllowFrom, ", "))
	}
	if c.ServiceAccount != "" {
		lines = append(lines, grantHint(c.ServiceAccount))
	}
	if c.CreatedBy != "" {
		lines = append(lines, "Set up by "+c.CreatedBy+".")
	}
	return strings.Join(lines, "\n")
}

func (a *App) connectorAddCmd() *cobra.Command {
	var driver, instance, host, database, sslmode, network, caFile string
	var port int
	cmd := &cobra.Command{
		Use:   "add <name> (--instance <connection-name> | --host <address>) --database <name>",
		Short: "Add a connector, or change one (owner only)",
		Long: `Add a data connector to the workspace, or replace one with the same name.
Owner only. Each viewer's query logs in as that viewer, so there's no
user or password to give.

For Cloud SQL: --instance with the instance connection name
(gcloud sql instances describe INSTANCE --format='value(connectionName)').
Then grant the service account it prints the Cloud SQL Client role on the
instance's project. The instance needs no authorized networks.

For any other Postgres: --host, and --network public for a public IP, which
then has to allow the Nimbus IPs it prints.`,
		Example: `  $ nimbus connector add warehouse --instance acme-prod:us-central1:main-db --database analytics
  {"ok":true,"data":{"name":"warehouse","driver":"postgres","auth":"viewer","network":"cloudsql","read_only":true,"config":{"instance":"acme-prod:us-central1:main-db","database":"analytics"},"service_account":"nimbus-server@nimbus-513018.iam.gserviceaccount.com",...}}
  $ nimbus connector add billing --host 35.1.2.3 --database billing --network public --sslmode verify-ca --server-ca-file server-ca.pem`,
		Args: func(cmd *cobra.Command, args []string) error { return argCount(cmd, args, 1, 1) },
		RunE: a.run(func(ctx context.Context, args []string) (any, error) {
			if instance != "" && network == "" {
				network = "cloudsql"
			}
			in := api.ConnectorInput{Driver: driver, Auth: "viewer", Network: network, Config: map[string]any{}}
			flags := map[string]string{"instance": instance, "host": host, "database": database, "sslmode": sslmode}
			for k, v := range flags {
				if v != "" {
					in.Config[k] = v
				}
			}
			if port != 0 {
				in.Config["port"] = port
			}
			if caFile != "" {
				b, err := os.ReadFile(a.resolvePath(caFile))
				if err != nil {
					return nil, output.Usage(fmt.Sprintf("Can't read --server-ca-file: %v.", err), "Pass the path of the CA certificate file, in PEM form.")
				}
				in.Config["server_ca"] = string(b)
			}
			sess, err := a.session()
			if err != nil {
				return nil, err
			}
			org, err := a.orgFor(ctx, sess)
			if err != nil {
				return nil, err
			}
			c, err := sess.client.PutConnector(ctx, org, args[0], in)
			if err != nil {
				return nil, err
			}
			return &connectorAdded{Connector: *c, created: c.CreatedAt.Equal(c.UpdatedAt)}, nil
		}),
	}
	f := cmd.Flags()
	f.StringVar(&driver, "driver", "postgres", "kind of data source")
	f.StringVar(&instance, "instance", "", "Cloud SQL: the instance connection name, PROJECT:REGION:INSTANCE")
	f.StringVar(&host, "host", "", "the database's IP address or hostname")
	f.IntVar(&port, "port", 0, "port (default 5432)")
	f.StringVar(&database, "database", "", "database to read")
	f.StringVar(&network, "network", "", "private (default): an address on a private network; public: a public IP that allowlists Nimbus; cloudsql (with --instance)")
	f.StringVar(&sslmode, "sslmode", "", "verify-full (default), verify-ca (needs --server-ca-file), or require (private networks only)")
	f.StringVar(&caFile, "server-ca-file", "", "the server's CA certificate (PEM), to verify it")
	return cmd
}

// resolvePath reads a relative path from the command's working directory.
func (a *App) resolvePath(p string) string {
	if filepath.IsAbs(p) || a.WorkDir == "" {
		return p
	}
	return filepath.Join(a.WorkDir, p)
}

type connectorAdded struct {
	api.Connector
	created bool
}

func (r *connectorAdded) Text() string {
	verb := "updated"
	if r.created {
		verb = "added"
	}
	s := fmt.Sprintf("Connector %s %s: %s at %s. Reads run as each viewer.", r.Name, verb, r.Driver, where(r.Connector))
	if len(r.AllowFrom) > 0 {
		s += "\nAllow these Nimbus IPs on the database: " + strings.Join(r.AllowFrom, ", ")
	}
	if r.ServiceAccount != "" {
		s += "\n" + grantHint(r.ServiceAccount)
	}
	return s
}

// grantHint tells a Cloud SQL connector's owner what to grant Nimbus.
func grantHint(serviceAccount string) string {
	return "Grant Nimbus's service account " + serviceAccount + " the Cloud SQL Client role on the instance's project."
}

func (a *App) connectorRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a connector (owner only)",
		Args:  func(cmd *cobra.Command, args []string) error { return argCount(cmd, args, 1, 1) },
		RunE: a.run(func(ctx context.Context, args []string) (any, error) {
			sess, err := a.session()
			if err != nil {
				return nil, err
			}
			org, err := a.orgFor(ctx, sess)
			if err != nil {
				return nil, err
			}
			if err := sess.client.DeleteConnector(ctx, org, args[0]); err != nil {
				return nil, err
			}
			return &connectorRemoved{Name: strings.ToLower(args[0]), Removed: true}, nil
		}),
	}
}

type connectorRemoved struct {
	Name    string `json:"name"`
	Removed bool   `json:"removed"`
}

func (r *connectorRemoved) Text() string { return "Connector " + r.Name + " removed." }

// where is a connector's address, e.g. 35.1.2.3:5432/analytics, or
// acme-prod:us-central1:main-db/analytics for Cloud SQL.
func where(c api.Connector) string {
	var cfg struct {
		Instance string `json:"instance"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Database string `json:"database"`
	}
	json.Unmarshal(c.Config, &cfg)
	if cfg.Instance != "" {
		return cfg.Instance + "/" + cfg.Database
	}
	if cfg.Host == "" {
		return "(no host)"
	}
	return fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
}
