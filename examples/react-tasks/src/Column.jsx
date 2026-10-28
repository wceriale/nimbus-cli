import { useState } from "react";
import Card from "./Card.jsx";

export default function Column({ column, columns, me, drag, actions }) {
  const isTarget = drag?.column === column.id;
  // Cards other than the dragged one; drag.index counts positions in this list.
  const others = column.cards.filter((c) => c.id !== drag?.id);

  function dropMark(card) {
    if (!isTarget || card.id === drag.id) return undefined;
    const i = others.indexOf(card);
    if (i === drag.index) return "before";
    if (i === others.length - 1 && drag.index === others.length) return "after";
  }

  return (
    <section
      className={`column${isTarget ? " is-target" : ""}`}
      data-column={column.id}
      aria-label={column.title}
      onDragOver={(e) => {
        if (!drag) return; // something other than a card, like a file or text from another app
        e.preventDefault(); // allows the drop
        actions.hover(column.id, dropIndex(e.currentTarget, e.clientY, drag.id));
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget)) actions.hover(null, null);
      }}
      onDrop={(e) => {
        e.preventDefault();
        actions.drop();
      }}
    >
      <header className="column-head">
        <span className="marker" aria-hidden="true" />
        <h2>{column.title}</h2>
        <span className="count" aria-label={`${column.cards.length} cards`}>
          {column.cards.length}
        </span>
      </header>

      <div className="column-cards">
        {column.cards.map((card) => (
          <Card
            key={card.id}
            card={card}
            me={me}
            columns={columns}
            dragging={card.id === drag?.id}
            dropMark={dropMark(card)}
            actions={actions}
          />
        ))}
        {column.cards.length === 0 && <p className="column-empty">{isTarget ? "Drop here" : "No cards yet"}</p>}
      </div>

      <AddCard columnTitle={column.title} onAdd={(title) => actions.add(column.id, title)} />
    </section>
  );
}

// Where a card dropped at height y would land: the first card whose middle is below y.
function dropIndex(columnEl, y, dragId) {
  const cards = [...columnEl.querySelectorAll("[data-card-id]")].filter((el) => el.dataset.cardId !== String(dragId));
  const i = cards.findIndex((el) => {
    const box = el.getBoundingClientRect();
    return y < box.top + box.height / 2;
  });
  return i === -1 ? cards.length : i;
}

function AddCard({ columnTitle, onAdd }) {
  const [title, setTitle] = useState("");
  return (
    <form
      className="add-card"
      onSubmit={(e) => {
        e.preventDefault();
        if (!title.trim()) return;
        onAdd(title.trim());
        setTitle("");
      }}
    >
      <input
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        placeholder="Add a card"
        aria-label={`Add a card to ${columnTitle}`}
        maxLength={200}
      />
      <button type="submit" disabled={!title.trim()}>
        Add
      </button>
    </form>
  );
}
