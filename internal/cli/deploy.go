package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/wceriale/nimbus-cli/internal/api"
	"github.com/wceriale/nimbus-cli/internal/bundle"
	"github.com/wceriale/nimbus-cli/internal/manifest"
	"github.com/wceriale/nimbus-cli/internal/output"
)

const (
	deployTimeout = 10 * time.Minute
	pollInterval  = 2 * time.Second
)

func (a *App) deployCmd() *cobra.Command {
	var noWait, personal, work bool
	cmd := &cobra.Command{
		Use:   "deploy [dir]",
		Short: "Build and ship a directory (default: the current one), then print its URL",
		Long: `Build and ship an app directory (default: the current one) and wait until the
new version serves traffic. That usually takes 1–3 minutes and is capped at 10,
so give this command a timeout of at least 10 minutes, or pass --no-wait and
poll nimbus status.

A project is personal or work, and only the user can say which. Until
nimbus.yaml has an account, deploy fails with "ask_user": ask the user, then
pass --personal or --work. Personal projects are public: anyone with the URL can
open them. Work projects are private, behind the company's Google sign-in, for
the people they're shared with. The answer is saved in nimbus.yaml, and deploy
checks it against the account you're logged in with.

The first deploy writes nimbus.yaml, naming the app after the directory (or
--app). The upload skips .gitignore matches, .git, node_modules, .venv,
__pycache__ and every .env* file. A Dockerfile is used if there is one;
otherwise the runtime (Python, Node, Go) is detected.

Deploying the same source again returns "unchanged": true and builds nothing.
After the first deploy of a work project, only you can open it: share it with
nimbus share.`,
		Example: `  # The first deploy, before the user has said personal or work:
  $ nimbus deploy
  {"ok":false,"error":{"code":"ask_user","message":"Ask the user whether this is a personal or a work project, and don't choose for them. ...","hint":"Then run ` + "`nimbus deploy --personal` or `nimbus deploy --work`" + `. ..."}}

  $ nimbus deploy --work
  {"ok":true,"data":{"app":"renewal-desk","url":"https://renewal-desk.acme.nimbus-apps.dev","account":"work","version":1,"state":"live","unchanged":false,"manifest_created":true,"shared_with":["you@acme.com"],"next":"Only you can open it. ..."}}

  # On failure, error.log_tail has the end of the log:
  {"ok":false,"error":{"code":"start_failed","message":"Container didn't listen on port 8080 within 60s","hint":"Listen on 0.0.0.0:$PORT. ...","log_tail":["..."]}}`,
		Args: maxArgs(1),
		RunE: a.run(func(ctx context.Context, args []string) (any, error) {
			if personal && work {
				return nil, output.Usage("Pass --personal or --work, not both.", "Ask the user which one this project is.")
			}
			var account string
			switch {
			case personal:
				account = manifest.Personal
			case work:
				account = manifest.Work
			}
			return a.deploy(ctx, args, noWait, account)
		}),
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return once the build is queued; follow it with nimbus status")
	cmd.Flags().BoolVar(&personal, "personal", false, "the user said it's a personal project: public, under their personal Google account")
	cmd.Flags().BoolVar(&work, "work", false, "the user said it's a work project: private, in their company's org")
	return cmd
}

type deployResult struct {
	App             string   `json:"app"`
	URL             string   `json:"url"`
	Account         string   `json:"account"` // personal or work
	Version         int      `json:"version"`
	State           string   `json:"state"`
	Unchanged       bool     `json:"unchanged"`
	ManifestCreated bool     `json:"manifest_created"`
	SharedWith      []string `json:"shared_with,omitempty"`
	Public          bool     `json:"public,omitempty"`
	EnvFilesSkipped []string `json:"env_files_skipped,omitempty"`
	Next            string   `json:"next,omitempty"`
}

func (r *deployResult) Text() string {
	s := fmt.Sprintf("%s version %d is %s: %s", r.App, r.Version, r.State, r.URL)
	if r.Unchanged {
		s += " (unchanged)"
	}
	if r.Next != "" {
		s += "\n" + r.Next
	}
	return s
}

func (a *App) deploy(ctx context.Context, args []string, noWait bool, flagAccount string) (any, error) {
	dir := a.WorkDir
	if len(args) == 1 {
		dir = args[0]
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(a.WorkDir, dir)
		}
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, output.Errorf("not_a_directory", "Pass the app's directory, e.g. `nimbus deploy .`.", "%s isn't a directory.", dir)
	}

	// Settle the manifest and whether the project is personal or work before
	// the login check, so the agent asks the user first, and before writing
	// anything into the user's directory.
	m, err := manifest.Load(dir)
	created := errors.Is(err, fs.ErrNotExist)
	switch {
	case created:
		name := firstNonEmpty(a.appFlag, manifest.NameFromDir(dir))
		if !manifest.ValidName(name) {
			return nil, manifest.InvalidName(name, "Pass a name with `--app <name>`: 3–40 lowercase letters, digits and hyphens, starting with a letter.")
		}
		m = &manifest.Manifest{Name: name}
	case err != nil:
		return nil, err
	}
	saveAccount := m.Account == ""
	account, err := projectAccount(m.Account, flagAccount)
	if err != nil {
		return nil, err
	}
	name := m.Name
	if a.appFlag != "" {
		if !manifest.ValidName(a.appFlag) {
			return nil, manifest.InvalidName(a.appFlag, "")
		}
		name = a.appFlag
	}

	sess, err := a.session()
	if err != nil {
		return nil, err
	}
	c := sess.client
	me, err := c.Me(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkAccount(account, me); err != nil {
		return nil, err
	}
	switch {
	case created:
		err = manifest.Write(dir, m.Name, account)
	case saveAccount:
		err = manifest.SetAccount(dir, account)
	}
	if err != nil {
		return nil, err
	}

	p := a.printer()
	b, err := bundle.Create(dir)
	if err != nil {
		return nil, err
	}
	p.Progress("Packaged %d files (%s).", b.Files, humanBytes(len(b.Data)))

	app, err := c.EnsureApp(ctx, name)
	if err != nil {
		return nil, err
	}
	up, err := c.CreateUpload(ctx, name, b.SHA256, len(b.Data))
	if err != nil {
		return nil, err
	}
	if !up.AlreadyUploaded {
		p.Progress("Uploading…")
		if err := c.PutUpload(ctx, up, b.Data); err != nil {
			return nil, err
		}
	}
	v, unchanged, err := c.CreateVersion(ctx, name, up.UploadID)
	if err != nil {
		return nil, err
	}
	res := &deployResult{App: name, URL: app.URL, Account: account, Version: v.Number, State: v.State, Unchanged: unchanged,
		ManifestCreated: created, EnvFilesSkipped: b.SkippedEnv}
	if noWait && !unchanged {
		res.Next = fmt.Sprintf("Run `nimbus status %s` until state is live or failed.", name)
		return res, nil
	}
	if !unchanged {
		if v, err = a.waitForVersion(ctx, c, name, v); err != nil {
			return nil, err
		}
	}
	if app, err = c.GetApp(ctx, name); err != nil {
		return nil, err
	}
	res.URL, res.SharedWith, res.Public, res.Version, res.State = app.URL, app.SharedWith, app.Public, v.Number, v.State
	switch {
	case account == manifest.Personal:
		res.Next = "It's a personal project, so it's public: anyone with the URL can open it without signing in, and the app gets no X-Viewer-Email."
	case !app.Public && len(app.SharedWith) <= 1:
		res.Next = "Only you can open it. Run `nimbus share <email>` to give coworkers access."
	}
	return res, nil
}

// projectAccount settles whether the project is personal or work: what
// nimbus.yaml says, else the flag. If neither says, only the user can.
func projectAccount(saved, flag string) (string, error) {
	switch {
	case saved != "" && flag != "" && flag != saved:
		return "", output.Errorf("account_conflict",
			fmt.Sprintf("Drop --%s. If the user says this is a %s project after all, change `account` in nimbus.yaml to %s and deploy again.", flag, flag, flag),
			"nimbus.yaml says this is a %s project, but --%s was passed.", saved, flag)
	case saved != "":
		return saved, nil
	case flag != "":
		return flag, nil
	}
	return "", output.AskUser(
		"Ask the user whether this is a personal or a work project, and don't choose for them. "+
			"Personal projects are public: anyone with the URL can open them. "+
			"Work projects are private: only the people the user shares them with can open them, behind their company's Google sign-in.",
		"Then run `nimbus deploy --personal` or `nimbus deploy --work`. The answer is saved in nimbus.yaml, so later deploys don't ask.")
}

// checkAccount makes sure the login fits the project: personal projects go
// under a personal Google account, and work projects into a company's org.
func checkAccount(account string, me *api.Me) error {
	switch {
	case account == manifest.Personal && !me.Org.Personal:
		return output.Errorf("wrong_account",
			"Run `nimbus logout`, then `nimbus login`, and ask the user to sign in with a personal Google account, such as Gmail.",
			"This is a personal project, but you're logged in with %s, a work account.", me.Email)
	case account == manifest.Work && me.Org.Personal:
		return output.Errorf("wrong_account",
			"Run `nimbus logout`, then `nimbus login`, and ask the user to sign in with their work Google account. "+
				"If their company isn't on nimbus, ask them whether to make this a personal project instead: it would be public.",
			"This is a work project, but you're logged in with %s, a personal account on nimbus. "+
				"A company account counts as personal when the company isn't set up on nimbus.", me.Email)
	}
	return nil
}

// waitForVersion polls until the version is live or failed.
func (a *App) waitForVersion(ctx context.Context, c *api.Client, name string, v *api.Version) (*api.Version, error) {
	p := a.printer()
	deadline := a.Now().Add(deployTimeout)
	last := ""
	for {
		if v.State != last {
			p.Progress("Version %d: %s.", v.Number, v.State)
			last = v.State
		}
		switch v.State {
		case "live":
			return v, nil
		case "failed":
			return nil, versionFailed(name, v)
		case "queued", "building", "deploying":
		default:
			return nil, output.Errorf("deploy_interrupted", fmt.Sprintf("Run `nimbus status %s` to see what's live.", name),
				"Version %d ended in state %q.", v.Number, v.State)
		}
		if a.Now().After(deadline) {
			return nil, output.Errorf("deploy_timeout", fmt.Sprintf("The deploy is still running on the server. Run `nimbus status %s` to follow it.", name),
				"Version %d didn't finish within %s.", v.Number, deployTimeout)
		}
		a.Sleep(pollInterval)
		next, err := c.GetVersion(ctx, name, strconv.Itoa(v.Number))
		if err != nil {
			return nil, err
		}
		v = next
	}
}

var failureHints = map[string]string{
	"build_failed": "Read log_tail, fix the build, then run `nimbus deploy` again.",
	"start_failed": "Make the app listen on 0.0.0.0:$PORT, then run `nimbus deploy` again.",
}

func versionFailed(name string, v *api.Version) *output.Error {
	e := &output.Error{
		Code:    "build_failed",
		Message: fmt.Sprintf("Version %d failed.", v.Number),
		LogTail: v.LogTail,
		Details: map[string]any{"app": name, "version": v.Number},
	}
	if v.Error != nil {
		e.Code = firstNonEmpty(v.Error.Code, e.Code)
		e.Message = firstNonEmpty(v.Error.Message, e.Message)
		e.Hint = v.Error.Hint
	}
	e.Hint = firstNonEmpty(e.Hint, failureHints[e.Code], "Fix the problem, then run `nimbus deploy` again.")
	e.Hint += fmt.Sprintf(" Full log: `nimbus logs %s --build`.", name)
	return e
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
