# `nimbus` CLI: design and tracker

`nimbus` is how agents use Nimbus. This file is both the CLI's spec and its feature tracker.

Last updated 2026-10-21. Status: design only; no release yet. Domains ending in `.example` are placeholders until real ones are chosen.

## Principles
1. **The agent is the user.** A human does one thing, once: approve `nimbus login` in a browser.
2. **Every command finishes on its own.** Nothing prompts. Output is JSON when stdout isn't a terminal.
3. **Every error says what to do next.** `error.hint` is a command to run or a one-line fix.
4. **Few commands, few flags.** The core loop is `login → deploy → share`. Anything beyond that has to earn its place.
5. **The docs ship inside the binary** (`nimbus docs`), so they always match the installed version.
6. **The CLI is public and holds no power.** All authorization happens on the server.

## Tracker
P0 is needed for the first end-to-end deploy. P1 must land before anyone outside the team uses it. Status values are todo, doing, and done. **"Done" means implemented and tested against `internal/fakeapi`.** Nothing has run against a real server yet, because there isn't one.

| # | Item | Pri | Status |
|---|---|---|---|
| 1 | Public repo: README, license (Apache-2.0), this doc | P0 | done |
| 2 | Go module and skeleton (`nimbus --version`, `nimbus --help`) | P0 | done |
| 3 | Release pipeline: GoReleaser in GitHub Actions on `v*` tags, with checksums | P0 | todo |
| 4 | `install.sh`: detect OS/arch, verify checksum, install to `~/.local/bin`, honor `NIMBUS_VERSION` | P0 | todo |
| 5 | Output contract: JSON when not a TTY, envelope, error codes and hints | P0 | done |
| 6 | `nimbus login`: two-step device flow, credentials file, `NIMBUS_TOKEN` | P0 | done |
| 7 | `nimbus whoami` | P0 | done |
| 8 | `nimbus deploy`: writes manifest if missing, ignore rules, upload, wait, log tail on failure | P0 | done |
| 9 | `nimbus status`, `nimbus list` | P0 | done |
| 10 | `nimbus share`, `nimbus unshare` | P0 | done |
| 11 | `nimbus logs` (runtime logs, plus `--build`) | P0 | done |
| 12 | `cli_outdated`: send `Nimbus-CLI-Version` on every request and surface the server's 426 | P0 | done |
| 13 | `docs/agent-guide.md` and `nimbus docs`, with a generated command reference | P0 | done |
| 14 | `--help` for every command, with one example and the JSON it returns | P0 | done |
| 15 | Agent usability eval (see [Docs for agents](#docs-for-agents)); needs a release and a server | P0 | todo |
| 21 | Fake control plane (`internal/fakeapi`, `cmd/nimbus-fakeapi`) for tests and trying the CLI by hand | P0 | done |
| 16 | `nimbus destroy`, `nimbus rollback`, `nimbus logout`, `nimbus upgrade` | P1 | todo |
| 17 | `nimbus share --org` (anyone in the org who has the link) | P1 | todo |
| 18 | `scopes` in the manifest, plus consent errors | P1 | todo |
| 19 | Claude Code skill and AGENTS.md snippet, generated from the guide | P1 | todo |
| 20 | CI check that the guide's examples still run | P1 | todo |

## Install
Public GitHub repos need no auth, so an agent can install with one curl:
```sh
curl -fsSL https://raw.githubusercontent.com/wceriale/nimbus-cli/main/install.sh | sh
```
What `install.sh` does:
1. Detects the OS and architecture.
2. Downloads `https://github.com/wceriale/nimbus-cli/releases/latest/download/nimbus_<os>_<arch>.tar.gz`.
3. Verifies it against `checksums.txt` from the same release.
4. Installs to `~/.local/bin` without sudo, since agents usually can't use it. Override the location with `NIMBUS_INSTALL_DIR`, or pin a version with `NIMBUS_VERSION=v0.1.0`.
5. Prints 3 lines: where it installed, then `nimbus login`, then `nimbus docs`.

Notes:
- `releases/latest/download/…` is a plain redirect, not a GitHub API call, so it isn't subject to the 60 requests/hour limit on unauthenticated API calls. It needs `-L`, which `-fsSL` includes.
- Go users can run `go install github.com/wceriale/nimbus-cli/cmd/nimbus@latest`.
- Releases are built by GoReleaser on every `v*` tag, for darwin and linux on amd64 and arm64.
- Agents in network sandboxes need `github.com`, `raw.githubusercontent.com`, and GitHub's release-asset host on their allowlist.
- This repo holds only the CLI, `install.sh`, the agent docs, examples, and this file. The server is private.

## Commands
### P0
```
nimbus login               device-flow login; the only step that needs a human
nimbus whoami              email, org, API URL, CLI version; use as a preflight check
nimbus deploy [dir]        build and ship a directory (default .), then print the URL
nimbus share <email>...    give viewers access
nimbus unshare <email>...  take access away
nimbus status [app]        URL, version, state (building|live|failed), who has access
nimbus logs [app]          runtime logs: --since 1h --limit 200; --build for the last build log
nimbus list                apps you own
nimbus docs                the agent guide, printed as markdown
```
- **Global flags:** `--json`, `--app <name>` (defaults to `name` in `./nimbus.yaml`), `--help`, `--version`.
- **Environment variables:** `NIMBUS_TOKEN` overrides the stored login. `NIMBUS_API_URL` sets the API URL. Until Nimbus has a real domain there's no built-in default, so it's required, and a release sets the default with `-ldflags`. Credentials are only ever sent to the API that issued them.

### How the core commands behave
**`nimbus login`** runs an RFC 8628 device flow against the Nimbus control plane, which handles the company sign-in (Google or Okta). The same flow works for local, headless, and cloud agents.
- **In a terminal:** it prints a URL and a code, opens the browser, and waits.
- **Not in a terminal (agents):** it returns at once:
  ```json
  {"ok":true,"data":{"status":"pending","verification_url":"https://nimbus.example/device?code=WDJB-MJHT","user_code":"WDJB-MJHT","expires_in":600}}
  ```
  The agent shows the user the URL and then runs `nimbus login` again. That second call returns `{"status":"logged_in","email":"…","org":"…"}` once the user approves, or the same pending code if they haven't yet. If the user is already logged in, it returns `logged_in` straight away.
- **Why it doesn't block for agents:** an agent can't show the user a URL while its own command is still running.
- **Credentials** live in `~/.config/nimbus/credentials.json` (mode 0600) and refresh silently. A file works in containers and CI. Keychain support can come later.

**`nimbus deploy`**
- **No manifest:** it writes `nimbus.yaml`, taking `name` from the directory name. There's no `nimbus init`.
- **Upload:** it respects `.gitignore` and always skips `.git`, `node_modules`, `.venv`, `__pycache__`, and **`.env*`**, because apps hold no secrets. The limit is 100 MB.
- **Build:** it uses the Dockerfile if there is one, otherwise buildpacks (Python, Node, Go).
- **Waiting:** it waits until the new version serves traffic, up to 10 minutes. A build takes 1–3 minutes, so the agent guide tells agents to raise their command timeout, or to use `--no-wait` and then poll `nimbus status`. Once the upload finishes, killing the CLI doesn't stop the deploy.
- **Unchanged source:** if the source matches the live version, it creates no new version and returns `"unchanged": true`.
- **Who can see it:** after the first deploy, only the deployer can open the app, and the output says so:
  ```json
  {"ok":true,"data":{"app":"renewal-desk","url":"https://renewal-desk.acme.nimbusapps.example","version":3,"state":"live","shared_with":["you@acme.com"],"manifest_created":false}}
  ```
- **Failures** include the end of the log, so the agent doesn't need a second call:
  ```json
  {"ok":false,"error":{"code":"start_failed","message":"Container didn't listen on port 8080 within 60s","hint":"Listen on 0.0.0.0:$PORT. Run `nimbus logs --build` for the full log.","log_tail":["…last 50 lines…"]}}
  ```

**`nimbus share` / `nimbus unshare`**
- **Who:** v0.1 only accepts email addresses in your own org.
- **Output:** the full access list, plus the URL to send people. v0.1 sends no notification.
- **Where sharing lives:** only on the server. Redeploying never changes who has access, and the manifest has no `share:` field.

### P1
- `nimbus destroy [app] --yes`
- `nimbus rollback [app] [--to <version>]`
- `nimbus share --org`
- `nimbus logout`
- `nimbus upgrade`, which re-runs the installer's download for the latest release

### Later
- Sharing with groups (`share group:<name>`, from Okta groups)
- `--role editor`
- `nimbus mcp`
- A Homebrew tap
- Windows support
- `nimbus env` for config that isn't secret
- Share notifications

## Output contract
- **Stdout:** when `--json` is set or stdout isn't a TTY, stdout carries **exactly one JSON object** per run. Progress lines go to stderr. A human at a terminal gets plain text.
- **Envelope:** success is `{"ok":true,"data":{…}}`. Failure is `{"ok":false,"error":{"code","message","hint",…}}`. Many results also carry `next`, a sentence telling the agent what to do next.
- **Exit codes:** `0` means ok, `1` means failed (read `error.code`), `2` means bad usage. Agents should branch on `error.code`, not on the exit code.
- **Stability:** a code never changes meaning once released. New codes can be added.
- **Exceptions:** `nimbus docs`, `--help`, and `--version` always print text, because their reader wants text.
- **Server errors pass straight through:** the CLI forwards the server's `code`, `message`, and `hint` unchanged.

| `error.code` | When | What `hint` tells the agent |
|---|---|---|
| `not_logged_in` | Credentials are missing, expired, or rejected | Run `nimbus login` and show the user the URL |
| `login_expired`, `login_denied` | The device code timed out, or the user denied it | Run `nimbus login` again |
| `org_not_enabled` | The user's org isn't onboarded | Tell the user Nimbus is invite-only for now |
| `api_url_not_set`, `invalid_api_url` | No usable `NIMBUS_API_URL` | Set it |
| `cli_outdated` | The server requires a newer CLI | Re-run the install one-liner |
| `invalid_manifest`, `invalid_name` | `nimbus.yaml` or the app name is invalid | Fix the field it names |
| `name_taken` | Someone else in the org owns that name | Change `name` in `nimbus.yaml` |
| `upload_too_large` | Over 100 MB after ignore rules | Add `details.largest` paths to `.gitignore` |
| `build_failed` | The build step failed | Read `log_tail`, fix the problem, deploy again |
| `start_failed` | The container never served on `$PORT` | Listen on `0.0.0.0:$PORT` |
| `deploy_timeout` | The deploy took longer than 10 minutes | Run `nimbus status` |
| `app_not_found` | No such app, or it isn't yours | Check `nimbus list` |
| `app_not_specified` | No app argument, no `--app`, and no `./nimbus.yaml` | Pass `--app` or run from the app's directory |
| `invalid_email`, `share_outside_org` | An address is malformed or outside the org | Fix the addresses in `details.emails` |
| `network_error`, `unexpected_response` | The API didn't answer, or answered nonsense | Retry |
| `usage` | Unknown command, bad flag, or wrong number of arguments | Run `nimbus <command> --help` |
| `internal` | A local problem (permissions, disk) or a bug | Report it |

## Manifest
This is the whole file in v0.1:
```yaml
name: renewal-desk      # required; becomes renewal-desk.<org>.<apps-domain>
port: 8080              # optional; the app receives it as $PORT
scopes:                 # optional, P1: data the app reads as the viewer
  - google.sheets.read
```
The runtime is detected, and sharing is a command.

## The app contract
Without docs, this is what agents get wrong, so `nimbus docs` opens with it. Items marked [planned] arrive after the first release.
1. Listen on `0.0.0.0:$PORT`.
2. Don't build a login. The viewer's email is in the `X-Viewer-Email` request header. Trust it, because only Nimbus can reach the app. If the header is missing, the app is running locally.
3. Don't hold secrets: no API keys in the code, and `.env*` files aren't uploaded. Reach data through `$NIMBUS_GATEWAY` with the `X-Viewer-Token` header, so every read runs as the viewer. [planned]
4. Treat the disk as temporary. The app also sleeps when idle, and a cold start takes a few seconds. Keep state in `$DATABASE_URL`. [planned]
5. Use plain HTTP. There are no websockets, and requests time out after 5 minutes.
6. Outbound internet is blocked; only the gateway is reachable. [planned]

## Docs for agents
Everything comes from one source and is published in several places:

| Where | Who reads it, and when | What it contains |
|---|---|---|
| `nimbus docs` | The agent, right after install | `docs/agent-guide.md`, embedded with `go:embed`, plus a command reference generated from the CLI's own command definitions so it can't drift |
| `nimbus <cmd> --help` | The agent, in the middle of a task | Usage, flags, one example, and the JSON it returns |
| README.md | An agent or human landing on the repo | Opens with an agents section: the install line, then "run `nimbus docs`" |
| `install.sh` output | The agent, right after install | Where it installed, then `nimbus login`, then `nimbus docs` |
| Skill / AGENTS.md snippet (P1) | Agents in repos that use Nimbus | When to use Nimbus, then "run `nimbus docs`" |

Outline of `docs/agent-guide.md`:
1. What Nimbus does and when to use it (3 lines)
2. Quickstart: `nimbus login`, then `nimbus deploy`, then `nimbus share`
3. How an agent logs in (the two-step flow)
4. The app contract
5. Command reference (generated)
6. Errors and what to do about each
7. Limits: upload size, command timeouts, no websockets, sharing only inside the org

**The agent usability eval** checks whether the docs work, and it gates the v0.1 release:
- **Fixture apps (3):** a Flask app with no Dockerfile, a Next.js app, and a Dockerfile app hardcoded to port 5000.
- **Prompt:** one prompt and nothing else: "Install the Nimbus CLI from github.com/wceriale/nimbus-cli, deploy this app, and share it with <email>."
- **Agents:** run it in Claude Code, Codex, and Cursor.
- **Pass:** the app is live and shared, and the only human step was approving the login.
- **Tracked:** pass rate, tool calls per run, and which errors agents hit. Every failure turns into a doc fix or a better `hint`.

## API the CLI needs (control plane, v1)
`internal/api` is the client and `internal/fakeapi` is an in-memory server for the same contract. Every request sends `Nimbus-CLI-Version`, and the server can answer `426 cli_outdated`. The `/oauth` endpoints follow RFC 8628 and RFC 6749, so an off-the-shelf OAuth server can implement them.
```
P0
POST   /oauth/device/code                       start login (RFC 8628)
POST   /oauth/token                             finish login (device grant); refresh
GET    /v1/me                                   login, whoami
GET    /v1/apps                                 list
PUT    /v1/apps/{app}                           create if missing (deploy)
GET    /v1/apps/{app}                           status, including the access list
POST   /v1/apps/{app}/uploads                   signed URL for the source tarball
PUT    {upload_url}                             signed storage URL, sent without the API token
POST   /v1/apps/{app}/versions                  build and deploy an upload
GET    /v1/apps/{app}/versions/{n|latest}       deploy polling, status
GET    /v1/apps/{app}/versions/{n}/build-log    logs --build
GET    /v1/apps/{app}/logs?since=&limit=        logs
PATCH  /v1/apps/{app}/shares                    share, unshare: {"add":[…],"remove":[…]}, all or nothing

P1
POST   /oauth/revoke                            logout (RFC 7009)
DELETE /v1/apps/{app}                           destroy
GET    /v1/apps/{app}/versions                  rollback targets
POST   /v1/apps/{app}/rollback                  rollback
```

## Development
```sh
go test -race ./...                        # unit tests, plus end-to-end tests against the fake API
go run ./cmd/nimbus-fakeapi                # fake control plane on 127.0.0.1:8787
export NIMBUS_API_URL=http://127.0.0.1:8787
go run ./cmd/nimbus login                  # approve at the printed URL, then run it again
```
Layout:
- `cmd/nimbus`: the CLI.
- `internal/cli`: the commands.
- `internal/api`: the HTTP client.
- `internal/bundle`: packs the upload.
- `internal/manifest`: reads and writes `nimbus.yaml`.
- `internal/config`: the credentials file.
- `internal/output`: the JSON contract.
- `docs/agent-guide.md`: what `nimbus docs` prints.

## Decisions
| Date | Decision | Why |
|---|---|---|
| 2026-10-21 | Name: Nimbus. Binary `nimbus`, manifest `nimbus.yaml`, env vars `NIMBUS_*` | Chosen working name. No short alias, because agents don't mind typing. |
| 2026-10-21 | Public repo `wceriale/nimbus-cli` under Apache-2.0; installs with curl from GitHub Releases | Agents can install it with nothing set up. Apache-2.0 includes an explicit patent grant. The server stays private. |
| 2026-10-21 | Go, as one static binary | No Node or Python needed on the agent's machine. |
| 2026-10-21 | Login is a device flow only, in two steps for agents | It's one flow for local, headless, and cloud agents, and agents can relay the URL. |
| 2026-10-21 | Sharing is a command, not a manifest field | It works like a Google Doc, and redeploys can't change who has access. |
| 2026-10-21 | `nimbus deploy` writes the manifest, so there's no `nimbus init` | One less command to learn. |
| 2026-10-21 | JSON whenever stdout isn't a TTY, exit codes 0/1/2, and `error.code` as the contract | Agents don't have to remember a flag, and there's one thing to branch on. |
| 2026-10-21 | `nimbus mcp` is deferred | Claude Code, Codex, and Cursor all have a shell. A second interface would double the docs and the tests. |
| 2026-10-21 | The version check is a header on every request, not a separate call | One fewer round trip, and the server can't forget to check. |
| 2026-10-21 | Sharing changes go through one all-or-nothing `PATCH` | `nimbus share a b c` is one call, and a bad address doesn't leave half the list applied. |
| 2026-10-21 | Non-owners get `404 app_not_found`, never `403` | App names don't leak across the org. `permission_denied` is reserved for when roles exist. |

## Open questions
- Real domains for the API and for apps. Until then, docs use `.example`, and the CLI must not ship a default API URL on a domain we don't own.
- "Nimbus" is a crowded name: Mozilla's experimentation platform, Status's Ethereum client, and Nimbus Note all use it. It's fine as a working name, but check trademarks and domain availability before launch.
