-- 0007_indexes.sql
-- Index strategy: indexes are added for queries this system actually runs, and
-- each one below was chosen after EXPLAIN ANALYZE showed the planner doing
-- something worse without it. See docs/design.md section 2.4.
--
-- Indexes are not free. Every INSERT, UPDATE and DELETE must maintain every
-- index on the table, so an over-indexed loans table would slow down the one
-- operation that has to be fast at the circulation desk. Nothing here is
-- speculative.

-- Trigram matching. Substring search (ILIKE '%clean%') cannot use a B-tree,
-- because a B-tree is ordered by prefix and a leading wildcard has no prefix.
-- Trigram indexes break the text into three-character runs and can match a
-- fragment anywhere in the string.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ---------------------------------------------------------------- books

-- Measured: a title search was a sequential scan over every book, discarding
-- 4,503 of 5,004 rows. The weighted full-text index (books_search_idx) serves
-- the q= parameter, but title= is a substring filter and full-text search does
-- not do substrings.
CREATE INDEX books_title_trgm_idx ON books USING gin (title gin_trgm_ops);

-- ---------------------------------------------------------------- authors

-- This index previously existed as btree(lower(name)) under a name that claimed
-- to be trigram. The author filter uses ILIKE '%...%', which a B-tree cannot
-- serve, so the index was never used and the query scanned every author.
-- An index that is never used is worse than no index: it costs write time and
-- disk while giving the reader a false sense that the query is covered. DEF-012.
DROP INDEX IF EXISTS authors_name_trgm_idx;
CREATE INDEX authors_name_trgm_idx ON authors USING gin (name gin_trgm_ops);

-- ---------------------------------------------------------------- users

-- Measured: the member roll search scanned all 3,003 users to find 375.
-- A librarian searching for a student at the desk should not wait on a scan.
CREATE INDEX users_full_name_trgm_idx ON users USING gin (full_name gin_trgm_ops);
CREATE INDEX users_identifier_trgm_idx ON users USING gin (identifier gin_trgm_ops);

-- email is citext, so the index is built on the text cast and the query casts to
-- match. This one is not optional: an OR chain is only as indexable as its
-- least-indexed branch, and leaving email out meant Postgres fell back to
-- scanning the whole roll even though the other two columns were indexed.
-- Measured at 38,000 members: 22.5 ms before, 3.1 ms after. DEF-013.
CREATE INDEX users_email_trgm_idx ON users USING gin ((email::text) gin_trgm_ops);

-- ---------------------------------------------------------------- loans

-- The borrowing-limit check and GET /me/loans both ask for one member's OPEN
-- loans. loans_user_idx covers user_id but spans the member's whole history,
-- most of which is returned and irrelevant. This partial index contains only
-- open loans, so it stays small however long the library operates.
CREATE INDEX loans_user_open_idx ON loans (user_id) WHERE returned_at IS NULL;

-- ---------------------------------------------------------------- reservations

-- Measured: GET /me/reservations was a sequential scan. reservations_queue_idx
-- is ordered (book_id, created_at) for the queue itself and cannot serve a
-- lookup by member.
CREATE INDEX reservations_user_open_idx ON reservations (user_id)
    WHERE status IN ('pending', 'ready');

-- ---------------------------------------------------------------- audit_log

-- "What happened to this loan?" is the question an audit trail exists to answer.
-- audit_log_created_idx orders the whole log by time and cannot answer it
-- without scanning.
CREATE INDEX audit_log_entity_idx ON audit_log (entity_type, entity_id);

-- "What did this librarian do?" -- the second question, asked after the first.
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, created_at DESC);

-- ---------------------------------------------------------------- not added
--
-- Recorded so the omissions read as decisions rather than oversights:
--
--   loans(status)              There is no status column. A loan's state is
--                              derived from returned_at and due_at, so there is
--                              nothing to index (I-02, I-08).
--   loans(status, due_at)      Superseded by loans_open_due_idx, a partial index
--                              on open loans only. It answers the same question
--                              from a smaller structure.
--   notifications(user_id,     No member-facing notification list exists yet.
--     read_at)                 The column is there for when one does.
--   import_jobs(*)             There is no such table. CSV import is a
--                              synchronous request that returns its own summary.
--   users(role, status)        No query filters on both together.
--   books(created_at)          No "recently added" query exists yet. HOL does
--                              display Recent Accessions, so this is a likely
--                              future need -- but a likely need is not a need.
--   books(author)              Authors are a join table, not a column, because a
--                              book may have several. The trigram index on
--                              authors.name serves the search instead.
