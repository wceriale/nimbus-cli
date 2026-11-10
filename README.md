# Nimbus CLI

`nimbus` takes a web app your agent already wrote and runs it at a URL. Every app starts private, behind Google sign-in: you share it by email, or make it public, and the app always knows who's viewing it, so it never needs its own login. Apps from your work account go into your company's workspace; apps from a personal account, such as Gmail, go into your personal space.

> **Status: early access (v0.0.x).** Nimbus is live at `api.nimby.run`. People sign in with Google. During private testing, a work domain has to be enabled before its people can sign in; personal accounts, such as Gmail, always can. The free plan has 5 apps per workspace or personal space, and 5 people per app. Progress and spec: [DESIGN.md](DESIGN.md).

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
nimbus share lena@acme.com   # gives someone access
nimbus share --public        # or lets anyone open it, without signing in
```
The account you're logged in with decides where an app goes: your work domain's workspace, or your personal space. The first deploy records it as `org:` in `nimbus.yaml`. Every app starts private, and `nimbus unshare --public` makes a public one private again. Personal apps can be shared with any Google account; work apps with people in your domain, unless the workspace owner runs `nimbus org external-sharing on`. A public app gets no `X-Viewer-Email`, so it can't tell who's viewing.

Output is JSON whenever stdout isn't a terminal. On failure, `error.code` says what went wrong and `error.hint` gives the next step.

To pin a version, or install somewhere other than `~/.local/bin`, set `NIMBUS_VERSION=v0.0.3` or `NIMBUS_INSTALL_DIR=…` before `sh`. The installer verifies the download against the release's `checksums.txt` and doesn't need sudo.

## For humans
The same commands work in a terminal, with readable output. During private testing, your work domain has to be enabled before you can sign in with it.

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
