# Nimbus CLI

`nimbus` takes a web app your agent already wrote and runs it at a URL. A work project runs privately behind your company's sign-in: you share it with coworkers by email, and the app always knows who's viewing it, so it never needs its own login. A personal project runs under your personal Google account, such as Gmail, and is public.

> **Status: early access (v0.0.x).** Nimbus is live at `api.nimbostrat.dev`. People sign in with Google. Personal Google accounts, such as Gmail, host personal projects, which are always public. Company Workspace accounts host private work projects, and the company's domain has to be enabled first. Progress and spec: [DESIGN.md](DESIGN.md).

## For agents
Install:
```sh
curl -fsSL https://raw.githubusercontent.com/wceriale/nimbus-cli/main/install.sh | sh
```
Then run `nimbus docs` and follow it. It covers logging in, deploying, sharing, and how to write an app that runs on Nimbus.

The core loop:
```sh
nimbus login                 # returns a URL: show it to your user, then run `nimbus login` again
nimbus deploy --work         # builds and ships the current directory, prints the URL
nimbus share lena@acme.com   # gives a coworker access
nimbus share --public        # or lets anyone open it, without signing in
```
The first deploy asks the agent to check with the user: is this a work project (`--work`) or a personal one (`--personal`)? The answer is saved in `nimbus.yaml`. Work apps start private, and `nimbus unshare --public` makes a public one private again. Personal apps are always public. A public app gets no `X-Viewer-Email`, so it can't tell who's viewing.

Output is JSON whenever stdout isn't a terminal. On failure, `error.code` says what went wrong and `error.hint` gives the next step.

To pin a version, or install somewhere other than `~/.local/bin`, set `NIMBUS_VERSION=v0.0.3` or `NIMBUS_INSTALL_DIR=…` before `sh`. The installer verifies the download against the release's `checksums.txt` and doesn't need sudo.

## For humans
The same commands work in a terminal, with readable output. During early access, work projects need your company's domain to be enabled.

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
