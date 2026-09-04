-- Bookmarks: a member saving a title to come back to.
--
-- A bookmark is an interest in a TITLE, not a claim on a copy. It reserves
-- nothing, affects no queue and changes no availability. That is the whole
-- distinction from a reservation, and it is why the row references books and
-- never copies.
--
-- Deliberately not stored: any ordering the member chooses. The list is shown
-- newest first, which needs no column, and a manual sort order is a feature
-- nobody asked for that would need maintaining on every insert.

CREATE TABLE bookmarks (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    book_id    uuid NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Bookmarking the same title twice is not an error worth reporting to a
-- reader, but it must not create a second row. The unique index makes the
-- second attempt a no-op the service can absorb, rather than a duplicate the
-- list would show twice.
CREATE UNIQUE INDEX one_bookmark_per_user_book ON bookmarks (user_id, book_id);

-- The only query this table serves: "this member's bookmarks, newest first".
-- created_at DESC is in the index so the read needs no sort, and id breaks
-- ties so pagination cannot repeat or drop a row (DEF-008).
CREATE INDEX bookmarks_by_user_idx ON bookmarks (user_id, created_at DESC, id);
