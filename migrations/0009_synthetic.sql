-- 0009_synthetic.sql
-- Support for the activity simulator.
--
-- The simulator creates members and drives borrowing so the system can be seen
-- working with a populated catalogue rather than four seeded books. Everything
-- it creates must be identifiable as synthetic, for two reasons:
--
--   1. Nobody should ever mistake a simulated borrower for a real student, in a
--      report, a dashboard, or a conversation with a librarian.
--   2. Synthetic data must be removable in one statement when the library's real
--      records arrive. Data you cannot separate is data you cannot delete.

ALTER TABLE users ADD COLUMN is_synthetic boolean NOT NULL DEFAULT false;
ALTER TABLE books ADD COLUMN is_synthetic boolean NOT NULL DEFAULT false;

-- Reports and dashboards can exclude simulated members cheaply.
CREATE INDEX users_synthetic_idx ON users (is_synthetic) WHERE is_synthetic;

-- A record of every simulator pass: what it did, what it found, and whether the
-- system was healthy. This is the "did anything break overnight" log.
CREATE TABLE simulation_runs (
    id              uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    started_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz,
    -- ok | degraded | failed
    outcome         text,
    books_imported  int NOT NULL DEFAULT 0,
    copies_added    int NOT NULL DEFAULT 0,
    members_created int NOT NULL DEFAULT 0,
    loans_created   int NOT NULL DEFAULT 0,
    returns_made    int NOT NULL DEFAULT 0,
    reservations    int NOT NULL DEFAULT 0,
    -- Every rejection the API gave, counted by error code. A simulator that
    -- silently swallowed refusals would report a healthy system while the
    -- library quietly stopped lending.
    refusals        jsonb NOT NULL DEFAULT '{}'::jsonb,
    failures        jsonb NOT NULL DEFAULT '[]'::jsonb,
    notes           text
);

CREATE INDEX simulation_runs_started_idx ON simulation_runs (started_at DESC);
