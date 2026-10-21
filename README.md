# Nimbus CLI

`nimbus` takes a web app your agent already wrote and runs it at a private URL behind your company's sign-in. You share it with coworkers by email, and every data access inside the app runs as the person viewing it.

> **Status: design stage, no release yet.** The install command below won't work until v0.1 ships. Progress and spec: [DESIGN.md](DESIGN.md).

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

## For humans
The same commands work in a terminal, with readable output. Nimbus is invite-only during early access.

## License
[Apache-2.0](LICENSE)
