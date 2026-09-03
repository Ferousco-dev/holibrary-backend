# Software Design Description
## HOLibrary Backend — Group 4 Online Library Management System

**Module path:** `github.com/Ferousco-dev/holibrary-backend`
**Repository:** `holibrary-backend` (display name: HOLibrary Backend)
**Phase:** 02 Architecture · **Gate:** G2
**Traces to:** docs/srs.md (REQ-001..074, NFR-001..020, DOM-001..009)

---

## 1. Architectural style

**A layered monolith, deployed as one container.**

This is a deliberate choice, and the reason matters at defence. The project brief warns
against "unnecessary enterprise complexity". Microservices would add network partitions,
distributed transactions and service discovery to a system whose entire dataset fits in
one Postgres instance. The genuinely hard problem here is *transactional integrity on a
single shared resource* (the last available copy) — which is easier, not harder, in one
process against one database.

```
        HTTP request
             |
   +---------v---------+   DES-001  transport: routing, auth, validation,
   |     handler       |            serialisation. No business rules.
   +---------+---------+
             |
   +---------v---------+   DES-002  service: use cases and business rules.
   |     service       |            Knows nothing about HTTP or SQL.
   +---------+---------+
             |
   +---------v---------+   DES-003  repository: SQL only. No business rules.
   |    repository     |
   +---------+---------+
             |
        PostgreSQL (Neon)
```

Dependencies point **inward only**. `domain` imports nothing from the project. This is what
makes the service layer unit-testable without a database, which is how NFR-012 (70% coverage)
is reached without a slow test suite.

### 1.1 Package layout (DES-004)

```
cmd/api/main.go              composition root: wire dependencies, start server
internal/
  config/                    env loading and validation (NFR-015)
  domain/                    entities, enums, business errors. Zero dependencies.
  service/                   use cases: auth, catalogue, circulation, member, ...
  repository/postgres/       SQL implementations of the repository interfaces
  transport/http/
    router.go                route table
    handler/                 one file per resource
    middleware/              auth, RBAC, rate limit, request ID, recovery, CORS
    response/                uniform success and error envelopes (NFR-017)
  auth/                      Argon2id hashing, JWT issue and verify
  notify/                    Resend email, FCM push
  queue/                     Redis outbox worker
  validate/                  request validation helpers (NFR-007)
migrations/                  numbered, forward-only SQL
docs/openapi.yaml            OpenAPI 3 specification (REQ-073)
```

`internal/` is used deliberately: Go's compiler forbids external import of these packages,
so the API surface is the HTTP API and nothing else.

---

## 2. Data model

### 2.1 The central design decision (DOM-002)

`book` and `copy` are separate tables. This is not normalisation for its own sake — it is
how Hezekiah Oluwasanmi Library actually works:

- **`books.call_number`** — the LCC class mark, e.g. `DT 515.15 .Ob21`. **Shared by every copy.**
- **`copies.accession_number`** — assigned on arrival. **Unique to one physical volume.**

Five copies of *Clean Code* are one `books` row and five `copies` rows.

### 2.2 Schema (DES-005)

```sql
-- users: members and staff in one table, exactly one role each (REQ-008)
users (
  id              uuid primary key,
  identifier      text not null unique,        -- matric or staff number (REQ-001)
  email           citext not null unique,
  full_name       text not null,
  password_hash   text not null,               -- Argon2id (NFR-002)
  role            user_role not null,          -- member | librarian | admin
  category        member_category,             -- undergraduate | postgraduate | staff
  status          user_status not null,        -- active | suspended | inactive
  must_change_password boolean not null default true,   -- REQ-007
  created_at, updated_at timestamptz
)

books (
  id            uuid primary key,
  title         text not null,
  subtitle      text,
  isbn13        text, isbn10 text,
  publisher     text, place_of_publication text, published_year int,
  call_number   text not null,                 -- LCC class mark (DOM-001)
  lcc_class     char(1) not null,              -- first letter, for wing derivation
  description   text,
  status        book_status not null,          -- active | archived (REQ-020)
  search_vector tsvector generated always as (...) stored,   -- REQ-031
  created_at, updated_at timestamptz
)

authors (id, name)          book_authors (book_id, author_id, position)   -- REQ-029
subjects (id, heading)      book_subjects (book_id, subject_id)           -- REQ-030

copies (
  id                uuid primary key,
  book_id           uuid not null references books(id),
  accession_number  text not null unique,      -- DOM-002, REQ-023
  loan_policy       loan_policy not null,      -- circulating | reference_only
                                               -- | on_display | restricted  (DOM-004)
  status            copy_status not null,      -- available | on_loan | lost
                                               -- | damaged | withdrawn  (REQ-025)
  acquired_at       date,
  created_at, updated_at timestamptz
)

loans (
  id           uuid primary key,
  copy_id      uuid not null references copies(id),
  user_id      uuid not null references users(id),
  borrowed_at  timestamptz not null,
  due_at       timestamptz not null,           -- REQ-042
  returned_at  timestamptz,                    -- null == still out
  issued_by    uuid not null references users(id),   -- NFR-020
  returned_to  uuid references users(id)
)

reservations (id, book_id, user_id, created_at, status, expires_at, notified_at)  -- DEC-003
audit_log    (id, actor_id, action, entity_type, entity_id, metadata jsonb, created_at)
refresh_tokens  (id, user_id, token_hash, expires_at, revoked_at)   -- NFR-003
password_resets (id, user_id, token_hash, expires_at, used_at)      -- REQ-005
outbox       (id, user_id, channel, template, payload jsonb,
              status, attempts, created_at, sent_at)                -- REQ-072
```

Note: **there is no `available_copies` column.** NFR-039 and REQ-039 require availability to be
derived. A stored counter is a second source of truth that drifts; a derived count cannot.

```sql
-- REQ-038: availability is a query, not a column
select count(*) filter (where status = 'available'
                          and loan_policy = 'circulating') as available,
       count(*) filter (where status = 'on_loan')          as on_loan,
       count(*)                                            as total
from copies where book_id = $1;
```

### 2.3 Derived values, never stored
| Value | Derivation | Requirement |
|---|---|---|
| Availability counts | `count(*) filter` over `copies.status` | REQ-038, REQ-039 |
| Overdue | `returned_at is null and due_at < now()` | REQ-052, REQ-053 |
| Wing | `lcc_class` in `A..J` → South, `K..Z` → North | DOM-003, REQ-027 |

Storing any of these creates a value that silently goes stale. That is the answer if an
examiner asks why there is no `is_overdue` column.

---

## 3. The critical design element: last-copy concurrency (DES-006)

**REQ-047 and NFR-009.** Two librarians issue the last copy of *Clean Code* at the same
instant. A naive read-then-write lends one physical book twice.

Defence is in **three layers, all in the database**:

**1. Atomic compare-and-swap.** The status check and the status change are one statement.
```sql
UPDATE copies SET status = 'on_loan'
 WHERE id = $1 AND status = 'available' AND loan_policy = 'circulating'
 RETURNING id;
```
Zero rows returned means another transaction won. The loser gets `409 Conflict`, never a
double loan. There is no window between check and write because there is no separate check.

**2. A partial unique index — the real guarantee.**
```sql
CREATE UNIQUE INDEX one_active_loan_per_copy
    ON loans (copy_id) WHERE returned_at IS NULL;
```
Even if every line of Go were wrong, Postgres physically cannot store two open loans against
one copy. This is NFR-009's "enforced by database constraint, not application code alone".

**3. Borrow-limit check inside the same transaction**, with the member row locked
(`SELECT ... FOR UPDATE`), so a member cannot exceed their category limit (REQ-043) by firing
concurrent requests.

All three run in one transaction. It commits or none of it happened.

---

## 4. Security design

| Concern | Design | Requirement |
|---|---|---|
| Password storage | Argon2id, per-password salt, tuned cost | NFR-002 |
| Sessions | JWT access token 15 min; opaque refresh token 7 days, **hash stored**, revocable | NFR-003, REQ-006 |
| Authorisation | Middleware resolves role from token; every protected route declares its required role. Ownership checks in the service layer — a member reads only their own loans | NFR-004, REQ-062 |
| Brute force | Redis fixed-window rate limit, 5 attempts/min/IP on auth routes | NFR-005 |
| Injection | `pgx` parameterised queries throughout. No string-built SQL anywhere | NFR-008 |
| Transport | HTTPS terminated at Cloudflare; HSTS; HTTP redirected | NFR-006 |
| Input | Every request body validated before it reaches a service | NFR-007 |
| Errors | Uniform envelope. Driver errors and stack traces logged, never returned | NFR-017 |
| Logging | `log/slog` structured. Passwords, tokens and member PII never logged | NFR-010, DOM-009 |
| Secrets | Environment only. `.env` git-ignored; `.env.example` committed | NFR-015 |
| CORS | Explicit origin allowlist. No wildcard | NFR-016 |

**Enumeration note:** password reset (REQ-004) returns the same response whether or not the
email exists. Otherwise the endpoint becomes a directory of registered students — a real
privacy leak given DOM-009.

---

## 4A. Time, date and timezone policy (DES-010)

Borrowing, due dates, returns, overdue detection, reminders, audit entries and
token expiry are all time. If the time model is wrong, all of them are wrong
together, and none of them announce it. So the rules are fixed here rather than
decided per-file.

### The eleven rules

| # | Rule |
|---|---|
| 1 | **Canonical timezone is UTC.** Everything stored and compared is UTC. |
| 2 | **Display timezone is `Africa/Lagos`** — by name, never as `UTC+1`. A name stays correct if the offset ever changes. |
| 3 | **PostgreSQL uses `TIMESTAMPTZ`** for every event time. Never plain `TIMESTAMP`. |
| 4 | **Go uses `time.Time`**, generated with `time.Now().UTC()`. |
| 5 | **APIs exchange RFC 3339**: `2026-09-17T18:15:00Z`. Never `17/09/26 6:15`. |
| 6 | **The server is the authority** on `created_at`, `updated_at`, `borrowed_at`, `returned_at`, audit times and token expiry. A client-supplied timestamp is not trusted for any of them. |
| 7 | **The frontend converts for display only.** It never decides anything. |
| 8 | **Due and overdue are decided in the backend.** |
| 9 | **Scheduled jobs compare UTC instants.** |
| 10 | **`DATE` for date-only values** (a copy's acquisition date); `TIMESTAMPTZ` for instants. |
| 11 | **Tests cover timezone and boundary cases.** |

### Why `TIMESTAMPTZ` and not `TIMESTAMP`

`TIMESTAMPTZ` stores an absolute instant. `TIMESTAMP` stores a wall-clock
reading with no zone attached, so a due date written in Lagos and read from a
server running UTC is silently an hour out — and nothing raises an error.
Silent wrongness is the worst failure mode available, so the type that cannot
produce it is the one used. All 21 timestamp columns in this schema are
`TIMESTAMPTZ`.

### One instant, three readings

```
        borrowed_at              due_at (undergraduate, +14 days)
   2026-09-03T18:15:00Z      2026-09-17T18:15:00Z     <- stored, compared
   03 Sep 2026, 7:15 PM      17 Sep 2026, 7:15 PM     <- shown, Africa/Lagos
   03 Sep 2026, 2:15 PM      17 Sep 2026, 2:15 PM     <- shown, New York
```

All three are the same instant. Whichever a reader sees, the server's answer to
"is this overdue" is identical.

### Overdue

```go
// Computed, never stored. An is_overdue column would be correct only until the
// clock moved past the next due date.
func (l Loan) IsOverdueAt(now time.Time) bool {
    return l.ReturnedAt == nil && now.After(l.DueAt)
}
```

The boundary is exclusive: a loan is overdue *strictly after* its due instant,
not at it. That off-by-one is the difference between warning a member and
accusing one.

### Notification schedule

```
 borrowed                                due                    overdue
 2026-09-03T18:15Z ................ 2026-09-17T18:15Z ...............>
                          |         |         |          |
                        -3 days   -1 day    due date   +1 day, +7 days
                          FCM       FCM       FCM       FCM + email
```

The worker compares UTC instants. `outbox.scheduled_at` lets a reminder be
queued when the loan is created rather than by a nightly scan of every open loan.

### Two deployment traps this policy closes

1. **The production image is `FROM scratch`** and carries no
   `/usr/share/zoneinfo`, so `time.LoadLocation("Africa/Lagos")` would fail
   there and nowhere else — passing every test on a laptop and breaking only
   once deployed. `import _ "time/tzdata"` embeds the database in the binary
   (+0.5 MB).
2. **`time.Local = time.UTC` at startup**, so the process behaves identically
   whatever the host is set to.

### Time columns in the schema

```
users        created_at, updated_at, last_login_at,
             password_changed_at, account_disabled_at
books/copies created_at, updated_at, acquired_at (DATE)
loans        borrowed_at, due_at, returned_at
reservations created_at, notified_at, expires_at
tokens       expires_at, revoked_at, used_at, created_at
outbox       created_at, scheduled_at, sent_at, read_at
audit_log    created_at
```

Deliberately absent: `import_started_at` / `import_completed_at`. CSV import is
a synchronous request that returns its own summary, so there is no long-running
job to track. Those columns would be structure with nothing behind them.

---

## 5. API surface (DES-007)

Base path `/api/v1`. Full detail in `docs/openapi.yaml` (REQ-073).

| Method | Path | Role | Requirements |
|---|---|---|---|
| POST | `/auth/login` | public | REQ-001 |
| POST | `/auth/refresh` | public | NFR-003 |
| POST | `/auth/logout` | any | REQ-006 |
| POST | `/auth/change-password` | any | REQ-003, REQ-007 |
| POST | `/auth/forgot-password` | public | REQ-004 |
| POST | `/auth/reset-password` | public | REQ-005 |
| GET | `/books` | public | REQ-028..035, REQ-037 |
| GET | `/books/{id}` | public | REQ-036, REQ-038 |
| POST | `/books` | librarian | REQ-016 |
| PATCH | `/books/{id}` | librarian | REQ-019 |
| POST | `/books/{id}/archive` | librarian | REQ-020 |
| GET | `/books/lookup?isbn=` | librarian | REQ-017, REQ-018 |
| POST | `/books/{id}/copies` | librarian | REQ-022, REQ-023 |
| PATCH | `/copies/{id}` | librarian | REQ-024..026 |
| POST | `/loans` | librarian | REQ-041..047 |
| POST | `/loans/{id}/return` | librarian | REQ-048..051 |
| GET | `/loans?overdue=true` | librarian | REQ-052, REQ-054 |
| GET | `/me/loans` | member | REQ-060 |
| GET | `/me/history` | member | REQ-061 |
| GET | `/me/reservations` | member | REQ-057 |
| POST | `/reservations` | member | REQ-055, REQ-056 |
| DELETE | `/reservations/{id}` | member | REQ-057 |
| GET | `/members` | librarian | REQ-012 |
| POST | `/members` | librarian | REQ-009 |
| POST | `/members/import` | librarian | REQ-010, REQ-011 |
| GET | `/members/{id}` | librarian | REQ-013, REQ-063 |
| PATCH | `/members/{id}` | librarian | REQ-014, REQ-015 |
| GET | `/admin/dashboard` | librarian | REQ-065, REQ-066 |
| GET | `/admin/audit` | admin | REQ-068 |
| GET | `/healthz` | public | REQ-074 |
| GET | `/docs` | public | REQ-073 |

### 5.1 Uniform envelopes (NFR-017)
```json
{ "data": { }, "meta": { "page": 1, "per_page": 20, "total": 143 } }
{ "error": { "code": "COPY_NOT_AVAILABLE", "message": "That copy is already on loan." } }
```
Machine-readable `code`, human-readable `message`. The frontend switches on `code`; the
message is safe to display and reveals nothing about internals.

---

## 6. Notifications (DES-008)

Requests never block on email or push (REQ-072). Writes go to an **outbox table in the same
transaction** as the business change; a background worker drains it.

```
service --(same tx)--> outbox row --> worker --> Resend (email) / FCM (push)
                                        |
                                   retry w/ backoff, attempts capped
```

The transactional outbox means a due-date reminder is never queued for a loan that rolled
back. Redis carries the worker's scheduling and the rate limiter; Postgres remains the source
of truth. This is why Redis is in the stack — a defensible reason, not a name (RSK-003).

---

## 7. Deployment (DES-009, DEC-010)

```
Browser -> Cloudflare (DNS, TLS, CDN, WAF) -> Render (Docker, Go binary)
                                                 |-> Neon (PostgreSQL)
                                                 |-> Upstash (Redis)
                                                 |-> Resend / FCM
```

Multi-stage Docker build: `golang:alpine` compiles a static binary, final stage is `scratch`
plus CA certificates. Target under 50 MB (NFR-011); realistically ~15 MB.

CI (NFR-013): every push runs `go build`, `go vet`, `golangci-lint`, `go test -race -cover`.
Red build blocks merge.

---

## 8. Traceability summary

| Design element | Covers |
|---|---|
| DES-001 handler layer | REQ-001..074 transport concerns, NFR-007, NFR-016, NFR-017 |
| DES-002 service layer | all business rules; REQ-041..059 |
| DES-003 repository layer | NFR-008 |
| DES-004 package layout | NFR-012, NFR-018 |
| DES-005 schema | DOM-001..008, REQ-016..040, REQ-064 |
| DES-006 concurrency control | REQ-047, NFR-009 |
| DES-007 API surface | REQ-073, and the route table above |
| DES-008 outbox notifications | REQ-069..072 |
| DES-009 deployment | NFR-006, NFR-011, NFR-013, NFR-015, NFR-018 |
| DES-010 time and timezone policy | REQ-042, REQ-052, REQ-053, REQ-069..072, NFR-003, NFR-020 |

## 9. Deferred
Renewals (DEC-009). Inter-library loan. Fines. Full-text search beyond Postgres `tsvector`
— revisit only if measured performance fails NFR-001, per Article 10.
