# HOLibrary Backend

An online library management system for **Hezekiah Oluwasanmi Library**, Obafemi
Awolowo University, Ile-Ife.

Group 4 · SEN 106 / SEN 216, Introduction to Web Technologies

---

## What this is

A **modernised Online Public Access Catalogue with an integrated circulation
module**. It digitises two things HOL currently does with a card catalogue and a
paper register at the Loans desk:

1. **Discovery**: does the library have this book, is a copy free, and where is it?
2. **Custody**: which member holds which physical copy, from when, until when.

It is **not** an e-library. No book content is stored, read, uploaded or sold.
Every book in this system is a physical volume standing on a shelf in the
building.

## The idea the whole design rests on

A **book** and a **copy** are not the same thing, and HOL's own vocabulary says
so. From the LIB 001 course material:

> Accession number is the number given to an item as it comes to the library. It
> is unique to a copy of a title, if three copies of a title come to the
> library, each will be given a different accession number. The class number of
> the 3 copies will be the same.

So:

| Concept | HOL term | Scope |
|---|---|---|
| Bibliographic record | **Call number** (`DT 515.15 .Ob21`) | shared by every copy |
| Physical volume | **Accession number** (`523153`) | unique to one copy |

Five copies of *Clean Code* are one `books` row and five `copies` rows. That is
why the schema looks the way it does.

Two further consequences, both deliberate:

- **There is no `available_copies` column.** Availability is counted from copy
  states on every read. A stored counter is a second source of truth and drifts.
- **There is no `is_overdue` column.** Overdue is computed from the clock. A
  stored flag is correct only until the clock moves.

## Concurrency: the last copy

Two librarians issue the final copy of a title at the same instant. Three layers
stop that becoming one book lent twice, all in the database:

```sql
-- 1. atomic compare-and-swap: no window between checking and writing
UPDATE copies SET status = 'on_loan'
 WHERE id = $1 AND status = 'available' AND loan_policy = 'circulating'
 RETURNING id;              -- zero rows => you lost the race => 409

-- 2. the real guarantee: this bad state cannot be stored at all
CREATE UNIQUE INDEX one_active_loan_per_copy
    ON loans (copy_id) WHERE returned_at IS NULL;
```

Plus a borrowing-limit check taken **while holding a row lock on the member**,
all inside one transaction. Even if every line of Go were wrong, Postgres refuses
to lend the same physical volume twice.

> An earlier version of this file claimed that `FOR UPDATE` lock and the code did
> not contain it. An adversarial audit found the gap, and a test reproduced it: 5
> simultaneous borrows against a limit of 2 produced **3 loans**. The lock is
> real now, and the test that caught it runs in the suite. The lesson is recorded
> rather than quietly deleted, a comment asserting a safety property is a
> hypothesis, not an enforcement.

## Not everything circulates

HOL does not lend its whole collection, so neither does this system:

| Policy | Borrowable | Reservable | Basis |
|---|---|---|---|
| `circulating` | yes | yes | main lending collection |
| `reference_only` | no | no | Reference Room, consulted in place |
| `on_display` | no | **yes** | Recent Accessions, *"may not be borrowed while on display but may be reserved at the Loans desk"* |
| `restricted` | no | no | Africana, OAU Publications, Serials, Conservation |

## Indexing

Indexes exist for queries this system actually runs, and **every one was
validated with `EXPLAIN ANALYZE`**. You cannot tell whether Postgres is using an
index by reading the schema.

| Query | Before | After |
|---|---|---|
| Title search, 155k books | 51.5 ms `Seq Scan` | **21.9 ms** `Bitmap Index Scan` |
| Member search, 38k members | 22.5 ms `Seq Scan` | **3.1 ms** `BitmapOr` |

Three things the measurements taught us, all of them counter-intuitive:

- **At 5,000 books the planner correctly ignored the new trigram index** and
  scanned the table, because at that size a scan really is cheaper. It only
  started paying at around 150,000 rows. HOL holds 750,000 volumes, so the index
  is justified by the target scale, not by a laptop with seed data on it.
- **An `OR` chain is only as indexable as its least-indexed branch.** Member
  search filtered `full_name OR identifier OR email`; two had trigram indexes and
  `email` did not, so Postgres used *neither* and scanned all 38,000 members.
- **A B-tree cannot serve a leading wildcard.** This project shipped an index
  named `authors_name_trgm_idx` that was actually `btree(lower(name))` and could
  never have served the `ILIKE '%...%'` it was built for.

Partial indexes carry real weight here: a library accumulates returned loans
forever, but nearly every question concerns open ones, so `WHERE returned_at IS
NULL` keeps those indexes proportional to books currently out rather than to the
library's whole history.

Full strategy, including what is deliberately **not** indexed and why:
`docs/design.md` §2A (DES-011).

## Last-copy retention

A title held in **two or more** circulating copies always keeps one on the shelf,
so a reader who walks in can still consult it:

```
5 copies, 3 free  ->  2 borrowable, 1 stays
2 copies, 1 free  ->  0 borrowable, it stays      LAST_COPY_RETAINED
1 copy,   1 free  ->  1 borrowable                a lone copy still circulates
```

That last line is the important one. A blanket "never lend the last copy" would
make every single-copy title permanently unborrowable, at HOL that would strand
most of the Africana and OAU Publications holdings.

**This is our stated policy, not a rule from the LIB 001 material.** That
document restricts only the Reference, Recent Accessions, Serials, Africana and
Conservation collections. Worth being precise about: a policy you chose and can
justify is defensible; a policy you attribute to a document that does not contain
it is not.

The API distinguishes three things a reader cares about: `on_shelf` (you can come
and read it), `borrowable` (you can take it away), and `shelf_copy_retained`
(why a book that is present cannot be borrowed).

## Membership begins in the building

There is no `POST /auth/register` and there never will be. At HOL a user
*"must present the university identity and library card and sign the register"*.
So:

```
Student registers physically at the library
        ↓
Librarian creates the account (singly, or by CSV import)
        ↓
Temporary password handed over
        ↓
First sign-in forces a password change
        ↓
Student signs in with matric number or email + password
```

This mirrors reality **and** removes the entire class of attacks that begins
with an attacker creating their own account.

---

## The activity simulator

A library with four books in it does not look like a library. `cmd/simulator` is
a scheduled agent that stocks the catalogue from openlibrary.org, registers
members, and drives borrowing and returns according to a behaviour model, then
checks the library is still internally consistent and reports.

```bash
SIMULATOR_PASSWORD=... go run ./cmd/simulator -login librarian@oauife.edu.ng
```

```
HOLibrary activity simulator - hol-reader-behaviour v1.0.0
OK  (2.6s)

  what it did
    titles imported   12
    copies shelved    19
    members added     5
    books lent        6
    books returned    6

  consistency checks
    [pass] the catalogue is reachable without signing in
    [pass] availability never exceeds the number of copies held
    [pass] no copy is out on more than one loan
    [pass] a member cannot reach staff routes
    [pass] the service reports its database reachable
```

**It is not an AI model.** There is no training, no learned weights, no
inference. `model.json` is 4.7 KB of hand-chosen probabilities, closer to a
game's loot table than to a neural network. The pattern has a proper name,
**synthetic monitoring**: exercising a live system with generated traffic to
prove it works, rather than waiting for a real user to discover that it does not.
Describing it accurately is worth more than describing it impressively.

**It drives the public API, not the database.** Writing rows directly would
populate the catalogue while proving nothing. Going through the API means every
pass re-exercises authentication, authorisation, validation, the borrowing rules
and the concurrency guards, exactly as a librarian's browser would. There is no
back door for the simulator, when it tripped the library's own login rate limit
that was the limiter working and the simulator behaving badly.

The model makes the generated activity resemble a real library rather than
uniform noise:

- **Demand is Zipf-distributed**: the top 10% of titles take **74%** of
  borrowing. Uniform sampling would look busy but would never produce the one
  situation the system most needs to handle: everybody wanting the same book.
- **Holdings are long-tailed**: most titles in one or two copies, a few core
  texts in many.
- **18% of copies do not circulate**, matching HOL's reference, display and
  restricted collections.
- **Four reader archetypes** with different borrowing and returning rates.

Everything it creates is flagged `is_synthetic` and uses a non-deliverable
`@simulated.invalid` address, so no simulated borrower can ever be mistaken for a
real student and nothing it creates can email a real person. Each pass is
recorded in `simulation_runs` with what it did, which rules fired, and whether
every consistency check held.

## Running it

### Locally, with Docker

```bash
cp .env.example .env      # then edit
docker compose up --build
curl http://localhost:8080/healthz
```

Postgres, Redis and the API all come up. Migrations apply automatically on the
first start of an empty volume.

### Locally, without Docker

```bash
cp .env.example .env      # point DATABASE_URL at any Postgres
make migrate
make run
```

### Useful targets

```bash
make test     # go test -race ./...
make cover    # coverage summary
make docker   # build the production image
make help     # everything else
```

---

## Architecture

A **modular monolith**: one Go application, one container, internally layered.

```
        HTTP request
             │
   ┌─────────▼─────────┐   transport  - routing, auth, validation, JSON
   │      handler      │                no business rules
   ├─────────▼─────────┤   service    - use cases and library rules
   │      service      │                knows nothing about HTTP or SQL
   ├─────────▼─────────┤   repository - SQL only
   │    repository     │
   └─────────┬─────────┘
             ▼
        PostgreSQL
```

Dependencies point inward only, and `internal/domain` imports nothing from the
project at all. That is what lets the borrowing rules be unit-tested with no
database and no web server.

**Microservices were considered and rejected.** The hard problem here is
transactional integrity on a single shared resource, the last copy, which is
strictly easier in one process against one database. Splitting it would add
distributed transactions and service discovery and solve nothing.

```
internal/
  domain/                entities, enums, rules. Zero dependencies.
  service/               use cases: auth, catalogue, circulation, members
  repository/postgres/   SQL
  transport/http/        router, handlers, middleware, response envelopes
  auth/                  Argon2id hashing, JWT and opaque tokens
  config/                environment loading
migrations/              numbered, forward-only SQL
```

## Stack, and why

| Choice | Reason |
|---|---|
| **Go** | The last-copy race is the core problem; Go's tooling and static binary make it easy to reason about and to deploy. 15 MB image. |
| **PostgreSQL** (Neon) | Constraints and transactions are the correctness mechanism, not an afterthought. Neon's free tier does not expire; Render's Postgres does. |
| **Redis** (Upstash) | Rate limiting and background job coordination. **Not** email, Redis is not a mail system. |
| **Resend** | Email delivery. Requires a DNS-verified sending domain. |
| **FCM** | Push notifications for due-soon, overdue and reservation-ready. |
| **Docker** | One artefact, identical locally and in production. |
| **GitHub Actions** | Build, vet, gofmt check and race-detector tests on every push. |
| **Cloudflare** | DNS, TLS, CDN and WAF in front of the API, the way Cloudflare is actually used in production. |
| **OpenAPI** | The frontend is a separate repository and consumes this contract. |

No component is here without a problem it solves. That is deliberate: every
choice has to be defensible out loud.

## Notifications

Nothing is sent on the request path. A service writes an outbox row **in the same
transaction** as the change that caused it, and a worker delivers it later:

```
service --(same tx)--> outbox row --> worker --> Resend (email) / FCM (push)
```

The transaction is the point: a due-date reminder can never be queued for a loan
that rolled back.

**The worker re-checks the world before sending.** A reminder queued last night
describes a situation that may have changed:

```
"your book is due tomorrow"   queued
member returns the book        five minutes later
worker runs                    -> superseded, not sent
```

The queue records an *intention*; only the database knows whether the intention
still holds.

Other things the worker gets right:

- `FOR UPDATE SKIP LOCKED` on the claim, so two workers, or a restart
  overlapping the previous process, never send the same message twice.
- `scheduled_at` gates the claim, so a reminder written today for next week waits.
- A push **fans out to every device** a member has registered. One stored token
  would reach whichever device happened to register last.
- A device token FCM reports as permanently dead is **retired**, not retried
  forever, and one dead device does not stop a working one from being told.
- A permanent failure (malformed address) is closed immediately; a transient one
  is retried up to five times.
- A channel with **no provider configured leaves its messages queued** rather
  than marking them sent, so nothing is lost while an account is being set up.

Outside production, with no mail provider configured, notifications are written
to the log instead. The outbox, the re-check, the retry accounting and the worker
all behave exactly as they will in production; only the final hop changes. The
message body is never logged, a password reset body carries a working token.

## Observability

One structured JSON line per request:

```json
{"level":"INFO","msg":"request","method":"POST","path":"/api/v1/loans",
 "status":201,"duration_ms":8,"request_id":"47e6d440-...",
 "actor_id":"3f0a...","actor_role":"librarian"}
```

`request_id` is returned to the caller in `X-Request-ID`, so a member reporting
"it failed at about 6:30" can be traced to an exact line, and that line
correlates with the audit trail.

**Not logged:** request bodies and query strings. They carry passwords, reset
tokens, search terms and member names. A borrowing history is a record of what a
named student reads and does not belong in a log aggregator.

A note on **Firebase**: it is in this stack for **FCM push notifications only**.
Crashlytics and Performance Monitoring are client-side products for mobile and
web apps, they have no Go server SDK and cannot observe this API. Backend
observability here is structured logs plus the health endpoint; a log drain or
an error tracker with a real Go SDK can be added later if the need is measured
rather than assumed.

## Time and timezone

Borrowing, due dates, overdue detection, reminders, audit entries and token
expiry are all time. If the time model is wrong they are all wrong together, and
none of them announce it. So one rule governs:

> **Store UTC. Display `Africa/Lagos`.**

```
   2026-09-03T18:15:00Z      2026-09-17T18:15:00Z     stored and compared
   03 Sep 2026, 7:15 PM      17 Sep 2026, 7:15 PM     shown, Africa/Lagos
```

- Every event column is **`TIMESTAMPTZ`**: 21 of 21. Plain `TIMESTAMP` stores a
  wall-clock reading with no zone, so a due date written in Lagos and read from a
  UTC server is silently an hour out and nothing raises an error.
- The wire format is **RFC 3339**, never `17/09/26 6:15`.
- The **server** generates `borrowed_at`, `returned_at`, audit times and token
  expiry. A client-supplied timestamp is not trusted for any of them.
- **Overdue is decided here, not in the browser**, and the boundary is
  exclusive, overdue strictly *after* the due instant.
- The zone is named (`Africa/Lagos`), not an offset (`UTC+1`), so it stays
  correct if the offset ever changes.
- `import _ "time/tzdata"` embeds the timezone database, because the production
  image is `FROM scratch` and has no `/usr/share/zoneinfo`: a bug that would
  appear in production and nowhere else.
- `time.Local = time.UTC` at startup, so the host's zone is irrelevant.

Full policy: `docs/design.md` §4A (DES-010).

## Security

| Concern | Mechanism |
|---|---|
| Passwords | Argon2id, per-password salt, PHC-encoded parameters |
| Sessions | 15-minute JWT access token; opaque refresh token, stored hashed, rotated on every use |
| Authorisation | Role checked **server-side** on every protected route. A hidden menu is not access control. |
| Brute force | **5 attempts/minute per account** (the control an attacker cannot dodge by changing address), plus a generous 120/minute per network. Counters live in Redis, so a limit survives a restart and holds across instances. |
| Proxy headers | `CF-Connecting-IP` / `X-Forwarded-For` are trusted **only** when `TRUST_PROXY_HEADERS=true`. They are ordinary headers anyone can forge if the origin is reachable directly. |
| Session invalidation | A password change stamps `tokens_invalid_before` **in the same statement as the new hash**, so there is no window for a concurrent refresh to mint a surviving session |
| Login timing | The unknown-account path performs the same Argon2 work as a real verification, so membership cannot be read off the clock |
| Injection | Parameterised queries throughout; no string-built SQL |
| Enumeration | Password reset replies identically whether or not the address is registered |
| Privacy | A member's record is reached via `/me`, never by an id in the URL, so there is no parameter to tamper with |
| Logging | Structured; never logs bodies, query strings, tokens or member data |
| Transport | HTTPS at Cloudflare, HSTS, `nosniff`, `DENY` framing |
| Secrets | Environment only; `.env` git-ignored |
| Uploads | 1 MiB JSON bodies, 8 MiB CSV imports |

A borrowing history is a record of what a named student reads. It is treated as
sensitive throughout.

## API

Base path `/api/v1`. **The API documents itself:**

| | |
|---|---|
| `GET /docs` | Interactive Swagger UI |
| `GET /openapi.yaml` | The OpenAPI 3 specification, for code generators |

The spec is **embedded in the binary**, so the documentation ships with the
artefact and cannot go missing on a container with no source tree. There is one
copy of the file: `docs/openapi.yaml` is a symlink to the embedded one, so the
two cannot drift.

Two tests hold the contract honest: every route in the router must appear in the
spec, and every path in the spec must have a route serving it. Documentation that
has quietly drifted is worse than none, because the frontend builds against it
and finds out at integration time.

| Access | Routes |
|---|---|
| **Public** | `GET /books`, `GET /books/{id}`, `POST /auth/login`, refresh, forgot/reset password, `GET /healthz` |
| **Signed in** | logout, change-password, `GET /me/loans`, `GET /me/history` |
| **Librarian** | book and copy management, `POST /loans`, `POST /loans/{id}/return`, `GET /loans?overdue=true`, member management and CSV import, dashboard |
| **Admin** | `GET /admin/audit` |

Responses have exactly one success shape and one error shape:

```json
{ "data": {}, "meta": { "page": 1, "per_page": 20, "total": 143 } }
{ "error": { "code": "COPY_NOT_AVAILABLE", "message": "That copy is already on loan." } }
```

`code` is stable and machine-readable; `message` is safe to show a user.

### CSV import

```bash
# validate without writing - preview first
curl -X POST "$API/api/v1/members/import?dry_run=true" \
     -H "Authorization: Bearer $TOKEN" -F file=@intake.csv
```

```csv
student_id,first_name,last_name,email,department,level
SWE/2025/001,Feranmi,Oresajo,feranmi@oauife.edu.ng,Software Engineering,200
```

Common column aliases (`student_id`, `matric_no`, `identifier`) are all accepted.
The response counts valid, duplicate and invalid rows and names the line number
of every problem. A bad row never aborts the batch.

## Testing

```bash
make test
```

Business rules are tested against fakes, so the suite needs no database and runs
in seconds. `DEF-001`: a due-soon reminder that would have been sent for a book
already returned, was caught this way, before the code ever ran against
Postgres.

## Process

This project is built under a gated process. `docs/` and `.ilana/` in the parent
project directory carry the SRS, the design description, the traceability matrix
from requirement to code to test, the decision records, the risk register and
the defect log.

Every requirement has an identifier. Comments in this codebase cite them:
`DOM-002` is the accession number rule, `NFR-009` is the availability invariant,
`REQ-047` is the concurrency requirement. If a comment explains *why* rather than
*what*, that is on purpose.

## Not in scope

Reading books online, PDF or EPUB, ebook downloads, audiobooks, DRM, book sales,
payments, and fines. Excluded from the architecture, not merely unimplemented.
Renewals and inter-library loan are deferred, and recorded as deferred.
