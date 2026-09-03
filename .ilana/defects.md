# Defect Log

| ID | Description | Severity | Found by | Found in | State | Fix |
|---|---|---|---|---|---|---|
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
