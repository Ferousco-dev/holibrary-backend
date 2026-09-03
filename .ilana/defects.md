# Defect Log

| ID | Description | Severity | Found by | Found in | State | Fix |
|---|---|---|---|---|---|---|
| DEF-019 | **Rate limiting was ineffective in both directions.** It was keyed on client IP only, held in an in-process map (lost on every restart, no coordination across instances), and trusted `CF-Connecting-IP` / `X-Forwarded-For` unconditionally. Five students behind one campus NAT could lock out a sixth with correct credentials; meanwhile anyone reaching the origin directly could send a fresh forged header per request and never be limited at all — or forge a victim's address to lock *them* out. | **High** | Adversarial audit | `internal/transport/http/middleware/middleware.go` | **Closed** | Two limits: **5/min per account** (precise — an attacker cannot change the account they are attacking) and 120/min per network (coarse — a campus NAT is shared by thousands). Counters in Redis, so limits survive restarts and hold across instances, with an in-process fallback so a Redis outage degrades the limiter rather than the service. Proxy headers trusted only when `TRUST_PROXY_HEADERS=true`. |
| DEF-018 | **Any authenticated user could unregister another user's device.** `DELETE /me/devices` matched on the FCM token alone, with no ownership condition. Anyone who learned a victim's token — from a shared browser profile, client storage, or a log — could silently switch off that victim's due-date and reservation notifications from their own account. | **High** | Adversarial audit | `internal/repository/postgres/outbox_repo.go`, `device_handler.go` | **Closed** | `RevokeOwnDeviceToken` puts `user_id` in the `WHERE` clause. A token that exists but belongs to someone else returns the same 404 as one that does not exist, so the endpoint is not an oracle for guessed tokens. |
| DEF-017 | **Login was an account-enumeration oracle by timing.** The unknown-account path returned after a cheap index miss; the wrong-password path paid for a 64 MiB Argon2id computation. The error text was identical, but the latency was not — roughly 11 ms apart, trivially separable across a network. An attacker could sweep matriculation numbers and read library membership off the clock. | Medium | Adversarial audit | `internal/service/auth_service.go`, `internal/auth/password.go` | **Closed** | The miss path now verifies against a dummy Argon2 hash generated at startup. Measured ratio after the fix: **1.10**, with a test that fails if it drifts outside 0.5–2.0. |
| DEF-016 | **Pending reservations expired before any copy was ready.** `expires_at` was set when the member joined the queue, and the sweep expired `pending` and `ready` alike. A member waiting for a popular title was silently dropped from the queue after three days, having never been offered anything — and `PromoteNext` then skipped straight past them to the next student. The hold period is a deadline for *collecting*, not a limit on how long one may *wait*. | **High** | Adversarial audit | `internal/repository/postgres/reservation_repo.go`, migration `0008` | **Closed** | A pending reservation has no expiry. `PromoteNext` sets `expires_at` when a copy is actually held, and the sweep touches `ready` only. |
| DEF-015 | **A password change could be raced, leaving the attacker's session alive.** `UpdatePassword` and `RevokeAllRefreshTokens` were separate statements. A concurrent `POST /auth/refresh` with a stolen token could consume it and insert a *new* refresh token in the gap between them — so the victim's password change, the exact action taken to end a compromise, handed the attacker a fresh session instead. | **Critical** | Adversarial audit | `internal/service/auth_service.go`, `user_repo.go`, migration `0008` | **Closed** | `users.tokens_invalid_before` is stamped in the **same statement** as the new hash, and refresh checks it after consuming a token. Nothing has to be revoked, so there is no gap to race. |
| DEF-014 | **The borrowing limit could be exceeded by concurrent requests.** The limit was counted inside the borrow transaction but without serialising on the borrower. Two librarians lending to the same member each claim a *different* copy, so the copy-level compare-and-swap never collides; both then count the member's open loans before either has inserted its own row, both read the same number, and both pass. **Reproduced: 5 simultaneous borrows against a limit of 2 produced 3 loans.** Worse, `README.md` and `docs/design.md` both claimed a `SELECT ... FOR UPDATE` that did not exist anywhere in the code. | **Critical** | Adversarial audit, then reproduced with a concurrency test | `internal/repository/postgres/circulation_repo.go` | **Closed** | `SELECT id FROM users WHERE id = $1 FOR UPDATE` before the count, held to commit, so the second transaction waits and reads a count that includes the first loan. Verified: the same 5-way race now yields exactly 2. |
| DEF-013 | **The member roll search could not use its indexes.** The predicate filtered `full_name OR identifier OR email`; the first two had trigram indexes but `email` (a `citext`) had none, and an `OR` chain is only as indexable as its least-indexed branch. Postgres discarded both usable indexes and scanned the whole roll — 33,628 rows rejected to find 375. | Medium | `EXPLAIN ANALYZE` at 38,000 members | `internal/repository/postgres/user_repo.go`, `migrations/0007_indexes.sql` | **Closed** | Trigram index on `(email::text)` with a matching cast in the query, so all three branches combine in a `BitmapOr`. **22.5 ms → 3.1 ms.** |
| DEF-012 | **An index that could never be used, under a name claiming otherwise.** `authors_name_trgm_idx` was defined as `btree(lower(name))`, but the author filter uses `ILIKE '%...%'` — a B-tree is ordered by prefix and a leading wildcard has no prefix, so the index was never once consulted. It cost write time and disk while giving every reader of the schema the false impression that author search was covered. | Medium | `EXPLAIN ANALYZE` showed `Seq Scan on authors` | `migrations/0002_search.sql`, corrected in `0007_indexes.sql` | **Closed** | Replaced with a real `gin(name gin_trgm_ops)` index. |
| DEF-010 | `POST /members` reported a **privilege violation as a validation failure** — `400 VALIDATION_FAILED` instead of `403 FORBIDDEN`. The handler sniffed the error's Go type to decide which response shape to use. Misleading to the client and misleading in the logs, where an escalation attempt looked like a typo. | Low | Verification of the DEF-005 fix | `internal/transport/http/handler/member_handler.go` | **Closed** | Explicit `errors.Is` check against the known domain errors; only genuine field complaints fall through to a validation response. |
| DEF-009 | **A librarian could edit a borrowed copy back to `available`.** The loan stayed open, so the system showed the book on the shelf while a student still held it — and the copy was simultaneously counted as available and on loan. Any status transition was accepted: `withdrawn` → `on_loan`, `lost` → `available`, and a copy's loan policy could be rewritten underneath an active borrower. | **High** | Audit against the bug-class list (state-machine bugs) | `internal/service/catalogue_service.go`, `catalogue_repo.go`, `internal/domain/domain.go` | **Closed** | An explicit copy state machine (`CanTransitionTo`), enforced in the service. Marking a borrowed copy lost or damaged now closes its loan and writes an audit row in one transaction, so no librarian has to fake a return to record a real loss. |
| DEF-008 | **Pagination could repeat and drop rows.** Catalogue search ordered by rank then title, loans by due date, members by creation time — none of which are unique. Postgres is free to order ties differently between requests, so page 2 could repeat a row from page 1 and silently omit another. | Medium | Audit against the bug-class list (pagination bugs) | all four list queries | **Closed** | Primary key appended as a tie-breaker to every list `ORDER BY`, giving a total order. |
| DEF-007 | **A librarian-issued temporary password was a fully working credential.** `must_change_password` was stored and returned at login but **never enforced**, so a member who ignored the prompt kept full API access on a password handed to them on paper — one that a third party may also have seen. | **High** | Audit against the bug-class list (authentication bugs) | `internal/auth/token.go`, `middleware.go` | **Closed** | The flag rides in the access token as `pending`; middleware confines such a token to the change-password and logout routes. |
| DEF-006 | **Sessions survived a password change.** Refresh tokens issued before a change or reset kept working, so changing a password — the exact thing a member does when they suspect compromise — achieved nothing against an attacker already holding a session. | **High** | Audit against the bug-class list (authentication bugs) | `internal/service/auth_service.go`, `token_repo.go` | **Closed** | `RevokeAllRefreshTokens` on both change and reset. |
| DEF-005 | **Privilege escalation.** `POST /members` accepted a `role` field from the request body and passed it straight through, so **any librarian could create an administrator account** — including for themselves — by posting `{"role":"admin"}`. Classic mass-assignment. | **Critical** | Audit against the bug-class list (RBAC bugs) | `internal/service/member_service.go` | **Closed** | `RolesCreatableBy(actor)`: a librarian may create members only; staff accounts require an administrator. The role is checked against the caller's own role from the signed token, never trusted from the body. |
| DEF-004 | The audit log formatted its timestamp in SQL with `to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SSZ')`. The `Z` was a **literal character, not a conversion** — it labelled a session-local time as UTC. Read from a session in Africa/Lagos, every audit entry was stamped an hour off while claiming to be UTC, and sub-second precision was discarded. An audit trail that is confidently wrong about *when* is worse than no audit trail. | **High** | Time policy review | `internal/repository/postgres/audit_repo.go` | **Closed** | Scan `created_at` as `time.Time` and let `encoding/json` render RFC 3339. No hand-formatted timestamps anywhere. |
| DEF-003 | The loan period was computed against the application's clock while `borrowed_at` defaulted to Postgres `now()`. The two clocks differed by milliseconds, so a 14-day loan was recorded as 13 days 23:59:59 and displayed as **13 days**. | Low | End-to-end run against the real database | `internal/service/circulation_service.go`, `circulation_repo.go` | **Closed** | `borrowed_at` is written explicitly from the same instant used to compute `due_at`. One clock, one pair of timestamps. |
| DEF-002 | Catalogue search and loan listing returned **HTTP 500** for every request. The pagination count `count(*) OVER() AS total` was concatenated onto a query string that already ended in its `FROM` clause, producing `FROM books b, count(*) OVER() ... WHERE` — a syntax error. Every listing endpoint was affected. | **High** | First end-to-end run against a real database. **Not caught by unit tests**, which use fakes and never execute SQL. | `internal/repository/postgres/catalogue_repo.go`, `circulation_repo.go` | **Closed** | Split the constants into `bookFields`/`bookFrom` and `loanFields`/`loanFrom` so the count joins the select list rather than following `FROM`. |
| DEF-001 | `CirculationService.NotifyDueSoon` queued a "due soon" reminder for loans that had already been **returned**. The overdue branch correctly checked `IsReturned`, but the due-soon branch tested only `DueAt - now <= window`, which is true for any past due date — including one on a closed loan. A member who returned a book on time could be emailed a reminder for it. | Medium | Unit test `TestNotifyDueSoonPicksTheRightTemplate`, before any deployment | `internal/service/circulation_service.go` | **Closed** | Explicit `if l.IsReturned() { continue }` guard at the top of the loop, rather than relying on the caller passing `openOnly=true` |

## Note on DEF-014..DEF-019 — the documentation was part of the defect

These six came from an adversarial audit given one instruction above all others:
**distrust the documentation**. The README, the design description and
`SYSTEM_INVARIANTS.md` were written by the same author as the code, so a reviewer
who reads them first inherits the author's blind spots and confirms rather than
attacks.

That instruction is what found DEF-014. The code was wrong, but the *documents
were also wrong, in the same direction*: both `README.md` and `docs/design.md`
described a `SELECT ... FOR UPDATE` lock that had never been written. Three
separate documents asserted a safety property that no line of code enforced. A
reviewer trusting them would have ticked the box.

Two lessons, both recorded rather than tidied away:

1. **A comment claiming a safety property is a hypothesis, not an enforcement.**
   The only question worth asking of one is "which line makes this true?"
2. **"Inside a transaction" is not the same as "serialised".** DEF-014 and
   DEF-015 are the same shape: two operations that each looked atomic, with a
   window between them that a second request could enter. Transactions give
   atomicity; they do not give mutual exclusion unless something is locked.

## Note on DEF-012 and DEF-013 — you cannot read an index's usefulness off the schema

Both defects looked correct in the migration file. One was an index of the wrong
*type* for its query; the other was a missing index that silently disabled two
present ones. Neither is visible by reading the schema, and no test would have
caught them: the queries returned the right rows all along, just slowly.

They were found by asking the planner with `EXPLAIN ANALYZE`, which is the only
thing that can answer "is this index actually used".

A third finding is worth recording even though it was not a defect: at 5,000
books the planner **correctly ignored** the new trigram index, because at that
size a sequential scan is genuinely cheaper. It only began using the index at
around 150,000 rows. Measuring on seed data and declaring victory would have
proved nothing about production.

## Note on DEF-002 — what unit tests could not catch

DEF-002 is the honest counterweight to DEF-001. The service layer is tested
against fakes, which is what makes those tests fast and database-free — but a
fake repository never executes SQL, so a malformed query is invisible to it. The
entire catalogue was broken and every unit test still passed.

It was caught within seconds of the first run against a real Postgres.

The lesson is the one the course teaches about test levels: unit tests verify
logic, and integration tests verify that the parts actually fit together. Neither
substitutes for the other. If asked at the defence "what did your testing miss",
this is the honest and more interesting answer.

Two seed-data faults were found the same way: a duplicate accession number
(`HOL-523153` assigned to both a Clean Code copy and the Africana volume, silently
swallowed by `ON CONFLICT DO NOTHING`) — which is precisely the collision the
`copies.accession_number` unique constraint exists to prevent, demonstrating
DOM-002 by violating it.

## Note on DEF-001

The production call passes `openOnly=true`, so this would probably never have
fired in the deployed system. It was still worth fixing rather than arguing away:
the service was depending on its caller to filter for it, and the next caller
would not have known that.

Article 5 of the constitution — prevention outranks detection. This defect was
caught by a unit test written alongside the code, before the function had ever
run against a database. That is the cheapest place a defect can be found, and it
is worth saying so at the defence when asked what testing actually caught.
