# System Invariants

Rules this system is **never** allowed to violate.

An invariant is not a feature and not a preference. It is a statement that must
be true of the data at every instant, whatever the code does, whatever order
requests arrive in, and whatever a client sends. If one of these is ever false,
the library's records are wrong about the physical world — and wrong records are
worse than a broken page, because nobody notices.

Each invariant below names **where it is enforced**. The rule is that no
invariant may rest on Go validation alone: application code can be bypassed by
the next endpoint someone writes, and it cannot be trusted under concurrency.
Where an invariant can be expressed as a database constraint, it is.

---

## I-01 · One physical copy cannot have two active loans

The load-bearing invariant of the whole system. Violating it means the library
believes one physical book is simultaneously with two students.

| Layer | Mechanism |
|---|---|
| **Database** | `CREATE UNIQUE INDEX one_active_loan_per_copy ON loans (copy_id) WHERE returned_at IS NULL` |
| **Database** | Atomic claim: `UPDATE copies SET status='on_loan' WHERE id=$1 AND status='available' AND loan_policy='circulating' RETURNING id` — zero rows means the caller lost the race |
| **Service** | Both statements plus the limit check in one transaction |
| **Test** | 20 concurrent borrows of one copy → 1 × 201, 19 × 409, exactly 1 loan row |

There is **no window** between checking availability and taking it, because
there is no separate check. And if the Go code were removed entirely, Postgres
would still refuse the second loan.

## I-02 · A returned loan cannot remain active

`returned_at IS NULL` **is** the definition of active. There is no `status`
column on `loans` to contradict it, so the two can never disagree.

| Layer | Mechanism |
|---|---|
| **Schema** | Status is derived from `returned_at`, never stored |
| **Database** | `CHECK (returned_at IS NULL OR returned_at >= borrowed_at)` |
| **Database** | Return is `UPDATE ... WHERE id=$1 AND returned_at IS NULL` — a second return matches no rows |

This is why `status = "returned", returned_at = NULL` is not a bug we guard
against: it is a state the schema cannot represent.

## I-03 · An unavailable copy cannot be borrowed

Reference works, display items and restricted collections stay in the building.
Lost, damaged and withdrawn volumes cannot be lent.

| Layer | Mechanism |
|---|---|
| **Database** | The claim's `WHERE` requires `status='available' AND loan_policy='circulating'` |
| **Domain** | `LoanPolicy.IsBorrowable()` — only `circulating` is true |
| **Test** | Borrowing a dictionary returns `COPY_NOT_BORROWABLE` |

## I-04 · A copy's status follows a legal path

A physical volume cannot teleport between states. In particular a **borrowed
copy can never be edited back to available**, which would abandon its open loan
and put a book on the shelf that a student is still holding.

```
   available ──▶ lost / damaged / withdrawn
   on_loan   ──▶ lost / damaged          (closes the open loan, honestly)
   lost      ──▶ available / withdrawn   (found)
   damaged   ──▶ available / withdrawn   (repaired)
   withdrawn ──▶ (terminal)
```

Lending is **not** a status edit — it goes through circulation so the loan and
the copy change together. Marking a borrowed copy lost or damaged closes its
loan in the same transaction, so no librarian ever has to record a fake return
in order to write down a real loss.

| Layer | Mechanism |
|---|---|
| **Domain** | `CopyStatus.CanTransitionTo` |
| **Service** | Checked before every copy update |
| **Repository** | `SetCopyStatusClosingLoan` does both halves in one transaction |

## I-05 · A member's identity is unique

| Layer | Mechanism |
|---|---|
| **Database** | `users.identifier UNIQUE`, `users.email UNIQUE` (`citext`, so case cannot create a twin) |
| **Import** | CSV detects in-file repeats by line number *and* database conflicts |

One matriculation number, one account. One email, one account.

## I-06 · An accession number identifies exactly one physical volume

| Layer | Mechanism |
|---|---|
| **Database** | `copies.accession_number UNIQUE` across the whole collection, not per title |

Proven by accident during development: seed data assigned `HOL-523153` to two
volumes and the constraint refused it.

## I-07 · Loan history is never lost

A returned book is a **closed record, not a deleted one**. The library must
still be able to say who held a copy last session.

| Layer | Mechanism |
|---|---|
| **Database** | `loans.user_id` and `loans.copy_id` are `ON DELETE RESTRICT` — a member or copy with history cannot be deleted out from under it |
| **Service** | Books are **archived**, never deleted |
| **Schema** | Return sets `returned_at`; no code path deletes a loan |

The cascade audit matters here: a careless `ON DELETE CASCADE` from `users` to
`loans` would erase the borrowing history of anyone removed from the roll.

## I-08 · Availability and overdue are derived, never stored

There is no `available_copies` column and no `is_overdue` column. A stored
counter is a second source of truth and drifts; a stored flag is correct only
until the clock moves.

| Layer | Mechanism |
|---|---|
| **Schema** | Neither column exists, so neither can be wrong |
| **Query** | `count(*) FILTER (WHERE status='available' AND loan_policy='circulating')` |
| **Domain** | `IsOverdueAt(now)` = `returned_at IS NULL AND now > due_at` |

`available_copies = -1` is not defended against — it is **unrepresentable**.

## I-09 · Redis may never override PostgreSQL

PostgreSQL is the source of truth. Redis is disposable: rate-limit counters and
job coordination only. **No availability, catalogue or authorisation data is
cached.** If Redis vanished entirely, every library record would survive intact
and the only loss would be rate-limit state.

This is why the classic stale-cache bug (`Postgres = 2, Redis = 3`) cannot occur
here: the cache that would go stale does not exist.

## I-10 · External book metadata is never library inventory

```
External API says a book exists   ≠   OAU owns a copy of it
```

The external API supplies **bibliographic metadata only** — title, author, ISBN,
publisher. Whether HOL holds it, how many copies, which shelf, who has one, and
whether it is available is answered **exclusively** from `copies` in our own
database.

| Layer | Mechanism |
|---|---|
| **Design** | Lookup pre-fills a form; the librarian saves the record and adds copies with accession numbers |
| **Schema** | Availability counts rows in `copies`. An API response cannot create one |

The catalogue therefore keeps working when the external API is down, rate
limited, or returns nonsense.

## I-11 · Client input never determines authorisation

| Layer | Mechanism |
|---|---|
| **Middleware** | Role comes from the signed token, never from a body or query field |
| **Service** | A librarian may create **members only**; staff accounts require an administrator |
| **Handlers** | Dedicated request structs with `DisallowUnknownFields` — there is no field to smuggle `"role":"admin"` through |
| **Routes** | A member reads their own record via `/me`, never `/members/{id}` — there is no id to tamper with, so IDOR has no surface |

## I-12 · Client clocks never determine event time

`borrowed_at`, `returned_at`, audit timestamps and token expiry are generated by
the **server**, in UTC. A client-supplied timestamp is never trusted for any of
them.

See `design.md` §4A (DES-010) for the full time policy.

## I-13 · Every privileged mutation is attributable

| Layer | Mechanism |
|---|---|
| **Schema** | `loans.issued_by` and `loans.returned_to` reference the staff account |
| **Schema** | `audit_log` records actor, action, entity and UTC timestamp |
| **Design** | Audit failure is logged but does not roll back the member's operation |

## I-14 · A password change ends every other session

Changing or resetting a password is how someone reacts to a suspected
compromise. If old refresh tokens kept working, the change would achieve nothing
against an attacker who already held one.

| Layer | Mechanism |
|---|---|
| **Service** | `RevokeAllRefreshTokens` on both change and reset |
| **Schema** | Refresh tokens stored **hashed** and rotated on every use |

## I-15 · A temporary password is not a working credential

An account still holding a librarian-issued temporary password may do exactly
one thing: replace it. The flag rides in the access token and the middleware
refuses every other route.

## I-16 · Pagination is stable

Every list query orders on a **total order** — the sort column plus the primary
key. Without the tie-break, rows sharing a title or a due date order
arbitrarily, so page 2 can repeat a row from page 1 and silently drop another.

## I-17 · Infrastructure errors never reach the client

A driver message like `pq: duplicate key value violates unique constraint
users_email_key` tells an attacker the schema. Every error is translated to a
domain error with a stable code and a human message; the original is logged.

## I-18 · Personal data stays out of the logs

Access logs record method, path, status, duration and request id. **Never**
bodies, query strings, tokens or member details. A borrowing history is a record
of what a named student reads.

---

## How these are kept true

1. **Database first.** If an invariant can be a constraint, it is a constraint.
   Go validation is a courtesy to the user; the constraint is the guarantee.
2. **Tested by violation.** Each invariant has a test that *attempts* the
   forbidden thing and asserts it fails. A test proving the happy path works
   proves nothing about an invariant.
3. **Concurrency assumed.** Every invariant is written as though two requests
   arrive at the same instant, because during a defence demo they will.
4. **Recorded when broken.** Five defects have violated one of these and been
   fixed: DEF-002 and DEF-004 through DEF-009 in `.ilana/defects.md`.
