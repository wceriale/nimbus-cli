import { useCallback, useEffect, useState } from "react";
import { api } from "./api.js";
import Avatar from "./Avatar.jsx";
import Column from "./Column.jsx";

let tempIds = 0; // ids for cards that haven't been saved yet

export default function App() {
  const [me, setMe] = useState(null); // the viewer's email, from GET /api/me
  const [columns, setColumns] = useState(null); // [{ id, title, cards: [...] }], as the server sends it
  const [loadError, setLoadError] = useState(null);
  const [notice, setNotice] = useState(null); // why the last change didn't save
  const [drag, setDrag] = useState(null); // { id, column, index } while a card is being dragged

  const load = useCallback(async () => {
    try {
      setColumns((await api.board()).columns);
      setLoadError(null);
    } catch (err) {
      setLoadError(err.message);
    }
  }, []);

  useEffect(() => {
    api.me().then(({ email }) => setMe(email), () => {});
    load();
    // Nimbus has no websockets, so pick up teammates' changes whenever the tab comes back into view.
    const onVisible = () => document.visibilityState === "visible" && load();
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, [load]);

  useEffect(() => {
    if (!notice) return;
    const timer = setTimeout(() => setNotice(null), 6000);
    return () => clearTimeout(timer);
  }, [notice]);

  // Optimistic update: change the board right away, then tell the server.
  // If the server says no, show why and reload the board so it matches what's saved.
  function save(change, request) {
    setColumns(change);
    request().catch((err) => {
      setNotice(`Couldn't save your change: ${err.message}. The board has been reloaded.`);
      load();
    });
  }

  const actions = {
    add(column, title) {
      const temp = { id: `new-${++tempIds}`, title, column, createdBy: me ?? "", createdAt: new Date().toISOString(), pending: true };
      save(
        (cols) => placeCard(cols, temp, column),
        async () => {
          const card = await api.createCard(title, column);
          setColumns((cols) => mapCard(cols, temp.id, () => card));
        },
      );
    },
    rename(id, title) {
      save((cols) => mapCard(cols, id, (card) => ({ ...card, title })), () => api.updateCard(id, { title }));
    },
    // index is the card's new place in the column, counted without the card. No index means last.
    move(id, column, index) {
      const from = columns.find((c) => c.cards.some((card) => card.id === id));
      const at = from?.cards.findIndex((card) => card.id === id);
      if (!from || (from.id === column && at === index)) return;
      save((cols) => placeCard(cols, from.cards[at], column, index), () => api.updateCard(id, { column, index }));
    },
    remove(id) {
      save((cols) => mapCard(cols, id, () => null), () => api.deleteCard(id));
    },
    // Drag and drop: Card starts and ends the drag, Column reports where it would land.
    startDrag: (id) => setDrag({ id, column: null, index: null }),
    hover: (column, index) =>
      setDrag((d) => (!d || (d.column === column && d.index === index) ? d : { ...d, column, index })),
    drop() {
      if (drag?.column) actions.move(drag.id, drag.column, drag.index);
      setDrag(null);
    },
    endDrag: () => setDrag(null),
  };

  return (
    <>
      <header className="topbar">
        <h1 className="brand">
          <BoardIcon />
          Team tasks
        </h1>
        {me && (
          <p className="me">
            <Avatar email={me} />
            <span className="me-text">
              <span className="me-label">Signed in as </span>
              <strong>{me}</strong>
            </span>
          </p>
        )}
      </header>

      <main className="board">
        {columns ? (
          columns.map((column) => (
            <Column key={column.id} column={column} columns={columns} me={me} drag={drag} actions={actions} />
          ))
        ) : loadError ? (
          <div className="board-status" role="alert">
            <p>Couldn't load the board: {loadError}</p>
            <button onClick={load}>Try again</button>
          </div>
        ) : (
          <p className="board-status">Loading the board…</p>
        )}
      </main>

      {notice && (
        <div className="toast" role="alert">
          <span>{notice}</span>
          <button onClick={() => setNotice(null)}>Dismiss</button>
        </div>
      )}
    </>
  );
}

// Board helpers. Each returns new arrays instead of changing the old ones, so React re-renders.

// Replace one card with fn(card), or remove it if fn returns null.
const mapCard = (columns, id, fn) =>
  columns.map((c) => ({ ...c, cards: c.cards.map((card) => (card.id === id ? fn(card) : card)).filter(Boolean) }));

// Take the card out of wherever it is and insert it into column `to` at `index` (default: last).
function placeCard(columns, card, to, index) {
  return columns.map((c) => {
    const cards = c.cards.filter((x) => x.id !== card.id);
    return c.id === to ? { ...c, cards: cards.toSpliced(index ?? cards.length, 0, { ...card, column: to }) } : { ...c, cards };
  });
}

const BoardIcon = () => (
  <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true" fill="currentColor">
    <rect x="2" y="3" width="6" height="18" rx="2" />
    <rect x="9" y="3" width="6" height="12" rx="2" />
    <rect x="16" y="3" width="6" height="7" rx="2" />
  </svg>
);
