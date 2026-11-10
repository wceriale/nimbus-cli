package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
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
		Long: `Show who you're logged in as, and where your apps go: the workspace of your
work domain, or for a personal account, such as Gmail, your personal space.
"org" is empty until the first deploy creates it.`,
		Example: `  $ nimbus whoami
  {"ok":true,"data":{"email":"you@acme.com","domain":"acme.com","org":"acme.com","org_kind":"work","role":"owner","external_sharing":false,"apps_used":2,"apps_limit":5,"idp":"google","api_url":"https://api.nimby.run","auth":"login","cli_version":"v0.1.0"}}`,
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
			res := &whoamiResult{Email: me.Email, Domain: me.Domain, IdP: me.IdP, APIURL: sess.apiURL, Auth: sess.auth, CLIVersion: Version}
			if o := me.Org; o != nil {
				res.Org, res.OrgKind, res.Role, res.ExternalSharing = o.Key, o.Kind, o.Role, o.ExternalSharing
				res.AppsUsed, res.AppsLimit = &o.AppsUsed, o.AppsLimit
			} else {
				res.Next = "No apps yet: your first `nimbus deploy` creates " + firstPlace(me.Domain) + "."
			}
			return res, nil
		}),
	}
}

// firstPlace names where a first deploy goes for an account with domain.
func firstPlace(domain string) string {
	if domain == "" {
		return "your personal space"
	}
	return "the " + domain + " workspace"
}

type whoamiResult struct {
	Email           string `json:"email"`
	Domain          string `json:"domain"`   // the account's verified domain; "" for a personal account
	Org             string `json:"org"`      // where apps go, as nimbus.yaml's org: says it; "" before the first deploy
	OrgKind         string `json:"org_kind"` // personal or work
	Role            string `json:"role,omitempty"`
	ExternalSharing bool   `json:"external_sharing"`
	AppsUsed        *int   `json:"apps_used,omitempty"`
	AppsLimit       *int   `json:"apps_limit,omitempty"`
	IdP             string `json:"idp"`
	APIURL          string `json:"api_url"`
	Auth            string `json:"auth"`
	CLIVersion      string `json:"cli_version"`
	Next            string `json:"next,omitempty"`
}

func (r *whoamiResult) Text() string {
	var place string
	switch {
	case r.Org == "":
		place = "no apps yet"
	case r.OrgKind == api.OrgPersonal:
		place = "personal space " + r.Org
	default:
		place = r.Org + " workspace, " + r.Role
	}
	s := fmt.Sprintf("%s (%s, signed in with %s)", r.Email, place, r.IdP)
	if r.AppsUsed != nil && r.AppsLimit != nil {
		s += fmt.Sprintf("\nApps: %d/%d", *r.AppsUsed, *r.AppsLimit)
	}
	s += fmt.Sprintf("\nAPI: %s\nCLI: %s", r.APIURL, r.CLIVersion)
	if r.Next != "" {
		s += "\n" + r.Next
	}
	return s
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
  {"ok":true,"data":{"name":"renewal-desk","url":"https://renewal-desk.acme.nimbus-apps.dev","owner":"you@acme.com","state":"live","live_version":3,"latest_version":3,"shared_with":["you@acme.com","lena@acme.com"],...}}`,
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
	if r.Public {
		b.WriteString("Public: anyone can open it without signing in")
	} else {
		fmt.Fprintf(&b, "Shared with: %s", strings.Join(r.SharedWith, ", "))
	}
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
		if !app.Public && len(app.SharedWith) <= 1 {
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
  {"ok":true,"data":{"apps":[{"name":"renewal-desk","url":"https://renewal-desk.acme.nimbus-apps.dev","state":"live",...}]}}`,
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
	var public bool
	cmd := &cobra.Command{
		Use:   "share [email]...",
		Short: "Give people access to an app by email, or with --public open it to everyone",
		Long: `Give people access to an app by email. The app defaults to --app, then to name
in ./nimbus.yaml. Prints everyone who has access afterwards.

An app in your personal space can be shared with any Google account. An app in
a work workspace can be shared with addresses in the workspace's domain, and
outside it only once the workspace owner turns on external sharing (nimbus org
external-sharing on). On the free plan an app can be shared with 5 people.
Nobody is notified: send them the URL. Redeploying never changes who has
access.

With --public, anyone on the internet can open the app without signing in, and
the app gets no X-Viewer-Email header. Public doesn't count as a share. The
share list is kept for when you make it private again with nimbus unshare
--public.`,
		Example: `  $ nimbus share lena@acme.com
  {"ok":true,"data":{"app":"renewal-desk","url":"https://renewal-desk.acme.nimbus-apps.dev","shared_with":["you@acme.com","lena@acme.com"],"public":false,"visibility":"shared","shares_used":1,"shares_limit":5,"next":"Send them the URL. ..."}}

  $ nimbus share --public
  {"ok":true,"data":{"app":"renewal-desk","url":"https://renewal-desk.acme.nimbus-apps.dev","shared_with":["you@acme.com","lena@acme.com"],"public":true,"visibility":"public","shares_used":1,"shares_limit":5,"next":"Anyone with the URL can open it. ..."}}`,
		Args: emailsOrPublic(&public),
		RunE: a.run(func(ctx context.Context, args []string) (any, error) { return a.updateShares(ctx, args, true, public) }),
	}
	cmd.Flags().BoolVar(&public, "public", false, "let anyone on the internet open the app, without signing in")
	return cmd
}

func (a *App) unshareCmd() *cobra.Command {
	var public bool
	cmd := &cobra.Command{
		Use:   "unshare [email]...",
		Short: "Take away people's access to an app, or with --public make it private again",
		Long: `Take away people's access to an app. The app defaults to --app, then to name in
./nimbus.yaml. Prints everyone who has access afterwards.

With --public, a public app becomes private again: viewers have to sign in, and
only the people it's shared with get in.`,
		Example: `  $ nimbus unshare lena@acme.com
  {"ok":true,"data":{"app":"renewal-desk","url":"https://renewal-desk.acme.nimbus-apps.dev","shared_with":["you@acme.com"],"public":false,"visibility":"private","shares_used":0,"shares_limit":5}}

  $ nimbus unshare --public
  {"ok":true,"data":{"app":"renewal-desk","url":"https://renewal-desk.acme.nimbus-apps.dev","shared_with":["you@acme.com"],"public":false,"visibility":"private","shares_used":0,"shares_limit":5}}`,
		Args: emailsOrPublic(&public),
		RunE: a.run(func(ctx context.Context, args []string) (any, error) { return a.updateShares(ctx, args, false, public) }),
	}
	cmd.Flags().BoolVar(&public, "public", false, "make a public app private again, so viewers have to sign in")
	return cmd
}

// emailsOrPublic requires at least one email, unless --public is set.
func emailsOrPublic(public *bool) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 || *public {
			return nil
		}
		return output.Usage(fmt.Sprintf("`%s` needs at least one email address, or --public.", cmd.CommandPath()),
			fmt.Sprintf("Usage: `%s`. Run `%s --help` for details.", cmd.UseLine(), cmd.CommandPath()))
	}
}

type shareResult struct {
	App         string   `json:"app"`
	URL         string   `json:"url"`
	SharedWith  []string `json:"shared_with"`
	Public      bool     `json:"public"`
	Visibility  string   `json:"visibility"`
	SharesUsed  int      `json:"shares_used"`
	SharesLimit *int     `json:"shares_limit"`
	Next        string   `json:"next,omitempty"`
}

func (r *shareResult) Text() string {
	access := "shared with: " + strings.Join(r.SharedWith, ", ")
	if r.Public {
		access = "public: anyone can open it without signing in"
	}
	s := fmt.Sprintf("%s (%s) is %s", r.App, r.URL, access)
	if r.Next != "" {
		s += "\n" + r.Next
	}
	return s
}

// updateShares adds or removes emails. With public, share makes the app
// public and unshare makes it private again.
func (a *App) updateShares(ctx context.Context, emails []string, add, public bool) (any, error) {
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
	var pub *bool
	if public {
		pub = &add
	}
	var s *api.Shares
	if add {
		s, err = sess.client.UpdateShares(ctx, name, emails, nil, pub)
	} else {
		s, err = sess.client.UpdateShares(ctx, name, nil, emails, pub)
	}
	if err != nil {
		return nil, err
	}
	res := &shareResult{App: s.App, URL: s.URL, SharedWith: s.SharedWith, Public: s.Public,
		Visibility: s.Visibility, SharesUsed: s.SharesUsed, SharesLimit: s.SharesLimit}
	switch {
	case s.Public:
		res.Next = "Anyone with the URL can open it, without signing in, and the app gets no X-Viewer-Email. Run `nimbus unshare --public` to require sign-in again."
	case add:
		res.Next = "Send them the URL. Nimbus doesn't notify anyone; they open it and sign in with their Google account."
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

func (a *App) orgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Change your workspace's settings (owner only)",
		Long: `Change your workspace's settings. Only the workspace owner can, and personal
spaces have none.`,
		Args: noArgs,
		RunE: a.run(func(context.Context, []string) (any, error) {
			return nil, output.Usage("`nimbus org` needs a setting.", "Run `nimbus org external-sharing on` or `nimbus org external-sharing off`.")
		}),
	}
	cmd.AddCommand(a.externalSharingCmd())
	return cmd
}

func (a *App) externalSharingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "external-sharing on|off",
		Short: "Let your workspace's apps be shared outside its domain, or stop it",
		Long: `Owner only. Lets the workspace's apps be shared with addresses outside the
domain. Turning it off removes those shares, and prints how many.`,
		Example: `  $ nimbus org external-sharing on
  {"ok":true,"data":{"org":"acme.com","external_sharing":true,"removed_shares":0}}

  $ nimbus org external-sharing off
  {"ok":true,"data":{"org":"acme.com","external_sharing":false,"removed_shares":2}}`,
		Args: onOrOff,
		RunE: a.run(func(ctx context.Context, args []string) (any, error) {
			sess, err := a.session()
			if err != nil {
				return nil, err
			}
			me, err := sess.client.Me(ctx)
			if err != nil {
				return nil, err
			}
			switch {
			case me.Org == nil:
				return nil, output.Errorf("no_org", "Run `nimbus deploy` first: it creates "+firstPlace(me.Domain)+".",
					"You don't have a workspace yet.")
			case me.Org.Kind == api.OrgPersonal:
				return nil, output.Errorf("org_is_personal",
					"Personal apps can already be shared with any email address, e.g. `nimbus share friend@gmail.com`.",
					"External sharing is a workspace setting; a personal space always shares outside.")
			}
			u, err := sess.client.SetExternalSharing(ctx, strconv.FormatInt(me.Org.ID, 10), args[0] == "on")
			if err != nil {
				return nil, err
			}
			return &externalSharingResult{Org: u.Org.Key, ExternalSharing: u.Org.ExternalSharing, RemovedShares: u.RemovedShares}, nil
		}),
	}
}

// onOrOff requires exactly one argument, on or off.
func onOrOff(cmd *cobra.Command, args []string) error {
	if len(args) == 1 && (args[0] == "on" || args[0] == "off") {
		return nil
	}
	return output.Usage(fmt.Sprintf("`%s` needs on or off.", cmd.CommandPath()),
		fmt.Sprintf("Usage: `%s`. Run `%s --help` for details.", cmd.UseLine(), cmd.CommandPath()))
}

type externalSharingResult struct {
	Org             string `json:"org"`
	ExternalSharing bool   `json:"external_sharing"`
	RemovedShares   int    `json:"removed_shares"`
}

func (r *externalSharingResult) Text() string {
	if r.ExternalSharing {
		return fmt.Sprintf("External sharing is on for %s.", r.Org)
	}
	shares := fmt.Sprintf("%d shares", r.RemovedShares)
	if r.RemovedShares == 1 {
		shares = "1 share"
	}
	return fmt.Sprintf("External sharing is off for %s. Removed %s outside %s.", r.Org, shares, r.Org)
}
