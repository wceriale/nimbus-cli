# sales-dashboard

A sales stats dashboard on fake data, written as a [Nimbus](../../README.md) example: Flask, SQLite, and server-rendered SVG charts, with no JavaScript.

It shows the two things every Nimbus app does:
- **It knows who's viewing.** Nimbus signs the viewer in and sends their email in the `X-Viewer-Email` header. The page greets them, offers "My deals" if they're one of the fake account executives, and records them under "Recent viewers".
- **It keeps state in `$NIMBUS_DATA_DIR`.** The SQLite database is `$NIMBUS_DATA_DIR/dashboard.db`. The first start seeds 300 fake deals into it (always the same deals, dated relative to that day), and every page view is written to it.

| File | What's in it |
|---|---|
| `app.py` | Database setup, queries, chart geometry, and the one route |
| `seed.py` | The fake data, and seeding that's safe if two processes start at once |
| `templates/` | The page, and the SVG chart macros |
| `Dockerfile` | gunicorn on `0.0.0.0:$PORT`, one worker with threads, and an access log line per request with the viewer (what `nimbus logs` shows) |

## Run it locally
```sh
python3.13 -m venv .venv && . .venv/bin/activate
pip install -r requirements.txt
python app.py                 # http://localhost:8080, data in ./data
```
Without the header, you're `local-dev@example.com`. To see the page as one of the AEs:
```sh
curl -H 'X-Viewer-Email: maya.chen@acme.com' 'http://localhost:8080/?owner=maya.chen@acme.com'
```
The AEs are maya.chen, jordan.reyes, aisha.patel, sam.okafor, hannah.weber, and kenji.sato, all `@acme.com`. To start over with fresh data, delete `data/`.

### With Docker, under Nimbus's restrictions
```sh
docker build -t sales-dashboard .
mkdir -p data && chmod 777 data   # the container runs as uid 10001
docker run --rm -p 8080:8080 --read-only --tmpfs /tmp --user 10001:10001 --cap-drop ALL \
  -e PORT=8080 -e NIMBUS_DATA_DIR=/data -e HOME=/tmp -v "$PWD/data:/data" sales-dashboard
```

## Deploy it
```sh
nimbus deploy                     # builds the Dockerfile, prints the URL
nimbus share lena@yourcompany.com
```
`data/` is in `.gitignore`, so a local database is never uploaded. On Nimbus, the app gets its own `/data`.
