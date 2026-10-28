"""Sales dashboard: an example Nimbus app (Flask + SQLite, fake data).

What makes it a Nimbus app, and where to look:
  * Who's viewing comes from the X-Viewer-Email header ........... viewer()
  * All state lives under $NIMBUS_DATA_DIR (/data on Nimbus) ..... DATA_DIR
  * It listens on 0.0.0.0:$PORT ................ Dockerfile, and the bottom of this file
  * No secrets and no outbound calls: the data comes from seed.py.
"""
import fcntl
import math
import os
import sqlite3
from datetime import date, datetime, timezone

from flask import Flask, g, render_template, request

from seed import OPEN_STAGES, seed_if_empty

DATA_DIR = os.environ.get("NIMBUS_DATA_DIR", "./data")
DB_PATH = os.path.join(DATA_DIR, "dashboard.db")
LOCAL_VIEWER = "local-dev@example.com"  # no header means we're running locally

SCHEMA = """
CREATE TABLE IF NOT EXISTS deals (
    id         INTEGER PRIMARY KEY,
    account    TEXT NOT NULL,
    owner      TEXT NOT NULL,     -- the account executive's email
    region     TEXT NOT NULL,
    stage      TEXT NOT NULL,     -- one of OPEN_STAGES, 'Closed Won' or 'Closed Lost'
    amount     INTEGER NOT NULL,  -- whole dollars
    created_on TEXT NOT NULL,     -- YYYY-MM-DD
    closed_on  TEXT               -- YYYY-MM-DD, NULL while the deal is open
);
CREATE INDEX IF NOT EXISTS deals_owner ON deals (owner);
CREATE TABLE IF NOT EXISTS viewers (
    email     TEXT PRIMARY KEY,
    views     INTEGER NOT NULL,
    last_seen TEXT NOT NULL       -- UTC, ISO 8601
);
"""

app = Flask(__name__)


# --- Database ---------------------------------------------------------------

def connect():
    # isolation_level=None: every statement commits on its own unless we BEGIN.
    # timeout: wait up to 10 s for another thread's write lock instead of failing.
    conn = sqlite3.connect(DB_PATH, timeout=10, isolation_level=None)
    conn.row_factory = sqlite3.Row
    return conn


def init_db():
    os.makedirs(DATA_DIR, exist_ok=True)
    if not os.access(DATA_DIR, os.W_OK):
        # An exception (not sys.exit) makes gunicorn stop instead of restarting the worker forever.
        raise PermissionError(f"{DATA_DIR} isn't writable by uid {os.getuid()}")
    # If two processes start at once, they take turns here. Without the lock, the
    # loser can get "database is locked" from the WAL switch: SQLite fails that
    # immediately instead of waiting out the timeout.
    with open(os.path.join(DATA_DIR, "dashboard.lock"), "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)  # released when the file closes
        conn = connect()
        conn.execute("PRAGMA journal_mode=WAL")  # readers don't block the writer
        conn.executescript(SCHEMA)
        seed_if_empty(conn)
        conn.close()


def get_db():
    if "db" not in g:
        g.db = connect()  # one connection per request, closed below
    return g.db


@app.teardown_appcontext
def close_db(_exc):
    if (db := g.pop("db", None)) is not None:
        db.close()


# --- Formatting (also used as template filters) -----------------------------

@app.template_filter()
def money(v):
    """$950, $8.5K, $412K, $1.2M"""
    for size, suffix in ((1e6, "M"), (1e3, "K")):
        if abs(v) >= size:
            n = v / size
            return f"${n:.0f}{suffix}" if n >= 100 else f"${n:.1f}".rstrip("0").rstrip(".") + suffix
    return f"${v:,.0f}"


@app.template_filter()
def person(email):
    return email.split("@")[0].replace(".", " ").title()  # maya.chen@acme.com -> Maya Chen


@app.template_filter()
def day(iso):
    d = date.fromisoformat(iso)
    return f"{d:%b} {d.day}, {d.year}"


@app.template_filter()
def ago(iso):
    s = (datetime.now(timezone.utc) - datetime.fromisoformat(iso)).total_seconds()
    for unit, size in (("d", 86400), ("h", 3600), ("min", 60)):
        if s >= size:
            return f"{int(s // size)} {unit} ago"
    return "just now"


def plural(n, word):
    return f"{n} {word}" if n == 1 else f"{n} {word}s"


# --- Charts: geometry here, SVG markup in templates/charts.html --------------

PLOT_H, PAD_TOP, X_BAND, COL_W, RADIUS = 150, 20, 24, 14, 4


def nice_step(raw):
    """Round raw up to 1, 2, 2.5 or 5 times a power of ten, for tidy axis ticks."""
    mag = 10 ** math.floor(math.log10(raw))
    return next(m * mag for m in (1, 2, 2.5, 5, 10) if raw <= m * mag)


def column_path(h, base):
    """A column h px tall standing on y=base, centered on x=0: square at the
    baseline, rounded at the data end."""
    x, r, top = COL_W / 2, min(RADIUS, h), base - h
    return f"M{-x},{base}V{top + r}A{r},{r} 0 0 1 {-x + r},{top}H{x - r}A{r},{r} 0 0 1 {x},{top + r}V{base}Z"


def column_chart(months):
    """months: [(first day of month, amount, deals)] -> geometry for charts.columns()."""
    peak = max(amount for _, amount, _ in months)
    step = nice_step(max(peak, 1) / 5)  # at most 5 gridline intervals
    top = step * max(1, math.ceil(peak / step))
    base, n = PAD_TOP + PLOT_H, len(months)
    bars = []
    for i, (month, amount, deals) in enumerate(months):
        h = amount / top * PLOT_H
        edge = "start" if i == 0 else "end" if i == n - 1 else "middle"
        bars.append({
            "slot_x": i * 100 / n,           # x positions are % of the plot's width
            "cx": (i + 0.5) * 100 / n,
            "path": column_path(h, base) if h else "",
            "top": base - h,
            "label": f"{month:%b}",
            "title": f"{month:%B %Y}: {money(amount)} closed won, {plural(deals, 'deal')}",
            "value": money(amount),
            "peak": peak > 0 and amount == peak,  # the one column that gets a value label
            "anchor": edge,
            "dx": {"start": -COL_W / 2, "middle": 0, "end": COL_W / 2}[edge],
            "partial": i == n - 1,               # this month isn't over yet
            "alt": (n - 1 - i) % 2 == 1,         # every other month label hides on phones
        })
    ticks = [{"y": round(base - v / top * PLOT_H) + 0.5, "label": money(v)}
             for v in [step * k for k in range(round(top / step) + 1)]]
    return {"bars": bars, "ticks": ticks, "base": base, "height": base + X_BAND, "slot_w": 100 / n}


def bar_rows(rows):
    """rows: [(label, amount, deals)] -> each bar's length as a % of the longest."""
    longest = max((amount for _, amount, _ in rows), default=0) or 1
    return [{"label": label, "amount": amount, "deals": plural(deals, "deal"),
             "pct": round(amount / longest * 100, 2)} for label, amount, deals in rows]


# --- The page ------------------------------------------------------------------

def viewer():
    """The signed-in viewer. Nimbus authenticates every request and sets this
    header, and only Nimbus can reach the app, so the app can trust it."""
    return (request.headers.get("X-Viewer-Email") or LOCAL_VIEWER).strip().lower()


def add_months(d, n):
    """First day of the month n months after d's month (n can be negative)."""
    y, m = divmod(d.year * 12 + d.month - 1 + n, 12)
    return date(y, m + 1, 1)


@app.get("/")
def dashboard():
    me, db = viewer(), get_db()

    # Record this view. It's a write that persists in $NIMBUS_DATA_DIR.
    db.execute(
        "INSERT INTO viewers (email, views, last_seen) VALUES (?, 1, ?)"
        " ON CONFLICT (email) DO UPDATE SET views = views + 1, last_seen = excluded.last_seen",
        (me, datetime.now(timezone.utc).isoformat(timespec="seconds")),
    )

    # ?owner=<email> narrows every number on the page to one AE.
    owners = [r[0] for r in db.execute("SELECT DISTINCT owner FROM deals ORDER BY owner")]
    owner = request.args.get("owner")
    owner = owner if owner in owners else None
    scope, scope_args = ("owner = ?", [owner]) if owner else ("1 = 1", [])

    def q(sql, *args):
        """Run sql with {scope} filled in. Values are always bound, never pasted."""
        return db.execute(sql.replace("{scope}", scope), [*scope_args, *args]).fetchall()

    today = date.today()
    months = [add_months(today, -i) for i in range(11, -1, -1)]  # last 12 calendar months
    since = months[0].isoformat()
    quarter = date(today.year, (today.month - 1) // 3 * 3 + 1, 1)

    open_amt, open_n = q("SELECT TOTAL(amount), COUNT(*) FROM deals WHERE {scope} AND closed_on IS NULL")[0]
    qtr_amt, qtr_n = q("SELECT TOTAL(amount), COUNT(*) FROM deals WHERE {scope}"
                       " AND stage = 'Closed Won' AND closed_on >= ?", quarter.isoformat())[0]
    won_n, closed_n, won_amt = q(
        "SELECT COUNT(*) FILTER (WHERE stage = 'Closed Won'), COUNT(*),"
        " TOTAL(amount) FILTER (WHERE stage = 'Closed Won')"
        " FROM deals WHERE {scope} AND closed_on >= ?", since)[0]
    kpis = [
        ("Open pipeline", money(open_amt), plural(open_n, "open deal")),
        ("Closed won this quarter", money(qtr_amt),
         f"Q{(today.month - 1) // 3 + 1} {today.year} to date, {plural(qtr_n, 'deal')}"),
        ("Win rate", f"{won_n / closed_n:.0%}" if closed_n else "n/a",
         f"{won_n} won of {closed_n} closed, last 12 months"),
        ("Average deal size", money(won_amt / won_n) if won_n else "n/a", "Closed won, last 12 months"),
    ]

    won_by_month = {r[0]: (r[1], r[2]) for r in q(
        "SELECT substr(closed_on, 1, 7), TOTAL(amount), COUNT(*) FROM deals"
        " WHERE {scope} AND stage = 'Closed Won' AND closed_on >= ? GROUP BY 1", since)}
    monthly = [(m, *won_by_month.get(f"{m:%Y-%m}", (0, 0))) for m in months]

    by_stage = {r[0]: (r[1], r[2]) for r in q(
        "SELECT stage, TOTAL(amount), COUNT(*) FROM deals WHERE {scope} AND closed_on IS NULL GROUP BY stage")}

    return render_template(
        "index.html",
        me=me,
        is_local=me == LOCAL_VIEWER,
        owners=owners,
        owner=owner,
        kpis=kpis,
        monthly=monthly,
        chart=column_chart(monthly),
        pipeline=bar_rows([(s, *by_stage.get(s, (0, 0))) for s in OPEN_STAGES]),
        top_accounts=bar_rows(q(
            "SELECT account, TOTAL(amount), COUNT(*) FROM deals WHERE {scope}"
            " AND stage = 'Closed Won' AND closed_on >= ? GROUP BY account ORDER BY 2 DESC LIMIT 6", since)),
        deal_count=q("SELECT COUNT(*) FROM deals WHERE {scope}")[0][0],
        deals=q("SELECT * FROM deals WHERE {scope}"  # open deals first, then the latest closes
                " ORDER BY closed_on IS NOT NULL, COALESCE(closed_on, created_on) DESC, id DESC LIMIT 50"),
        viewers=db.execute("SELECT * FROM viewers ORDER BY last_seen DESC LIMIT 8").fetchall(),
    )


init_db()  # at import, so gunicorn and `python app.py` both start with a ready database

if __name__ == "__main__":
    app.run(host="0.0.0.0", port=int(os.environ.get("PORT", "8080")))
