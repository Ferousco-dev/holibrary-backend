# Ìlànà Ledger — Group 4 Online Library Management System

Append-only. Newest at the bottom. Entries are never edited, only superseded.

---

## 2026-09-03 | G0 | conductor | GATE PASS
Cold start. Mode FLEET, rigour 3, process style hybrid.
Evidence:
  - Project brief received and analysed.
  - Document analysis performed on LIB 001 course material (402 pp) and
    SEN_106_merged.pdf (313 pp).
  - Key domain finding: HOL assigns a unique **accession number per physical copy**
    while all copies share a **call number**. This is the Book/Copy distinction,
    evidenced by the library's own documentation rather than assumed.
  - Further findings: LCC (not Dewey); South wing A-J, North wing K-Z;
    reference and on-display items do not circulate; borrower privileges vary
    by category; registration is in person.
  - SEN 106 material currently ends at CSS Flexbox. JavaScript, Git, hosting and
    security chapters are not yet written. Logged as RSK-003.
Decisions: DEC-001..DEC-013 recorded.
Risks: RSK-001..RSK-005 recorded.
Decision: advance to phase 01.

## 2026-09-03 | 01 | analyst | ARTIFACT EMITTED
Produced docs/srs.md (IEEE 830 shape) and .ilana/traceability.csv.
103 requirements: 74 functional, 20 non-functional, 9 domain.
All 9 domain requirements cite the LIB 001 material.

## 2026-09-03 | G1 | analyst | GATE PASS (caveat)
Evidence:
  - 103 rows in .ilana/traceability.csv, 0 duplicate identifiers.
  - Unmeasurable-adjective grep audit over docs/srs.md: 0 hits.
  - 8 of 20 NFRs carry numeric thresholds; the remaining 12 are
    binary-verifiable predicates.
Caveat: criterion 5 partial. No librarian or lecturer has reviewed the SRS.
Loan periods and limits (DEC-005) are stated assumptions, not confirmed facts.
Decision: advance to phase 02 Architecture.

## 2026-09-03 | 02 | architect | ARTIFACT EMITTED
Produced docs/design.md. DES-001..DES-009.
Repository: holibrary-backend, module github.com/Ferousco-dev/holibrary-backend.
Key decisions:
  - Layered monolith. Microservices rejected: the hard problem is transactional
    integrity on one shared resource, which is easier in one process.
  - No stored availability counter and no is_overdue column. Both derived,
    per REQ-039 and REQ-053, so neither can go stale.
  - Last-copy race (REQ-047, NFR-009) defended three ways: atomic compare-and-swap
    UPDATE, a partial unique index on loans(copy_id) WHERE returned_at IS NULL,
    and a FOR UPDATE limit check, all in one transaction.
  - Transactional outbox for notifications, which is the stated reason Redis is
    in the stack at all (mitigates RSK-003).

## 2026-09-03 | G2 | architect | GATE PASS
Evidence: docs/design.md §8 maps DES-001..009 across REQ, NFR and DOM sets.
All 9 domain requirements have a schema-level mechanism.
Phase 03 (interface design) deferred by scope: backend-first, frontend is a
separate repository and a later pass. Recorded, not skipped.
Decision: advance to phase 04 Construction.

## 2026-09-03 | 04 | constructor | CONSTRUCTION
Repository created at ~/Desktop/SCHOOL/holibrary-backend.
Module github.com/Ferousco-dev/holibrary-backend. Go 1.27.

Implemented: domain, auth (Argon2id + JWT + opaque tokens), config,
repository/postgres (users, catalogue, circulation, tokens, outbox, audit),
service (auth, catalogue, circulation, members), transport/http (router,
middleware, handlers, response envelopes), cmd/api, 4 migrations, Dockerfile,
docker-compose, GitHub Actions CI, Makefile, README, seed data.

Adopted mid-phase, on stakeholder input:
  - student profile fields (first/last name, faculty, department, level),
    migration 0003
  - CSV import dry-run preview with per-row validation summary and column
    aliases (student_id / matric_no / identifier)

Defects found and closed during construction: DEF-001, DEF-002, DEF-003.

## 2026-09-03 | 05 | verifier | EVIDENCE FROM A RUNNING SYSTEM
Stack brought up with docker compose (Postgres 17, Redis 7, API) and exercised.

  - go build, go vet, gofmt: clean
  - unit tests: pass, with -race
  - Docker image: 15 MB (NFR-011 budget was 50 MB)
  - health endpoint: 200, database reachable
  - catalogue search: 4 titles, wing derived correctly
    (DT -> South, Q and P -> North) (DOM-003)
  - reference-only dictionary reports not_for_loan and is_available=false
    (DOM-004)
  - loan period exactly 14 days for an undergraduate (DEC-005, REQ-042)
  - borrowing limit of 2 enforced; third loan rejected LOAN_LIMIT_REACHED
    (REQ-043)
  - reference copy rejected COPY_NOT_BORROWABLE (DOM-004, REQ-044)
  - double return rejected LOAN_ALREADY_CLOSED (REQ-051)
  - history survives return (DOM-008, REQ-061)
  - RBAC: student receives 403 on POST /books, POST /loans, GET /members,
    GET /admin/audit, GET /loans; 401 without a token; catalogue public
    (NFR-004, REQ-037)
  - CSV import: dry run reported 20 valid and wrote nothing; commit created 20

CONCURRENCY, REQ-047 and NFR-009, measured rather than asserted:
  20 simultaneous POST /loans for ONE copy.
  Result: 1 x HTTP 201, 19 x HTTP 409 COPY_NOT_AVAILABLE.
  Loans recorded against that copy: exactly 1. Copy status: on_loan.

## 2026-09-03 | 02 | architect | DESIGN AMENDED — DES-010
Time, date and timezone policy added to docs/design.md §4A on stakeholder input.
Eleven rules: UTC canonical, Africa/Lagos for display by name, TIMESTAMPTZ for
every event time, RFC 3339 on the wire, server-authoritative timestamps,
backend-decided overdue, DATE only for date-only values, timezone tests required.

Audit against the existing code:
  - 21 of 21 timestamp columns were already TIMESTAMPTZ. No change needed.
  - DEF-004 found and closed: the audit log appended a literal "Z" to a
    session-local time via to_char, mislabelling every entry as UTC.
  - time.Now() replaced with time.Now().UTC() at every site whose value is
    stored or compared. Rate-limiter and access-log sites deliberately left
    alone: they measure elapsed duration, not wall-clock instants.
  - time.Local pinned to UTC at startup.
  - tzdata embedded (DEC-016): the scratch image has no /usr/share/zoneinfo,
    so Africa/Lagos would have failed in production and nowhere else.
  - migration 0005: last_login_at, password_changed_at, account_disabled_at,
    outbox scheduled_at and read_at.
  - 5 timezone and boundary tests added, including the exclusive overdue
    boundary and a timezone-independence check across UTC, Africa/Lagos and
    the host zone.
Defects to date: DEF-001..DEF-004, all closed.

## 2026-09-03 | 04 | constructor | INVARIANT AUDIT
Audited the codebase against a 50-item bug-class list supplied by the
stakeholder. Wrote docs/SYSTEM_INVARIANTS.md: 18 invariants, each naming the
layer that enforces it, on the principle that no invariant may rest on Go
validation alone.

Already held, verified rather than assumed: parameterised queries throughout;
uniform error envelope with no driver detail leaked; API versioning; unknown
JSON fields rejected; page size clamped; ON DELETE RESTRICT protecting loan
history; no cached availability, so the stale-cache class cannot occur;
health check actually pings the database; graceful shutdown; fail-fast config;
CORS allowlist with no wildcard.

Six defects found and closed:
  DEF-005 CRITICAL privilege escalation - any librarian could create an admin
          by posting {"role":"admin"}. Mass assignment.
  DEF-006 HIGH     refresh tokens survived a password change or reset.
  DEF-007 HIGH     must_change_password was recorded but never enforced, so a
          temporary password was a fully working credential.
  DEF-008 MEDIUM   non-deterministic ORDER BY made pagination repeat and drop
          rows across pages.
  DEF-009 HIGH     no copy state machine. A borrowed copy could be edited back
          to available, abandoning its open loan.
  DEF-010 LOW      a privilege violation was reported as a validation failure.

All six verified fixed against the running stack. Defects to date: 10, all
closed. The three highest-severity defects in this project so far were all
found by inspection against a checklist, not by tests - which is the argument
for reviews preceding test execution (constitution article 5).

## 2026-09-03 | 04 | constructor | REQ-073 SWAGGER / OPENAPI
docs/openapi.yaml: OpenAPI 3.0.3, 21 paths, 24 operations, 14 schemas,
11 reusable responses. Served from the running service:
  GET /docs          interactive Swagger UI
  GET /openapi.yaml  the specification itself
The spec is embedded with go:embed so it ships inside the binary. There is one
copy of the file; docs/openapi.yaml is a symlink to the embedded one, so the
two cannot drift.

Two contract tests added against bug class 44 (documentation drift):
  - every route in the router must appear in the spec (24/24 verified)
  - every path in the spec must have a route serving it
Both fail the build if the contract and the implementation disagree, so the
frontend team cannot build against a stale document.

Traceability updated: 46 requirements now marked implemented with the file
that satisfies them.

## 2026-09-03 | 10 | constructor | OBSERVABILITY
Access log enriched with actor_id and actor_role. Middleware nests, so the
logger wraps the authenticator and runs first; the identity is carried back out
through a pointer placed in the context before the chain descends. Requests
returning 5xx now log at ERROR rather than INFO.

Bodies and query strings remain deliberately unlogged: they carry passwords,
reset tokens, search terms and member names (NFR-010, DOM-009).

DEC-017 recorded: Firebase serves FCM push only. Crashlytics and Performance
Monitoring are client-side products with no Go server SDK and cannot observe
this API. Stated so the defence answer is accurate rather than aspirational.

## 2026-09-03 | 04 | constructor | REQ-055..059 RESERVATIONS
Queue for titles that cannot be borrowed now. Members place their own
reservations; unlike borrowing, joining a queue commits nothing physical.

Refused when a copy is on the shelf (COPIES_AVAILABLE), when nothing about the
title is reservable (NOT_RESERVABLE), and when the member is already queued
(ALREADY_RESERVED, enforced by a partial unique index).

Queue position is computed on read, never stored: a stored position is wrong the
moment anyone ahead cancels. PromoteNext uses FOR UPDATE SKIP LOCKED so two
returns processed at once cannot promote the same member twice or skip anybody.
A return advances the queue after the return commits, and a queue failure is
logged rather than propagated: the book has physically come back and the record
must say so whatever the queue does.

Verified end to end: position 1 on create, ALREADY_RESERVED on a repeat,
COPIES_AVAILABLE for a title on the shelf, NOT_RESERVABLE for the reference
dictionary, promotion to 'ready' on return with push and email queued, and a
second member's DELETE returning 404 with the row surviving as pending.

## 2026-09-03 | 05 | verifier | NFR-012 COVERAGE GATE MET
domain 96.7%, service 78.6%, auth 83.8%. CI fails below 70% so it cannot
regress silently. The authentication service went from zero tests to twenty,
asserting security properties rather than happy paths: that an unknown account
and a wrong password are indistinguishable, that a suspended member can neither
sign in nor refresh, that refresh tokens are stored hashed and rotated, and that
changing or resetting a password ends every other session.

## 2026-09-03 | 04 | constructor | DEC-018 LAST-COPY RETENTION
Requested by the project owner, who believed the rule came from the LIB 001
material. It does not: the word "borrow" appears three times in 402 pages, and
the only restriction stated is that Recent Accessions on display may not be
borrowed but may be reserved. That was already implemented as the on_display
loan policy. Reported to the owner before building, and adopted as an explicit
policy decision instead of an attributed one.

Option A of three was chosen: retention applies only to titles held in two or
more circulating copies. A blanket rule would make every single-copy title
permanently unborrowable and strand most of the Africana holdings.

Enforced inside the borrow transaction after the copy is claimed, so two
librarians cannot each believe they are taking the second-to-last copy. Stock
counts only copies on the shelf or out, so losing a copy relaxes the rule rather
than tightening it. Search ?available=true and the reservation eligibility check
both key off the borrowable count rather than shelf presence, so a retained copy
neither looks borrowable nor blocks a queue.

Verified: 2-copy title lends one then refuses LAST_COPY_RETAINED; 1-copy title
lends normally; ?available=true drops the retained title; a member may queue for
a title whose only free copy is retained.

## 2026-09-03 | 04 | constructor | REQ-069..072 NOTIFICATION DELIVERY
Transactional outbox drained by a background worker. Resend for email, FCM for
push, and a console sender used outside production so the whole pipeline can be
run and demonstrated without a mail account.

The worker re-checks state before sending, which is the point of the design: a
reminder queued last night describes a situation that may have changed. Verified
live - a due-soon reminder for a book returned before the worker ran was recorded
as 'superseded' and never sent.

Also: FOR UPDATE SKIP LOCKED on the claim so a restart cannot double-send;
scheduled_at gating so a future-dated reminder waits; push fan-out across every
registered device; permanent FCM rejections retiring the device token rather than
retrying forever; permanent failures closed immediately and transient ones
retried five times; an unconfigured channel leaving messages queued rather than
marking them sent.

Migration 0006 adds device_tokens. The token is unique table-wide, not per user,
so a shared library terminal moves to whoever signs in rather than leaving the
previous member receiving somebody else's due dates. Verified.

Hourly schedule added: due-soon and overdue reminders, and releasing holds
nobody collected. Both recompute from the clock every pass.

8 worker tests. Contract tests caught the two undocumented device routes before
they were committed, which is what they are for.

## 2026-09-03 | 02 | architect | DES-011 INDEX STRATEGY
Index strategy added to docs/design.md section 2A, on stakeholder input.
Migration 0007. Every index validated with EXPLAIN ANALYZE against a seeded
dataset of 155,000 books, 38,000 members and 20,000 audit rows, then the
benchmark data was removed and the demonstration database rebuilt.

Measured:
  title substring search, 155k books  51.5 ms Seq Scan -> 21.9 ms Bitmap Index Scan
  member roll search, 38k members     22.5 ms Seq Scan -> 3.1 ms BitmapOr
  audit lookup by entity, 20k rows    filtered scan -> Index Scan, 0.03 ms
  overdue detection                   already Index Only Scan, 0 heap fetches

Two defects found by measurement, neither visible in the schema and neither
catchable by a test, because both queries returned correct rows all along:
  DEF-012 authors_name_trgm_idx was btree(lower(name)) under a name claiming
          trigram. A B-tree cannot serve a leading wildcard, so it was never
          used once. An index nothing uses costs write time and disk while
          making the schema look covered.
  DEF-013 the member search filtered full_name OR identifier OR email with
          trigram indexes on the first two only. An OR chain is only as
          indexable as its least-indexed branch, so Postgres used neither and
          scanned the whole roll.

Recorded and not a defect, but the most useful finding: at 5,000 books the
planner correctly ignored the trigram index, because at that size a sequential
scan is genuinely cheaper. It began using it at around 150,000 rows. HOL holds
over 750,000 volumes. Testing on seed data and declaring the index effective
would have proved nothing.

Also recorded: seven suggested indexes deliberately not added, each with its
reason, so the omissions read as decisions.
