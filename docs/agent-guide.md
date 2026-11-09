# Nimbus agent guide

Nimbus runs a web app at a URL. A work project is private, behind the company's Google sign-in, and shared by email; the app is told who's viewing it, so it never needs its own login. A personal project runs under a personal Google account, such as Gmail, and is public. Use Nimbus when the user wants other people to use something you built: a dashboard, a form, a small tool. Nimbus hosts the code you already wrote, so there's nothing to rebuild in another product.

**How to read the output.** When stdout isn't a terminal, or with `--json`, every command prints exactly one JSON object on stdout:
- **Success:** `{"ok": true, "data": {…}}` with exit code 0.
- **Failure:** `{"ok": false, "error": {"code": "…", "message": "…", "hint": "…"}}` with exit code 1, or 2 for bad usage.

Branch on `error.code` and follow `error.hint`, which names the next step. Many results also carry a `next` field saying what to do next. Progress lines go to stderr. `nimbus docs` and `--help` always print text.

## Quickstart
```sh
nimbus login                   # returns a URL for the user to approve; see "Logging in"
nimbus deploy --work           # run in the app's directory; ask the user --work or --personal first
nimbus share lena@acme.com     # work projects: give a coworker access
```

## Logging in
Logging in is the one step that needs the user.
1. Run `nimbus login`. It returns `"status": "pending"` with a `verification_url` and a `user_code`.
2. Show the user the URL and the code, and ask them to approve it in their browser. If `browser_opened` is true, a browser window may already be open on their machine. The code expires after about 10 minutes.
3. Once the user says they've approved it, run `nimbus login` again. It returns `"status": "logged_in"` with their email and org. `"personal": true` means they signed in with a personal Google account, which hosts personal projects; a work account hosts work projects.

If step 3 still returns `pending`, the user hasn't approved yet. Ask them instead of retrying in a loop. To check the login at any time, run `nimbus whoami`. If the user gives you a `NIMBUS_TOKEN`, set it in the environment and skip logging in.

## Deploying
Run `nimbus deploy` in the app's directory, or `nimbus deploy <dir>`.
- **Personal or work:** until `nimbus.yaml` says, deploy fails with `ask_user`. **Ask the user whether it's a personal or a work project, and don't pick for them.** A personal project is public: anyone with the URL can open it. A work project is private: only the people it's shared with, behind their company's Google sign-in. Then run `nimbus deploy --personal` or `nimbus deploy --work`. The answer is saved as `account` in `nimbus.yaml`, so later deploys don't ask. If it doesn't fit the login, deploy fails with `wrong_account`: personal projects need a personal Google account, such as Gmail, and work projects a work account from a company on Nimbus.
- **First deploy:** it creates `nimbus.yaml` and names the app after the directory. Commit that file. For a different name, pass `--app <name>` on the first deploy.
- **What's uploaded:** the directory, minus `.gitignore` matches, `.git`, `node_modules`, `.venv`, `__pycache__`, and **every `.env*` file**. Any `.env` files it left out are listed in `env_files_skipped`.
- **Build:** the app needs a `Dockerfile` at its root. Without one, the deploy fails with `build_failed`.
- **Waiting:** the command waits until the new version is live. That usually takes 1–3 minutes and is capped at 10, so **give the command a timeout of at least 10 minutes**. If your tool can't wait that long, run `nimbus deploy --no-wait`, then run `nimbus status` until `state` is `live` or `failed`.
- **No changes:** running it again on the same source returns `"unchanged": true` and builds nothing.
- **Failures:** `error.log_tail` has the end of the log. Fix the problem and deploy again. `nimbus logs --build` shows the full build log.

After the first deploy of a work project, only the user who deployed it can open it. A personal project is public from the start.

## Sharing
- **Work projects only:** personal projects are always public, so sharing by email or making one private fails with `personal_app`. Give the user the URL to send instead.
- **Granting and revoking:** `nimbus share <email>...` gives people access, and `nimbus unshare <email>...` takes it away. Both return the full list in `shared_with`.
- **Who:** only addresses in the user's own org work.
- **Public:** `nimbus share --public` lets anyone on the internet open the app without signing in, and `nimbus unshare --public` puts it back behind sign-in. Only make an app public when the user asks for it. A public app gets no `X-Viewer-Email`.
- **No notifications:** Nimbus doesn't notify anyone. Give the user the URL to send.
- **Not in the manifest:** sharing isn't part of `nimbus.yaml`, and redeploying never changes who has access.

## Writing an app that runs on Nimbus
1. **Listen on `0.0.0.0:$PORT`.** Read the port from the `PORT` environment variable. If the app has to use a fixed port, set `port:` in `nimbus.yaml` to the same number.
2. **Don't build a login.** Nimbus signs the viewer in before any request reaches the app, and puts the viewer's email in the `X-Viewer-Email` request header. Trust that header: only Nimbus can reach the app. If the header is missing, the app is public (every personal project is) or running locally: treat the viewer as anonymous, or fall back to a placeholder.
3. **React and other single-page apps need a small server.** Browser code can't read request headers. Serve the built frontend from a server that also exposes `GET /api/me`, which returns the `X-Viewer-Email` header. The same server owns the data.
4. **Keep state in `$NIMBUS_DATA_DIR`.** On Nimbus it's `/data`, a directory on a local disk, so SQLite (including WAL mode) works well there. It survives restarts, idle stops, and redeploys. Locally, default the directory to `./data`.
5. **Only `$NIMBUS_DATA_DIR` and `/tmp` are writable.** The app runs as an unprivileged user (uid 10001) with a read-only filesystem, so writing anywhere else fails. `HOME` is `/tmp` unless the image sets it. `/tmp` is in memory and cleared on every restart. The app gets 512 MB of memory.
6. **Nimbus only checks that the app accepts connections on `$PORT`.** It never sends HTTP requests of its own, so every request the app sees comes from a viewer, who has signed in unless the app is public.
7. **Don't put secrets in the app.** That means no API keys in code, and `.env` files aren't uploaded anyway.
8. **No outbound internet.** The build can download packages, but the running app can't call outside APIs or databases. Requests made by the viewer's browser, such as CDNs and fonts, aren't affected.
9. **One instance, which sleeps when idle.** After 15 minutes without requests or open WebSockets the app stops: it gets SIGTERM, then 10 seconds to exit. The next request starts it again, which takes a second or two.
10. **Expired sign-ins reach `fetch` as a 401.** When a viewer's sign-in has expired, page loads are sent to sign in again, but `fetch` calls get `401` with `{"error":"not_signed_in"}`. Reload the page when you see it.
11. **WebSockets work.** Open them from the app's own pages, to the app's own URL: `new WebSocket(location.origin.replace("http", "ws") + "/ws")`. The handshake carries `X-Viewer-Email` like any other request. On a private app, sockets from other sites are refused; on a public app, any site can open one. With gunicorn, each open socket holds a thread, so set `--threads` to at least the number of sockets you expect.
12. **Reconnect when a socket closes.** Redeploys and restarts close sockets, and so does losing access to the app. Retry after a short delay, then refetch anything the socket was keeping up to date. The browser doesn't say why a socket was refused, so if retries keep failing, `fetch` a path in the app: a 401 means the sign-in expired, so reload the page.
13. **Other requests time out after 5 minutes.**
14. **Log to stdout.** `nimbus logs` shows only what the app prints to stdout and stderr. Log one line per request (method, path, status, and the `X-Viewer-Email` viewer) and every error. Leave out query strings and request bodies.

Two complete examples, both with SQLite, are in the CLI's repo at https://github.com/wceriale/nimbus-cli/tree/main/examples: `react-tasks` (a task board: React with a small Node server) and `python-dashboard` (a stats dashboard: Flask and gunicorn). Copy one as a starting point.

A minimal Flask app with SQLite:
```python
import os, sqlite3
from flask import Flask, request

DATA_DIR = os.environ.get("NIMBUS_DATA_DIR", "./data")
os.makedirs(DATA_DIR, exist_ok=True)
db = sqlite3.connect(os.path.join(DATA_DIR, "app.db"), check_same_thread=False)
db.execute("CREATE TABLE IF NOT EXISTS visits (email TEXT, at TEXT DEFAULT CURRENT_TIMESTAMP)")

app = Flask(__name__)

@app.get("/")
def home():
    viewer = request.headers.get("X-Viewer-Email", "local-dev@example.com")
    with db:
        db.execute("INSERT INTO visits (email) VALUES (?)", (viewer,))
    count = db.execute("SELECT COUNT(*) FROM visits WHERE email = ?", (viewer,)).fetchone()[0]
    return f"Hello, {viewer}. Visits: {count}"

if __name__ == "__main__":
    app.run(host="0.0.0.0", port=int(os.environ.get("PORT", "8080")))
```

Saved as `app.py`, it needs this `Dockerfile` next to it:
```dockerfile
FROM python:3.13-slim
WORKDIR /app
RUN pip install --no-cache-dir flask gunicorn
COPY . .
CMD exec gunicorn --bind 0.0.0.0:${PORT:-8080} --threads 8 app:app
```

## nimbus.yaml
```yaml
name: renewal-desk      # required: 3–40 lowercase letters, digits, hyphens; becomes the URL
port: 8080              # optional: the app receives it as $PORT
account: work           # personal or work: the user's answer, written by deploy --personal/--work
```
The file has no other fields. A `share:` field is an error, because sharing is a command.

## Errors
| `error.code` | What to do |
|---|---|
| `not_logged_in` | Run `nimbus login` and show the user the URL. |
| `login_expired`, `login_denied` | Run `nimbus login` again. If it was denied, ask the user what happened. |
| `org_not_enabled` | Tell the user Nimbus is invite-only for now. Their org has to be onboarded. |
| `ask_user` | Ask the user what the message asks (personal or work project), then run the command the hint names with their answer. Don't choose for them. |
| `wrong_account` | The project doesn't fit the login. Follow the hint: `nimbus logout`, then `nimbus login` with the other kind of Google account, or ask the user. |
| `account_conflict` | The flag disagrees with `account` in `nimbus.yaml`. Drop the flag, or change `account` if the user says so. |
| `personal_app` | Personal projects are always public. Give the user the URL to send instead of sharing. |
| `app_limit` | A personal account has its maximum number of apps. Tell the user. |
| `api_url_not_set`, `invalid_api_url` | Set `NIMBUS_API_URL` to the URL the user gives you. |
| `cli_outdated` | Re-run the install one-liner, then retry. |
| `invalid_manifest`, `invalid_name` | Fix the field the message names. |
| `name_taken` | Someone else in the org owns that name. Change `name` in `nimbus.yaml`. |
| `upload_too_large` | Add the paths in `details.largest` to `.gitignore`, if the app doesn't need them at runtime. |
| `build_failed` | Read `log_tail`, fix the build (add a `Dockerfile` if there's none), and deploy again. |
| `start_failed` | Make the app listen on `0.0.0.0:$PORT`, then deploy again. |
| `deploy_timeout` | The deploy is still running. Run `nimbus status`. |
| `app_not_found` | There's no such app, or it isn't the user's. Check `nimbus list`. |
| `app_not_specified` | Pass `--app <name>`, or run from the app's directory. |
| `invalid_email`, `share_outside_org` | Use full addresses in the user's org (listed in `details.emails`). |
| `network_error`, `unexpected_response`, `rate_limited` | Wait a moment and retry. If it keeps happening, tell the user. |
| `usage` | The command line was wrong. Run `nimbus <command> --help`. |

## Limits
- **Upload:** 100 MB compressed, after the skips listed under Deploying.
- **Deploy wait:** 10 minutes.
- **Requests:** time out after 5 minutes.
- **WebSockets:** at most 1,000 open per app. They have no time limit, but close when access ends or the app restarts.
- **Runtime:** one instance per app, no outbound internet, and a stop after 15 idle minutes.
- **Sharing:** inside the org only, for work projects. Groups aren't supported yet.
- **Personal accounts:** at most 10 apps.
