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
	var noWait bool
	cmd := &cobra.Command{
		Use:   "deploy [dir]",
		Short: "Build and ship a directory (default: the current one), then print its URL",
		Long: `Build and ship an app directory (default: the current one) and wait until the
new version serves traffic. That usually takes 1–3 minutes and is capped at 10,
so give this command a timeout of at least 10 minutes, or pass --no-wait and
poll nimbus status.

The first deploy writes nimbus.yaml, naming the app after the directory (or
--app). The upload skips .gitignore matches, .git, node_modules, .venv,
__pycache__ and every .env* file. A Dockerfile is used if there is one;
otherwise the runtime (Python, Node, Go) is detected.

Deploying the same source again returns "unchanged": true and builds nothing.
After the first deploy, only you can open the app: share it with nimbus share.`,
		Example: `  $ nimbus deploy
  {"ok":true,"data":{"app":"renewal-desk","url":"https://renewal-desk.acme.nimbus-apps.dev","version":1,"state":"live","unchanged":false,"manifest_created":true,"shared_with":["you@acme.com"],"next":"Only you can open it. ..."}}

  # On failure, error.log_tail has the end of the log:
  {"ok":false,"error":{"code":"start_failed","message":"Container didn't listen on port 8080 within 60s","hint":"Listen on 0.0.0.0:$PORT. ...","log_tail":["..."]}}`,
		Args: maxArgs(1),
		RunE: a.run(func(ctx context.Context, args []string) (any, error) { return a.deploy(ctx, args, noWait) }),
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return once the build is queued; follow it with nimbus status")
	return cmd
}

type deployResult struct {
	App             string   `json:"app"`
	URL             string   `json:"url"`
	Version         int      `json:"version"`
	State           string   `json:"state"`
	Unchanged       bool     `json:"unchanged"`
	ManifestCreated bool     `json:"manifest_created"`
	SharedWith      []string `json:"shared_with,omitempty"`
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

func (a *App) deploy(ctx context.Context, args []string, noWait bool) (any, error) {
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
	// Check the login before writing anything into the user's directory.
	sess, err := a.session()
	if err != nil {
		return nil, err
	}

	m, err := manifest.Load(dir)
	created := false
	switch {
	case errors.Is(err, fs.ErrNotExist):
		name := firstNonEmpty(a.appFlag, manifest.NameFromDir(dir))
		if !manifest.ValidName(name) {
			return nil, manifest.InvalidName(name, "Pass a name with `--app <name>`: 3–40 lowercase letters, digits and hyphens, starting with a letter.")
		}
		if err := manifest.Write(dir, name); err != nil {
			return nil, err
		}
		m, created = &manifest.Manifest{Name: name}, true
	case err != nil:
		return nil, err
	}
	name := m.Name
	if a.appFlag != "" {
		if !manifest.ValidName(a.appFlag) {
			return nil, manifest.InvalidName(a.appFlag, "")
		}
		name = a.appFlag
	}

	p := a.printer()
	b, err := bundle.Create(dir)
	if err != nil {
		return nil, err
	}
	p.Progress("Packaged %d files (%s).", b.Files, humanBytes(len(b.Data)))

	c := sess.client
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
	res := &deployResult{App: name, URL: app.URL, Version: v.Number, State: v.State, Unchanged: unchanged,
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
	res.URL, res.SharedWith, res.Version, res.State = app.URL, app.SharedWith, v.Number, v.State
	if len(app.SharedWith) <= 1 {
		res.Next = "Only you can open it. Run `nimbus share <email>` to give coworkers access."
	}
	return res, nil
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
