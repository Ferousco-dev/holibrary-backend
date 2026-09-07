CREATE TABLE saved_searches (
    id uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    query jsonb NOT NULL CHECK (jsonb_typeof(query) = 'object'),
    query_version integer NOT NULL DEFAULT 1 CHECK (query_version > 0),
    notifications_enabled boolean NOT NULL DEFAULT false,
    last_checked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX saved_searches_user_created_idx ON saved_searches (user_id, created_at DESC, id);
-- Reserved for a future opt-in matcher; no producer or delivery is enabled.
CREATE INDEX saved_searches_notification_check_idx ON saved_searches (last_checked_at, id)
    WHERE notifications_enabled;
