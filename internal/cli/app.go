// Package cli implements the nimbus commands. DESIGN.md is the spec.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wceriale/nimbus-cli/internal/config"
	"github.com/wceriale/nimbus-cli/internal/output"
)

// Version is set at release time:
// -ldflags "-X github.com/wceriale/nimbus-cli/internal/cli.Version=v0.1.0".
var Version = "dev"

// DefaultAPIURL is the hosted Nimbus API. NIMBUS_API_URL overrides it, and a
// build can change it with -ldflags "-X …/internal/cli.DefaultAPIURL=https://…".
var DefaultAPIURL = "https://api.nimbostrat.dev"

// App is one run of the CLI. Everything it touches outside the process is a
// field, so tests can swap it.
type App struct {
	Stdout      io.Writer
	Stderr      io.Writer
	Getenv      func(string) string
	WorkDir     string
	ConfigDir   string
	StdoutIsTTY bool
	HTTPClient  *http.Client
	OpenBrowser func(url string) error
	Sleep       func(time.Duration)
	Now         func() time.Time

	jsonFlag bool
	appFlag  string
}

// Main runs the CLI in the real process environment and returns the exit code.
func Main() int {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "nimbus:", err)
		return 1
	}
	dir, err := config.Dir(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nimbus:", err)
		return 1
	}
	a := &App{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Getenv:      os.Getenv,
		WorkDir:     wd,
		ConfigDir:   dir,
		StdoutIsTTY: isTerminal(os.Stdout),
		HTTPClient:  &http.Client{},
		OpenBrowser: openBrowser,
		Sleep:       time.Sleep,
		Now:         time.Now,
	}
	return a.Run(os.Args[1:])
}

// Run executes one command line and returns the exit code: 0 ok, 1 failed,
// 2 bad usage.
func (a *App) Run(args []string) int {
	// Usage errors happen before flags are parsed, and they must respect
	// --json too.
	a.jsonFlag = slices.Contains(args, "--json")
	root := a.rootCmd()
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	if err == nil {
		return 0
	}
	var e *output.Error
	if !errors.As(err, &e) {
		// Only cobra's own parse errors get here; commands return *output.Error.
		e = output.Usage(capitalize(err.Error())+".", "Run `nimbus --help` to see the commands.")
	}
	a.printer().Failure(e)
	return e.ExitCode()
}

// printer writes JSON when asked to, or when stdout isn't a terminal,
// because then the reader is almost certainly a program.
func (a *App) printer() *output.Printer {
	return &output.Printer{Stdout: a.Stdout, Stderr: a.Stderr, JSON: a.jsonFlag || !a.StdoutIsTTY}
}

// run adapts a command body to cobra: it prints the result and makes sure
// every failure is an *output.Error.
func (a *App) run(fn func(ctx context.Context, args []string) (any, error)) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		data, err := fn(cmd.Context(), args)
		if err != nil {
			var e *output.Error
			if errors.As(err, &e) {
				return e
			}
			return &output.Error{Code: "internal", Message: capitalize(err.Error()) + ".",
				Hint: "This is a local problem (permissions, disk) or a bug in nimbus."}
		}
		a.printer().Success(data)
		return nil
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// openBrowser tries to show url to the person at this machine. It is best
// effort: a cloud sandbox has no browser, and that's fine.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
		cmd = exec.Command("xdg-open", url)
	default:
		return errors.New("unsupported platform")
	}
	return cmd.Start()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func isCode(err error, code string) bool {
	var e *output.Error
	return errors.As(err, &e) && e.Code == code
}
