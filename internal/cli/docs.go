package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wceriale/nimbus-cli/docs"
)

func (a *App) docsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "docs",
		Short: "Print the agent guide: logging in, deploying, sharing, and writing apps for Nimbus",
		Long: `Print the agent guide as markdown, followed by a command reference generated
from this build of the CLI. Unlike other commands it prints markdown even when
stdout isn't a terminal; pass --json to get {"markdown": "..."}.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			md := docs.AgentGuide + "\n" + reference(cmd.Root())
			if a.jsonFlag {
				a.printer().Success(map[string]string{"markdown": md})
				return nil
			}
			fmt.Fprint(a.Stdout, md)
			return nil
		},
	}
}

// reference documents every command from its own definition, so the guide
// can't drift from the CLI.
func reference(root *cobra.Command) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Command reference\n\nGenerated from nimbus %s.\n\nGlobal flags:\n\n```\n%s```\n", Version, root.PersistentFlags().FlagUsages())
	for _, c := range root.Commands() {
		if c.Hidden || c.Name() == "help" {
			continue
		}
		fmt.Fprintf(&b, "\n### `nimbus %s`\n\n%s\n", c.Use, firstNonEmpty(c.Long, c.Short))
		if f := c.LocalNonPersistentFlags().FlagUsages(); f != "" {
			fmt.Fprintf(&b, "\nFlags:\n\n```\n%s```\n", f)
		}
		if c.Example != "" {
			fmt.Fprintf(&b, "\nExample:\n\n```\n%s\n```\n", c.Example)
		}
	}
	return b.String()
}
