-- 0005_time_fields.sql
-- Additional event timestamps, and the time policy stated in the schema itself.
--
-- Canonical storage is UTC. Every column below is TIMESTAMPTZ, which Postgres
-- stores as an absolute instant and returns in the session timezone. Plain
-- TIMESTAMP would store a wall-clock reading with no timezone attached, so a
-- due date written in Lagos and read anywhere else would be an hour wrong and
-- nothing would report an error.
--
-- Africa/Lagos exists only at the point of display, in the frontend. The named
-- zone is used rather than UTC+1 because a name survives any future change to
-- the offset; a hardcoded offset does not.

-- Session timezone for this database. Postgres converts TIMESTAMPTZ on output,
-- so pinning UTC means psql and the application agree on what they are looking
-- at, and no reader has to guess.
SET timezone = 'UTC';

ALTER TABLE users
    -- Supports "this account has not been used since last session" and gives
    -- the audit trail something to correlate a suspicious login against.
    ADD COLUMN last_login_at        timestamptz,
    -- Answers "when did this member last change their password", which is the
    -- question asked after a shared or leaked credential.
    ADD COLUMN password_changed_at  timestamptz,
    ADD COLUMN account_disabled_at  timestamptz;

ALTER TABLE outbox
    -- scheduled_at lets a reminder be written now and delivered later: the
    -- three-days-before notice is queued when the loan is created, not by a job
    -- that must scan every open loan every night.
    ADD COLUMN scheduled_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN read_at      timestamptz;

-- The worker claims due messages only, so a future-dated reminder waits.
DROP INDEX IF EXISTS outbox_pending_idx;
CREATE INDEX outbox_due_idx ON outbox (scheduled_at)
    WHERE status = 'pending';

-- Deliberately NOT added: import_started_at / import_completed_at. CSV import
-- is a synchronous request that returns its own summary, so there is no
-- long-running job whose progress needs recording. Those columns would be
-- structure with nothing behind it. If import ever becomes a background job,
-- they arrive with the table that tracks it.
