# Nimbus CLI

`nimbus` takes a web app your agent already wrote and runs it at a private URL behind your company's sign-in. You share it with coworkers by email, and the app always knows who's viewing it, so it never needs its own login.

> **Status: early access (v0.0.x).** Nimbus is live at `api.nimbostrat.dev`. People sign in with their Google Workspace account, and their company's domain has to be enabled first. Progress and spec: [DESIGN.md](DESIGN.md).

## For agents
Install:
```sh
curl -fsSL https://raw.githubusercontent.com/wceriale/nimbus-cli/main/install.sh | sh
```
Then run `nimbus docs` and follow it. It covers logging in, deploying, sharing, and how to write an app that runs on Nimbus.

The core loop:
```sh
nimbus login                 # returns a URL: show it to your user, then run `nimbus login` again
nimbus deploy                # builds and ships the current directory, prints the URL
nimbus share lena@acme.com   # gives a coworker access
```
Output is JSON whenever stdout isn't a terminal. On failure, `error.code` says what went wrong and `error.hint` gives the next step.

To pin a version, or install somewhere other than `~/.local/bin`, set `NIMBUS_VERSION=v0.0.1` or `NIMBUS_INSTALL_DIR=…` before `sh`. The installer verifies the download against the release's `checksums.txt` and doesn't need sudo.

## For humans
The same commands work in a terminal, with readable output. Nimbus is invite-only during early access.

## Development
You need Go 1.24 or newer to build. Users don't need Go: releases are standalone binaries.
```sh
go test -race ./...
go run ./cmd/nimbus-fakeapi                 # a fake control plane on 127.0.0.1:8787
NIMBUS_API_URL=http://127.0.0.1:8787 go run ./cmd/nimbus login
```
See [DESIGN.md](DESIGN.md#development) for the code layout.

## License
[Apache-2.0](LICENSE)
