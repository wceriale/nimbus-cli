"""Fake sales data, seeded into SQLite the first time the app starts.

The random seed is fixed, so every fresh database gets the same deals. Dates are
relative to the day of seeding, so the dashboard always shows the last 12 months.
"""
import random
from datetime import date, timedelta

OPEN_STAGES = ["Prospecting", "Qualified", "Proposal", "Negotiation"]
SEED = 42
DEALS = 300

# (email, region, win rate, deal-size multiplier)
AES = [
    ("maya.chen@acme.com", "NA West", 0.42, 1.3),
    ("jordan.reyes@acme.com", "NA West", 0.34, 1.0),
    ("aisha.patel@acme.com", "NA East", 0.46, 1.1),
    ("sam.okafor@acme.com", "NA East", 0.30, 0.9),
    ("hannah.weber@acme.com", "EMEA", 0.38, 1.2),
    ("kenji.sato@acme.com", "APAC", 0.36, 0.8),
]

PREFIXES = """Northwind Bluefin Cedarline Harborview Juniper Lumen Granite Atlas Copperline
    Redwood Brightwater Ironbark Silverlake Foxglove Meridian Tidewater Kestrel Oakmont
    Pioneer Starling Cobalt Evergreen Larkspur Quarry Driftwood Highland Bramble Sequoia
    Fairhaven Windrose Ember Beacon Halcyon Marigold Riverbend Stonegate Thistle Waypoint
    Alder Basalt Canyon Falcon Glacier Hearth Summit Orchard Lantern Mistral""".split()
SUFFIXES = """Logistics Health Analytics Foods Robotics Capital Energy Labs Freight Retail
    Software Biotech Media Systems Outdoor Insurance Manufacturing Hospitality""".split()


def make_deals(today):
    rng = random.Random(SEED)
    accounts = [f"{p} {rng.choice(SUFFIXES)}" for p in rng.sample(PREFIXES, len(PREFIXES))]
    # Each AE owns a book of accounts. Weighting by position gives every book a few
    # big repeat customers, so "Top accounts" has a story.
    books = {email: accounts[i :: len(AES)] for i, (email, *_) in enumerate(AES)}

    rows = []
    for _ in range(DEALS):
        owner, region, win_rate, size = rng.choice(AES)
        book = books[owner]
        account = rng.choices(book, weights=[1 / (i + 3) for i in range(len(book))])[0]
        amount = rng.lognormvariate(10.0, 0.7) * size  # median around $22K
        amount = round(min(250_000, max(3_000, amount)) / 500) * 500
        cycle = rng.randint(14, 120)  # days from created to closed
        if rng.random() < 0.18:  # still open: the further into its cycle, the later the stage
            progress = rng.random()
            created = today - timedelta(days=int(cycle * progress))
            stage, closed_on = OPEN_STAGES[int(progress * 4)], None
        else:  # closed sometime in the last 12 months
            closed = today - timedelta(days=rng.randint(0, 364))
            created = closed - timedelta(days=cycle)
            stage = "Closed Won" if rng.random() < win_rate else "Closed Lost"
            closed_on = closed.isoformat()
        rows.append((account, owner, region, stage, amount, created.isoformat(), closed_on))
    return rows


def seed_if_empty(conn):
    """Insert the fake deals, unless the table already has rows.

    BEGIN IMMEDIATE takes SQLite's write lock *before* checking whether the table is
    empty, so the check and the insert are one step: a second process waits for the
    first to commit, then sees the rows and inserts nothing. A crash mid-seed rolls
    back, so a database is never half seeded.
    """
    conn.execute("BEGIN IMMEDIATE")
    try:
        if conn.execute("SELECT COUNT(*) FROM deals").fetchone()[0] == 0:
            rows = make_deals(date.today())
            conn.executemany(
                "INSERT INTO deals (account, owner, region, stage, amount, created_on, closed_on)"
                " VALUES (?, ?, ?, ?, ?, ?, ?)",
                rows,
            )
            print(f"seeded {len(rows)} fake deals", flush=True)
        conn.execute("COMMIT")
    except BaseException:
        conn.execute("ROLLBACK")
        raise
