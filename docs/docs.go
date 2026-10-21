// Package docs holds the agent guide that `nimbus docs` prints. It is
// embedded in the binary so it always matches the installed version.
package docs

import _ "embed"

//go:embed agent-guide.md
var AgentGuide string
