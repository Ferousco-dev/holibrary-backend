-- 0001_init.sql
-- Schema for HOLibrary. Traces to docs/design.md DES-005.
--
-- Modelled on Hezekiah Oluwasanmi Library. Two ideas drive this schema:
--   1. A book is bibliographic; a copy is a physical object (DOM-002).
--   2. Availability and overdue status are DERIVED, never stored, so they
--      cannot drift out of step with reality (REQ-039, REQ-053).

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS citext;

-- ---------------------------------------------------------------- enums

CREATE TYPE user_role       AS ENUM ('member', 'librarian', 'admin');
CREATE TYPE member_category AS ENUM ('undergraduate', 'postgraduate', 'staff');
CREATE TYPE user_status     AS ENUM ('active', 'suspended', 'inactive');
CREATE TYPE book_status     AS ENUM ('active', 'archived');

-- Why a copy might not be borrowable. Drawn from HOL's actual collections:
-- the Reference Room is consulted in place, and Recent Accessions on display
-- "may not be borrowed ... but may be reserved at the Loans desk" (DOM-004).
CREATE TYPE loan_policy AS ENUM ('circulating', 'reference_only', 'on_display', 'restricted');

CREATE TYPE copy_status        AS ENUM ('available', 'on_loan', 'lost', 'damaged', 'withdrawn');
CREATE TYPE reservation_status AS ENUM ('pending', 'ready', 'fulfilled', 'cancelled', 'expired');

-- ---------------------------------------------------------------- users

-- Members and staff share one table; role decides what they may do (REQ-008).
-- There is no public sign-up. Accounts originate at the library desk, mirroring
-- HOL, where a user presents an ID card and signs the register (DOM-006).
CREATE TABLE users (
    id                   uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    identifier           text   NOT NULL UNIQUE,   -- matriculation or staff number
    email                citext NOT NULL UNIQUE,
    full_name            text   NOT NULL,
    password_hash        text   NOT NULL,          -- Argon2id (NFR-002)
    role                 user_role   NOT NULL DEFAULT 'member',
    category             member_category,          -- null for staff accounts
    status               user_status NOT NULL DEFAULT 'active',
    must_change_password boolean     NOT NULL DEFAULT true,   -- REQ-007
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- A member's borrowing entitlement comes from their category, so a member
    -- without one has no defined limit. Staff roles do not borrow as members.
    CONSTRAINT members_need_a_category
        CHECK (role <> 'member' OR category IS NOT NULL)
);

-- ---------------------------------------------------------------- catalogue

CREATE TABLE books (
    id                   uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    title                text NOT NULL,
    subtitle             text,
    isbn13               text,
    isbn10               text,
    publisher            text,
    place_of_publication text,
    published_year       int,
    -- The LCC class mark, e.g. 'DT 515.15 .Ob21'. Shared by every copy of the
    -- title; it is the copies' accession numbers that differ (DOM-001, DOM-002).
    call_number          text NOT NULL,
    -- First letter of the class mark, kept as a column so the shelf wing can be
    -- derived and the catalogue browsed by LCC class (DOM-003).
    lcc_class            char(1) NOT NULL,
    description          text,
    status               book_status NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT lcc_class_is_a_letter CHECK (lcc_class ~ '^[A-Z]$'),
    CONSTRAINT published_year_is_plausible
        CHECK (published_year IS NULL OR published_year BETWEEN 1400 AND 2100)
);

CREATE TABLE authors (
    id   uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    name text NOT NULL UNIQUE
);

-- The card catalogue gives every title at least three access points: author,
-- title and subject (DOM-007). These two join tables provide the first and third.
CREATE TABLE book_authors (
    book_id   uuid NOT NULL REFERENCES books(id)   ON DELETE CASCADE,
    author_id uuid NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    position  int  NOT NULL DEFAULT 1,             -- 1 = main entry
    PRIMARY KEY (book_id, author_id)
);

CREATE TABLE subjects (
    id      uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    heading text NOT NULL UNIQUE
);

CREATE TABLE book_subjects (
    book_id    uuid NOT NULL REFERENCES books(id)    ON DELETE CASCADE,
    subject_id uuid NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    PRIMARY KEY (book_id, subject_id)
);

-- ---------------------------------------------------------------- copies

-- One row per physical volume on a shelf. The accession number is the library's
-- own identifier, assigned when the item arrives and unique to that single copy:
-- three copies of a title carry three accession numbers and one call number.
CREATE TABLE copies (
    id               uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    book_id          uuid NOT NULL REFERENCES books(id) ON DELETE RESTRICT,
    accession_number text NOT NULL UNIQUE,          -- DOM-002, REQ-023
    loan_policy      loan_policy NOT NULL DEFAULT 'circulating',
    status           copy_status NOT NULL DEFAULT 'available',
    acquired_at      date,
    notes            text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX copies_book_id_idx ON copies (book_id);
-- Supports the availability count, which filters on both columns (REQ-038).
CREATE INDEX copies_availability_idx ON copies (book_id, status, loan_policy);

-- ---------------------------------------------------------------- loans

-- A loan is an event record, not a mutation of the book. Returning a book closes
-- the record by setting returned_at; it never deletes it, because the library
-- must still be able to say who held a copy last year (DOM-008, REQ-064).
CREATE TABLE loans (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    copy_id     uuid NOT NULL REFERENCES copies(id) ON DELETE RESTRICT,
    user_id     uuid NOT NULL REFERENCES users(id)  ON DELETE RESTRICT,
    borrowed_at timestamptz NOT NULL DEFAULT now(),
    due_at      timestamptz NOT NULL,               -- from member category (REQ-042)
    returned_at timestamptz,                        -- NULL means still out
    issued_by   uuid NOT NULL REFERENCES users(id), -- which librarian (NFR-020)
    returned_to uuid REFERENCES users(id),

    CONSTRAINT due_after_borrow CHECK (due_at > borrowed_at),
    CONSTRAINT returned_after_borrow
        CHECK (returned_at IS NULL OR returned_at >= borrowed_at)
);

-- The load-bearing constraint of the whole system.
--
-- Two librarians can issue the last copy of a title at the same instant. The
-- service layer guards against it with an atomic compare-and-swap on the copy
-- row, but application code can be wrong. This index makes the bad state
-- physically unstorable: at most one open loan (returned_at IS NULL) may exist
-- per copy, enforced by Postgres regardless of what the Go code does.
-- REQ-047, NFR-009.
CREATE UNIQUE INDEX one_active_loan_per_copy
    ON loans (copy_id) WHERE returned_at IS NULL;

CREATE INDEX loans_user_idx ON loans (user_id, borrowed_at DESC);
-- Serves the overdue query, which reads only open loans (REQ-052).
CREATE INDEX loans_open_due_idx ON loans (due_at) WHERE returned_at IS NULL;

-- ---------------------------------------------------------------- reservations

-- HOL takes reservations at the Loans desk for items that cannot be borrowed
-- right now, including books on display (DOM-004, DEC-003).
CREATE TABLE reservations (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    book_id     uuid NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status      reservation_status NOT NULL DEFAULT 'pending',
    created_at  timestamptz NOT NULL DEFAULT now(),
    notified_at timestamptz,
    expires_at  timestamptz
);

-- Queue order is by created_at, and a member may queue for a title only once.
CREATE UNIQUE INDEX one_open_reservation_per_user_book
    ON reservations (book_id, user_id) WHERE status IN ('pending', 'ready');
CREATE INDEX reservations_queue_idx ON reservations (book_id, created_at);

-- ---------------------------------------------------------------- auth support

-- Refresh tokens are stored hashed, so a database leak does not hand out
-- sessions, and are revocable on logout (NFR-003, REQ-006).
CREATE TABLE refresh_tokens (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE password_resets (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,               -- 30 minutes (REQ-005)
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------- audit + outbox

-- Every state-changing staff action is attributable to an account and a
-- timestamp (NFR-020, REQ-068).
CREATE TABLE audit_log (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    actor_id    uuid REFERENCES users(id) ON DELETE SET NULL,
    action      text NOT NULL,
    entity_type text NOT NULL,
    entity_id   uuid,
    metadata    jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_created_idx ON audit_log (created_at DESC);

-- Transactional outbox. A notification row is written in the same transaction as
-- the change that caused it, so a reminder can never be queued for a loan that
-- was rolled back. A worker drains this table (DES-008, REQ-072).
CREATE TABLE outbox (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id    uuid REFERENCES users(id) ON DELETE CASCADE,
    channel    text NOT NULL,                      -- 'email' | 'push'
    template   text NOT NULL,
    payload    jsonb NOT NULL DEFAULT '{}'::jsonb,
    status     text NOT NULL DEFAULT 'pending',    -- pending | sent | failed
    attempts   int  NOT NULL DEFAULT 0,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz
);

CREATE INDEX outbox_pending_idx ON outbox (created_at) WHERE status = 'pending';
