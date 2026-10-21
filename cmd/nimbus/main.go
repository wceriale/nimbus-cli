// Command nimbus is the Nimbus CLI. See DESIGN.md for the spec, and run
// `nimbus docs` for the agent guide.
package main

import (
	"os"

	"github.com/wceriale/nimbus-cli/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
