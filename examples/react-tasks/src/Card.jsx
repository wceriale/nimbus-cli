import { useEffect, useRef, useState } from "react";
import Avatar, { shortName } from "./Avatar.jsx";

export default function Card({ card, me, columns, dragging, dropMark, actions }) {
  const [editing, setEditing] = useState(false);
  const classes = ["card", dragging && "is-dragging", dropMark && `drop-${dropMark}`].filter(Boolean);

  return (
    <article
      className={classes.join(" ")}
      data-card-id={card.id}
      draggable={!editing}
      inert={card.pending} // not saved yet, so it has no real id to edit, move, or delete
      onDragStart={(e) => {
        e.dataTransfer.effectAllowed = "move";
        e.dataTransfer.setData("text/plain", card.title); // Firefox won't start a drag without data
        // Wait a frame so the browser's drag image is taken before the card dims.
        requestAnimationFrame(() => actions.startDrag(card.id));
      }}
      onDragEnd={actions.endDrag}
    >
      {editing ? (
        <TitleEditor
          title={card.title}
          onSave={(title) => actions.rename(card.id, title)}
          onDone={() => setEditing(false)}
        />
      ) : (
        <p
          className="card-title"
          role="button"
          tabIndex={0}
          title="Click to edit"
          onClick={() => setEditing(true)}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              setEditing(true);
            }
          }}
        >
          {card.title}
        </p>
      )}

      <footer className="card-meta">
        <Avatar email={card.createdBy} />
        <span className="card-author" title={card.createdBy}>
          {card.createdBy === me ? "You" : shortName(card.createdBy)}
        </span>
        <time dateTime={card.createdAt} title={new Date(card.createdAt).toLocaleString()}>
          {timeAgo(card.createdAt)}
        </time>
        <span className="card-actions">
          {/* A native <select> under an icon: moving cards without drag and drop (phones, keyboards). */}
          <label className="icon-button" title="Move to another column">
            <MoveIcon />
            <select
              aria-label="Move card to column"
              value={card.column}
              onChange={(e) => actions.move(card.id, e.target.value)}
            >
              {columns.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.title}
                </option>
              ))}
            </select>
          </label>
          <button className="icon-button" title="Delete card" aria-label="Delete card" onClick={() => actions.remove(card.id)}>
            <DeleteIcon />
          </button>
        </span>
      </footer>
    </article>
  );
}

// Enter or clicking away saves, Escape cancels, Shift+Enter adds a line break.
function TitleEditor({ title, onSave, onDone }) {
  const [text, setText] = useState(title);
  const ref = useRef(null);
  const finished = useRef(false); // the editor can blur as it unmounts; don't save twice

  useEffect(() => ref.current.select(), []);

  function finish(save) {
    if (finished.current) return;
    finished.current = true;
    const trimmed = text.trim();
    if (save && trimmed && trimmed !== title) onSave(trimmed);
    onDone();
  }

  return (
    <textarea
      ref={ref}
      className="card-editor"
      aria-label="Card title"
      autoFocus
      value={text}
      maxLength={200}
      rows={3}
      onChange={(e) => setText(e.target.value)}
      onBlur={() => finish(true)}
      onKeyDown={(e) => {
        if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
          e.preventDefault();
          finish(true);
        } else if (e.key === "Escape") {
          finish(false);
        }
      }}
    />
  );
}

const units = [["year", 31_536_000], ["month", 2_592_000], ["week", 604_800], ["day", 86_400], ["hour", 3_600], ["minute", 60]];
const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

// "3 hours ago", "yesterday", "just now"
function timeAgo(iso) {
  const seconds = (new Date(iso) - Date.now()) / 1000;
  const [unit, size] = units.find(([, size]) => Math.abs(seconds) >= size) ?? [];
  return unit ? relative.format(Math.trunc(seconds / size), unit) : "just now";
}

const MoveIcon = () => (
  <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
    <path d="M2 5h10m-3-3 3 3-3 3M14 11H4m3-3-3 3 3 3" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

const DeleteIcon = () => (
  <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
    <path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
