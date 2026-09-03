-- 0006_devices.sql
-- Firebase Cloud Messaging device registrations.
--
-- A member may read the catalogue from a phone, a laptop and a library terminal,
-- so a single token column on users would silently drop two of the three. Each
-- registration is its own row.

CREATE TABLE device_tokens (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The FCM registration token. Unique across the whole table, not per user:
    -- when one student signs out of a shared library terminal and another signs
    -- in, the token must move to the new owner rather than leave the previous
    -- member receiving notifications about someone else's books.
    token      text NOT NULL UNIQUE,
    platform   text NOT NULL DEFAULT 'web',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    -- Set when FCM reports the token as permanently invalid, so a dead device
    -- is not retried forever.
    revoked_at timestamptz
);

CREATE INDEX device_tokens_user_idx ON device_tokens (user_id) WHERE revoked_at IS NULL;
