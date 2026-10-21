package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/wceriale/nimbus-cli/internal/api"
	"github.com/wceriale/nimbus-cli/internal/manifest"
	"github.com/wceriale/nimbus-cli/internal/output"
)

// appName finds the app a command is about: the argument, then --app, then
// ./nimbus.yaml.
func (a *App) appName(args []string) (string, error) {
	var name string
	switch {
	case len(args) > 0:
		name = args[0]
	case a.appFlag != "":
		name = a.appFlag
	default:
		m, err := manifest.Load(a.WorkDir)
		if errors.Is(err, fs.ErrNotExist) {
			return "", output.Errorf("app_not_specified", "Name the app with --app <name>, or run from the app's directory.",
				"No app given, and there's no nimbus.yaml in %s.", a.WorkDir)
		}
		if err != nil {
			return "", err
		}
		name = m.Name
	}
	if !manifest.ValidName(name) {
		return "", manifest.InvalidName(name, "")
	}
	return name, nil
}

func (a *App) whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who you're logged in as; use it to check the login before other commands",
		Example: `  $ nimbus whoami
  {"ok":true,"data":{"email":"you@acme.com","org":"acme","org_name":"Acme","idp":"google","api_url":"https://api.nimbus.example","auth":"login","cli_version":"v0.1.0"}}`,
		Args: noArgs,
		RunE: a.run(func(ctx context.Context, _ []string) (any, error) {
			sess, err := a.session()
			if err != nil {
				return nil, err
			}
			me, err := sess.client.Me(ctx)
			if err != nil {
				return nil, err
			}
			return &whoamiResult{Email: me.Email, Org: me.Org.Slug, OrgName: me.Org.Name, IdP: me.IdP,
				APIURL: sess.apiURL, Auth: sess.auth, CLIVersion: Version}, nil
		}),
	}
}

type whoamiResult struct {
	Email      string `json:"email"`
	Org        string `json:"org"`
	OrgName    string `json:"org_name"`
	IdP        string `json:"idp"`
	APIURL     string `json:"api_url"`
	Auth       string `json:"auth"`
	CLIVersion string `json:"cli_version"`
}

func (r *whoamiResult) Text() string {
	return fmt.Sprintf("%s (%s, signed in with %s)\nAPI: %s\nCLI: %s", r.Email, r.OrgName, r.IdP, r.APIURL, r.CLIVersion)
}

func (a *App) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [app]",
		Short: "Show an app's URL, state (new|building|live|failed), versions and who has access",
		Long: `Show an app's URL, state, versions and who has access. The app defaults to
--app, then to name in ./nimbus.yaml.

While a deploy runs, or after it failed, "latest" holds that version; when it
failed, latest.log_tail has the end of the log.`,
		Example: `  $ nimbus status
  {"ok":true,"data":{"name":"renewal-desk","url":"https://renewal-desk.acme.nimbusapps.example","owner":"you@acme.com","state":"live","live_version":3,"latest_version":3,"shared_with":["you@acme.com","lena@acme.com"],...}}`,
		Args: maxArgs(1),
		RunE: a.run(a.status),
	}
}

type statusResult struct {
	api.App
	// Latest is the newest version while it is building or after it failed.
	Latest *api.Version `json:"latest,omitempty"`
	Next   string       `json:"next,omitempty"`
}

func (r *statusResult) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is %s: %s\n", r.Name, r.State, r.URL)
	if r.LiveVersion != nil {
		fmt.Fprintf(&b, "Live version: %d\n", *r.LiveVersion)
	}
	if r.Latest != nil {
		fmt.Fprintf(&b, "Latest version: %d (%s)\n", r.Latest.Number, r.Latest.State)
		for _, line := range r.Latest.LogTail {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	fmt.Fprintf(&b, "Shared with: %s", strings.Join(r.SharedWith, ", "))
	if r.Next != "" {
		b.WriteString("\n" + r.Next)
	}
	return b.String()
}

func (a *App) status(ctx context.Context, args []string) (any, error) {
	name, err := a.appName(args)
	if err != nil {
		return nil, err
	}
	sess, err := a.session()
	if err != nil {
		return nil, err
	}
	c := sess.client
	// Fetch the version first, so the app's state is at least as new.
	v, err := c.GetVersion(ctx, name, "latest")
	if err != nil && !isCode(err, "version_not_found") {
		return nil, err
	}
	app, err := c.GetApp(ctx, name)
	if err != nil {
		return nil, err
	}
	res := &statusResult{App: *app}
	if v != nil && v.State != "live" && v.State != "retired" {
		res.Latest = v
	}
	switch app.State {
	case "new":
		res.Next = "Nothing is deployed yet. Run `nimbus deploy` from the app's directory."
	case "building":
		res.Next = fmt.Sprintf("Run `nimbus status %s` again in a few seconds.", name)
	case "failed":
		res.Next = "Read latest.log_tail, fix the problem, then run `nimbus deploy` again."
	case "live":
		if len(app.SharedWith) <= 1 {
			res.Next = "Only you can open it. Run `nimbus share <email>` to give coworkers access."
		}
	}
	return res, nil
}

func (a *App) listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the apps you own",
		Example: `  $ nimbus list
  {"ok":true,"data":{"apps":[{"name":"renewal-desk","url":"https://renewal-desk.acme.nimbusapps.example","state":"live",...}]}}`,
		Args: noArgs,
		RunE: a.run(func(ctx context.Context, _ []string) (any, error) {
			sess, err := a.session()
			if err != nil {
				return nil, err
			}
			apps, err := sess.client.ListApps(ctx)
			if err != nil {
				return nil, err
			}
			if apps == nil {
				apps = []api.App{}
			}
			return &listResult{Apps: apps}, nil
		}),
	}
}

type listResult struct {
	Apps []api.App `json:"apps"`
}

func (r *listResult) Text() string {
	if len(r.Apps) == 0 {
		return "No apps yet. Run `nimbus deploy` in an app's directory."
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tURL")
	for _, app := range r.Apps {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", app.Name, app.State, app.URL)
	}
	tw.Flush()
	return strings.TrimRight(b.String(), "\n")
}

func (a *App) shareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "share <email>...",
		Short: "Give people in your org access to an app",
		Long: `Give people in your org access to an app. The app defaults to --app, then to
name in ./nimbus.yaml. Prints everyone who has access afterwards.

Only addresses in your own org work, and nobody is notified: send them the URL.
Redeploying never changes who has access.`,
		Example: `  $ nimbus share lena@acme.com
  {"ok":true,"data":{"app":"renewal-desk","url":"https://renewal-desk.acme.nimbusapps.example","shared_with":["you@acme.com","lena@acme.com"],"next":"Send them the URL. ..."}}`,
		Args: minArgs(1),
		RunE: a.run(func(ctx context.Context, args []string) (any, error) { return a.updateShares(ctx, args, true) }),
	}
}

func (a *App) unshareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unshare <email>...",
		Short: "Take away people's access to an app",
		Example: `  $ nimbus unshare lena@acme.com
  {"ok":true,"data":{"app":"renewal-desk","url":"https://renewal-desk.acme.nimbusapps.example","shared_with":["you@acme.com"]}}`,
		Args: minArgs(1),
		RunE: a.run(func(ctx context.Context, args []string) (any, error) { return a.updateShares(ctx, args, false) }),
	}
}

type shareResult struct {
	App        string   `json:"app"`
	URL        string   `json:"url"`
	SharedWith []string `json:"shared_with"`
	Next       string   `json:"next,omitempty"`
}

func (r *shareResult) Text() string {
	s := fmt.Sprintf("%s (%s) is shared with: %s", r.App, r.URL, strings.Join(r.SharedWith, ", "))
	if r.Next != "" {
		s += "\n" + r.Next
	}
	return s
}

func (a *App) updateShares(ctx context.Context, emails []string, add bool) (any, error) {
	name, err := a.appName(nil)
	if err != nil {
		return nil, err
	}
	var bad []string
	for i, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		local, domain, ok := strings.Cut(e, "@")
		if !ok || local == "" || !strings.Contains(domain, ".") {
			bad = append(bad, emails[i])
		}
		emails[i] = e
	}
	if len(bad) > 0 {
		return nil, &output.Error{Code: "invalid_email", Message: "These aren't email addresses: " + strings.Join(bad, ", ") + ".",
			Hint: "Pass full addresses, e.g. `nimbus share lena@acme.com`.", Details: map[string]any{"emails": bad}}
	}
	sess, err := a.session()
	if err != nil {
		return nil, err
	}
	var s *api.Shares
	if add {
		s, err = sess.client.UpdateShares(ctx, name, emails, nil)
	} else {
		s, err = sess.client.UpdateShares(ctx, name, nil, emails)
	}
	if err != nil {
		return nil, err
	}
	res := &shareResult{App: s.App, URL: s.URL, SharedWith: s.SharedWith}
	if add {
		res.Next = "Send them the URL. Nimbus doesn't notify anyone; they open it and sign in with their company account."
	}
	return res, nil
}

func (a *App) logsCmd() *cobra.Command {
	var since string
	var limit int
	var build bool
	cmd := &cobra.Command{
		Use:   "logs [app]",
		Short: "Show an app's runtime logs, or with --build the latest build log",
		Long: `Show an app's runtime logs (what it prints to stdout and stderr), oldest
first. With --build, show the full build log of the latest version instead.
The app defaults to --app, then to name in ./nimbus.yaml.`,
		Example: `  $ nimbus logs --since 15m
  {"ok":true,"data":{"app":"renewal-desk","entries":[{"time":"2026-10-21T18:20:11Z","severity":"INFO","message":"Listening on 0.0.0.0:8080","version":3}],"truncated":false}}

  $ nimbus logs --build
  {"ok":true,"data":{"app":"renewal-desk","version":4,"lines":["..."],"truncated":false}}`,
		Args: maxArgs(1),
		RunE: a.run(func(ctx context.Context, args []string) (any, error) {
			if limit < 1 || limit > 1000 {
				return nil, output.Usage("--limit must be between 1 and 1000.", "Run `nimbus logs --help`.")
			}
			name, err := a.appName(args)
			if err != nil {
				return nil, err
			}
			sess, err := a.session()
			if err != nil {
				return nil, err
			}
			if build {
				l, err := sess.client.BuildLog(ctx, name, "latest")
				if err != nil {
					return nil, err
				}
				return &buildLogResult{App: name, Version: l.Version, Lines: l.Lines, Truncated: l.Truncated}, nil
			}
			l, err := sess.client.Logs(ctx, name, since, limit)
			if err != nil {
				return nil, err
			}
			if l.Entries == nil {
				l.Entries = []api.LogEntry{}
			}
			return &logsResult{App: name, Entries: l.Entries, Truncated: l.Truncated}, nil
		}),
	}
	cmd.Flags().StringVar(&since, "since", "1h", "how far back: a duration like 15m or 24h, or an RFC 3339 time")
	cmd.Flags().IntVar(&limit, "limit", 200, "most entries to return (1–1000)")
	cmd.Flags().BoolVar(&build, "build", false, "show the latest version's build log instead")
	return cmd
}

type logsResult struct {
	App       string         `json:"app"`
	Entries   []api.LogEntry `json:"entries"`
	Truncated bool           `json:"truncated"`
}

func (r *logsResult) Text() string {
	if len(r.Entries) == 0 {
		return "No log entries in that window."
	}
	var b strings.Builder
	for _, e := range r.Entries {
		fmt.Fprintf(&b, "%s %-7s %s\n", e.Time.Format("2006-01-02T15:04:05Z07:00"), e.Severity, e.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}

type buildLogResult struct {
	App       string   `json:"app"`
	Version   int      `json:"version"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

func (r *buildLogResult) Text() string {
	return fmt.Sprintf("Build log for %s version %d:\n%s", r.App, r.Version, strings.Join(r.Lines, "\n"))
}
