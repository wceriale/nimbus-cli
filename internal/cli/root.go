package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/wceriale/nimbus-cli/internal/output"
)

func (a *App) rootCmd() *cobra.Command {
	cobra.EnableCommandSorting = false
	root := &cobra.Command{
		Use:   "nimbus",
		Short: "Deploy web apps at a URL, private until you share them",
		Long: `nimbus takes a web app you already wrote and runs it at a URL. Every app starts
private, behind Google sign-in: share it by email, or make it public, and the
app knows who's viewing it. A work account deploys into its domain's
workspace; a personal account, such as Gmail, into its own personal space.

Agents: run ` + "`nimbus docs`" + ` first. Output is one JSON object on stdout whenever
stdout isn't a terminal.`,
		Version:       Version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetVersionTemplate("nimbus {{.Version}}\n")
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)
	root.PersistentFlags().BoolVar(&a.jsonFlag, "json", a.jsonFlag, "print one JSON object on stdout (the default when stdout isn't a terminal)")
	root.PersistentFlags().StringVar(&a.appFlag, "app", "", "app name (default: `name` in ./nimbus.yaml)")
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return output.Usage(capitalize(err.Error())+".", fmt.Sprintf("Run `%s --help` to see its flags.", cmd.CommandPath()))
	})
	root.AddCommand(
		a.loginCmd(),
		a.logoutCmd(),
		a.whoamiCmd(),
		a.deployCmd(),
		a.shareCmd(),
		a.unshareCmd(),
		a.orgCmd(),
		a.statusCmd(),
		a.logsCmd(),
		a.listCmd(),
		a.docsCmd(),
	)
	return root
}

func noArgs(cmd *cobra.Command, args []string) error { return argCount(cmd, args, 0, 0) }

func maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error { return argCount(cmd, args, 0, n) }
}

func argCount(cmd *cobra.Command, args []string, lo, hi int) error {
	if len(args) >= lo && (hi < 0 || len(args) <= hi) {
		return nil
	}
	return output.Usage(
		fmt.Sprintf("Wrong number of arguments for `%s`: got %d.", cmd.CommandPath(), len(args)),
		fmt.Sprintf("Usage: `%s`. Run `%s --help` for details.", cmd.UseLine(), cmd.CommandPath()))
}
