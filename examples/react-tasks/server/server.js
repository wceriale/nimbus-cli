// Team tasks server: serves the built React app (dist/) and a small JSON API backed by SQLite.
// Nimbus signs viewers in before requests get here and says who they are in X-Viewer-Email.
import fs from "node:fs";
import path from "node:path";
import express from "express";
import Database from "better-sqlite3";

const PORT = Number(process.env.PORT) || 8080;
// Nimbus keeps $NIMBUS_DATA_DIR (/data) across restarts and redeploys. Everything else is wiped.
const DATA_DIR = path.resolve(process.env.NIMBUS_DATA_DIR || "data");
const DIST = path.join(import.meta.dirname, "..", "dist");
const COLUMNS = [
  { id: "todo", title: "To do" },
  { id: "doing", title: "Doing" },
  { id: "done", title: "Done" },
];

// ---- Database ---------------------------------------------------------------

fs.mkdirSync(DATA_DIR, { recursive: true });
const db = new Database(path.join(DATA_DIR, "tasks.db"));
db.pragma("journal_mode = WAL");
db.exec(`
  CREATE TABLE IF NOT EXISTS cards (
    id         INTEGER PRIMARY KEY,
    title      TEXT    NOT NULL,
    column_id  TEXT    NOT NULL,
    position   INTEGER NOT NULL, -- 0, 1, 2, ... within a column, with no gaps
    created_by TEXT    NOT NULL, -- the email of the viewer who added it
    created_at TEXT    NOT NULL  -- ISO 8601, UTC
  )`);

const CARD = `id, title, column_id AS "column", created_by AS createdBy, created_at AS createdAt`;
const allCards = db.prepare(`SELECT ${CARD} FROM cards ORDER BY position, id`);
const getCard = db.prepare(`SELECT ${CARD} FROM cards WHERE id = ?`);
const cardIdsIn = db.prepare("SELECT id FROM cards WHERE column_id = ? ORDER BY position, id").pluck();
const insertCard = db.prepare(`
  INSERT INTO cards (title, column_id, position, created_by, created_at)
  VALUES (@title, @column, (SELECT COALESCE(MAX(position) + 1, 0) FROM cards WHERE column_id = @column),
          @createdBy, @createdAt)`);
const setTitle = db.prepare("UPDATE cards SET title = ? WHERE id = ?");
const setColumn = db.prepare("UPDATE cards SET column_id = ? WHERE id = ?");
const setPosition = db.prepare("UPDATE cards SET position = ? WHERE id = ?");
const deleteCard = db.prepare("DELETE FROM cards WHERE id = ?");

// Rewrite positions as 0, 1, 2, ... in the order given.
const renumber = (ids) => ids.forEach((id, i) => setPosition.run(i, id));

// Put a card in `column` at `index`, counted as if the card weren't there yet. No index means last.
function moveCard(card, column, index) {
  const ids = cardIdsIn.all(column).filter((id) => id !== card.id);
  ids.splice(Math.min(index ?? ids.length, ids.length), 0, card.id);
  setColumn.run(column, card.id);
  renumber(ids);
  if (column !== card.column) renumber(cardIdsIn.all(card.column)); // close the gap it left
}

const updateCard = db.transaction((card, { title, column, index }) => {
  if (title !== undefined) setTitle.run(title, card.id);
  if (index !== undefined || (column !== undefined && column !== card.column)) {
    moveCard(card, column ?? card.column, index);
  }
});

const removeCard = db.transaction((card) => {
  deleteCard.run(card.id);
  renumber(cardIdsIn.all(card.column));
});

// A new database has user_version 0, so the sample cards are added on the first start only.
if (db.pragma("user_version", { simple: true }) === 0) {
  const hoursAgo = (h) => new Date(Date.now() - h * 3_600_000).toISOString();
  db.transaction(() => {
    for (const [column, title, age] of [
      ["todo", "Drag a card to another column to move it", 30],
      ["todo", "Click a card's title to rename it", 30],
      ["todo", "Delete the sample cards when you're done with them", 30],
      ["doing", "Share the board: nimbus share teammate@yourco.com", 2],
      ["done", "Deploy the board to Nimbus", 3],
    ]) {
      insertCard.run({ title, column, createdBy: "sample@example.com", createdAt: hoursAgo(age) });
    }
    db.pragma("user_version = 1");
  })();
}

// ---- Validation -------------------------------------------------------------

// Throw one of these from a route to send a JSON error. The handler at the bottom sends it.
const httpError = (status, message) => Object.assign(new Error(message), { status });

function checkTitle(title) {
  const trimmed = typeof title === "string" ? title.trim() : "";
  if (trimmed.length < 1 || trimmed.length > 200) throw httpError(400, "title must be 1-200 characters");
  return trimmed;
}

function checkColumn(column) {
  if (!COLUMNS.some((c) => c.id === column)) {
    throw httpError(400, `column must be one of: ${COLUMNS.map((c) => c.id).join(", ")}`);
  }
  return column;
}

function checkIndex(index) {
  if (!Number.isInteger(index) || index < 0) throw httpError(400, "index must be a whole number, 0 or more");
  return index;
}

function findCard(id) {
  const card = /^\d+$/.test(id) && getCard.get(Number(id));
  if (!card) throw httpError(404, `card ${id} not found`);
  return card;
}

// ---- HTTP -------------------------------------------------------------------

const app = express();

// Nimbus sets this header on every request. It's only missing when the app runs locally.
const viewer = (req) => req.get("X-Viewer-Email") || "local-dev@example.com";

// One line per request on stdout, which is what `nimbus logs` shows, e.g.
// "POST /api/cards 201 4ms lena@acme.com". Query strings are left out, so nothing in them is logged.
app.use((req, res, next) => {
  const start = performance.now();
  res.on("finish", () => {
    const ms = Math.round(performance.now() - start);
    console.log(`${req.method} ${req.originalUrl.split("?")[0]} ${res.statusCode} ${ms}ms ${viewer(req)}`);
  });
  next();
});

app.use("/api", express.json({ limit: "10kb" }));

// Browser code can't read request headers, so the frontend asks who's signed in.
app.get("/api/me", (req, res) => res.json({ email: viewer(req) }));

app.get("/api/board", (req, res) => {
  const cards = allCards.all();
  res.json({ columns: COLUMNS.map((c) => ({ ...c, cards: cards.filter((card) => card.column === c.id) })) });
});

app.post("/api/cards", (req, res) => {
  const { title, column } = req.body ?? {};
  const card = { title: checkTitle(title), column: checkColumn(column) };
  const { lastInsertRowid } = insertCard.run({ ...card, createdBy: viewer(req), createdAt: new Date().toISOString() });
  res.status(201).json(getCard.get(lastInsertRowid));
});

// Body: { title?, column?, index? }. index is the card's new place in its (new) column, from 0.
app.patch("/api/cards/:id", (req, res) => {
  const card = findCard(req.params.id);
  const { title, column, index } = req.body ?? {};
  if (title === undefined && column === undefined && index === undefined) {
    throw httpError(400, "send at least one of title, column, index");
  }
  updateCard(card, {
    title: title === undefined ? undefined : checkTitle(title),
    column: column === undefined ? undefined : checkColumn(column),
    index: index === undefined ? undefined : checkIndex(index),
  });
  res.json(getCard.get(card.id));
});

app.delete("/api/cards/:id", (req, res) => {
  removeCard(findCard(req.params.id));
  res.status(204).end();
});

app.use("/api", (req, res) => res.status(404).json({ error: `no such endpoint: ${req.method} ${req.originalUrl}` }));

// The built frontend. Vite gives files in dist/assets hashed names, so they can be cached for good.
app.use("/assets", express.static(path.join(DIST, "assets"), { immutable: true, maxAge: "1y" }));
app.use("/assets", (req, res) => res.status(404).end()); // a missing file, not a page of the app
app.use(express.static(DIST));
app.get("/{*path}", (req, res) => res.sendFile(path.join(DIST, "index.html"))); // client-side routes

// Errors thrown above, bad JSON or a too-large body (from express.json), and anything unexpected.
app.use((err, req, res, next) => {
  const status = err.status ?? 500;
  if (status >= 500) console.error(err);
  res.status(status).json({ error: status >= 500 ? "internal server error" : err.message });
});

const server = app.listen(PORT, "0.0.0.0", (err) => {
  if (err) throw err;
  console.log(`team-tasks listening on 0.0.0.0:${PORT}, data in ${DATA_DIR}`);
  if (!fs.existsSync(DIST)) console.log("No dist/ yet: run `npm run build`, or use `npm run dev` for the frontend.");
});

// Nimbus stops idle apps with SIGTERM. Node ignores SIGTERM when it's PID 1 in a container,
// so handle it: finish open requests, close the database, and exit.
for (const signal of ["SIGTERM", "SIGINT"]) {
  process.on(signal, () => {
    server.close(() => {
      db.close();
      process.exit(0);
    });
  });
}
