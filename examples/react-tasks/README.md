# Team tasks: a React + SQLite app for Nimbus

A small Trello-style board with three columns. You can drag cards between columns, rename them inline, and see who added each one. It's the reference React app for Nimbus, so copy it when a tool needs a frontend and saved data.

## How it uses Nimbus
- **Who's viewing:** Nimbus signs people in and passes their email in the `X-Viewer-Email` header. The server stamps new cards with it and returns it from `GET /api/me`, because browser code can't read request headers. There's no login code. Locally the header is missing, so the server uses `local-dev@example.com`.
- **Data:** SQLite at `$NIMBUS_DATA_DIR/tasks.db`. That's `/data` on Nimbus and `./data` locally. It's the only directory that survives restarts and redeploys.
- **Runtime:** the server listens on `0.0.0.0:$PORT`, holds no secrets, and makes no outbound calls. Nimbus runs it as a non-root user on a read-only filesystem, so it writes only to the data directory.
- **Logs:** the server prints one line per request to stdout, e.g. `POST /api/cards 201 4ms lena@acme.com`. That's what `nimbus logs` shows.

## Layout
```
server/server.js   Express: the JSON API under /api, SQLite, and serving dist/
src/App.jsx        board state, optimistic updates, the header
src/Column.jsx     a column: drop target and "Add a card"
src/Card.jsx       a card: inline rename, move, delete
src/Avatar.jsx     initials and a color from an email
src/api.js         fetch helpers for /api
src/styles.css     light and dark themes
Dockerfile         builds the frontend, then runs node server/server.js
nimbus.yaml        the app's name on Nimbus
```

| API | Body | Returns |
|---|---|---|
| `GET /api/me` | | `{ email }` |
| `GET /api/board` | | `{ columns: [{ id, title, cards: [{ id, title, column, createdBy, createdAt }] }] }` |
| `POST /api/cards` | `{ title, column }` | 201 and the card |
| `PATCH /api/cards/:id` | `{ title?, column?, index? }` | the card. `index` is its new place in the column, from 0 |
| `DELETE /api/cards/:id` | | 204 |

Errors are `{ "error": "..." }` with a 4xx status. Titles are 1–200 characters, and columns are `todo`, `doing`, and `done`.

## Run it locally
```sh
npm install
npm run server    # the API on :8080, with data in ./data
npm run dev       # in a second terminal: the frontend with hot reload, on http://localhost:5173
```
If port 8080 is taken, set the same `PORT` for both commands. To run the production build instead, use `npm run build && npm start`, then open http://localhost:8080.

To run it the way Nimbus does, use Docker:
```sh
docker build -t team-tasks .
mkdir -p data && chmod 777 data
docker run --rm --read-only --tmpfs /tmp --user 10001:10001 --cap-drop ALL \
  -e PORT=8080 -e NIMBUS_DATA_DIR=/data -v "$PWD/data:/data" -p 8080:8080 team-tasks
curl -H 'X-Viewer-Email: lena@acme.com' localhost:8080/api/me   # act as a viewer
```

## Deploy
```sh
nimbus deploy                   # builds the Dockerfile and prints the app's URL
nimbus share lena@yourco.com    # gives a coworker access
```
For your own copy, change `name` in `nimbus.yaml` first.

## Notes
- The Dockerfile runs `npm ci --ignore-scripts`. better-sqlite3 ships prebuilt binaries, but without that flag npm runs node-gyp anyway, which needs Python that `node:24-slim` doesn't have.
- Nimbus has no websockets, so the board doesn't update live. It reloads when the tab comes back into view.
