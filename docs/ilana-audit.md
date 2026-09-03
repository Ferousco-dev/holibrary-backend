# Ilana Process and Security Audit: HOLibrary Backend

Date: 2026-09-03  
Auditor: quality-auditor  
Rigour assessed against: 4

## Verdict

The earlier borrower-lock, reservation-expiry, login-timing, and device-token
ownership fixes remain. The new simulator is not safe around real library data:
it neither marks nor isolates its records, and can return real loans. The new
session stamp does not distinguish a pre-change refresh token from one presented
after a password change. Redis failure also disables, rather than degrades, both
authentication limits. Do not deploy the simulator with real member or
circulation data until DEF-020 and DEF-021 are closed.

## Scorecard

| Phase | Score | Evidence | Gap |
|---|---:|---|---|
| 01 Requirements | 3 | `docs/srs.md`, `.ilana/traceability.csv` | Synthetic isolation has no trace to code/tests. |
| 02 Architecture | 3 | `docs/design.md`, package layers | External-client and session-revocation protocols are incomplete. |
| 03 Interface | 2 | OpenAPI and route-contract test | No operational simulator/isolation contract. |
| 04 Construction | 3 | Go formatting, migrations, package boundaries | Comments claim controls not enforced end-to-end. |
| 05 Verification | 2 | `go test ./...` passes; CI race/vet/coverage | New critical paths lack tests. |
| 06 SCM | 2 | 14 commits, Docker and CI | No release tags or change record for simulator risk. |
| 07 Quality assurance | 2 | Test plan and defect log | No security review gate caught regressions. |
| 08 Process assessment | 2 | `.ilana/ledger.md` | No current defect or outage metrics. |
| 09 Process modeling | 2 | Design docs and README | Synthetic/real segregation is undocumented. |
| 10 Tooling | 3 | GitHub Actions, Makefile, Docker Compose | No `govulncheck`, secret scanning, or security checks. |
| 11 Ethics and teams | 2 | PII-aware logging policy | No `SECURITY.md`, ownership, or disclosure process. |

## Indicative Maturity

Mean score: **2.36 / 4**, an indicative **Level 3 Defined (partial)**. This is
not a formal CMMI appraisal.

## Findings

### DEF-026 [High] Simulator bearer token is sent to any configured API base URL

The simulator correctly takes its password from `SIMULATOR_PASSWORD` rather than
from a command-line argument (`cmd/simulator/main.go:57-62`), and no normal
print/log path exposes the password or access token. However, its API base URL
is accepted directly from `-url` or `SIMULATOR_URL` (`main.go:47,73-76`) without
HTTPS or host validation. Every authenticated simulator call attaches the
staff bearer token (`internal/simulator/agent.go:108-115`).

If a deployment wrapper, scheduled-job environment, or operator configuration
points that URL to an attacker-controlled HTTP(S) server, the server receives a
librarian token and can use it until expiry. This is not a remote leak under a
trusted configuration; it is a credential-exfiltration path when the endpoint
configuration is changed independently of the password secret.

Fix: require an HTTPS allowlist for non-local simulator targets, reject URL
userinfo and redirects to a different origin, and use a least-privilege
simulator account rather than a human librarian account.

### DEF-020 [Critical] The simulator is not isolated from real library data

`migrations/0009_synthetic.sql:13-14` adds `users.is_synthetic` and
`books.is_synthetic`, but no application source reads or writes either column.
The standard inserts omit the flag at
`internal/repository/postgres/catalogue_repo.go:196-205` and
`internal/repository/postgres/user_repo.go:93-109`; simulator creation calls
those paths at `internal/simulator/actions.go:37-48` and `116-125`. Every
row gets the database default `false`.

This is data corruption, not only report contamination. `Circulate` selects
the complete catalogue at `actions.go:153-176` and lends any available copy at
`184-213`. `returnSomething` selects all open loans and closes a random one
at `216-231`. A scheduled pass can loan a real copy to a generated member or
return a real member's loan. The claimed one-statement cleanup is impossible.

Fix: give the simulator a dedicated scoped principal; make synthetic origin
server-controlled; persist it on every relevant record; and filter every
simulator selection/write to that scope. Add real-Postgres tests proving it
cannot select or mutate a real book, copy, loan, or reservation.

### DEF-021 [Critical] `tokens_invalid_before` does not invalidate old refresh tokens

Password changes write a database timestamp at
`internal/repository/postgres/user_repo.go:178-185`. Refresh then accepts any
consumed token whenever the application's *current* time is after that value at
`internal/service/auth_service.go:177-201`. `ConsumeRefreshToken` returns
only a user ID, not token creation time/version (`token_repo.go:33-42`).

Race: an attacker begins a refresh with a stolen token; the victim changes the
password and revokes rows; the attacker continues through the stamp check. Once
the API clock is after the new stamp, it mints a fresh session. The code compares
refresh execution time, not whether the presented token was issued before the
password change. API/Postgres clock skew also makes a just-written database stamp
temporarily appear to be in the API's future. The test at
`auth_service_test.go:378-401` tests an artificial future stamp and immediate
refresh, not this interleaving.

Fix: atomically consume the refresh token while comparing its persisted
`created_at`, or a user session version, with the invalidation version in the
same database transaction. Do not compare fresh wall-clock time to the stamp.

### DEF-022 [High] Access tokens survive password changes for their full TTL

`internal/transport/http/middleware/middleware.go:73-107` only validates JWT
signature/expiry; it never checks `tokens_invalid_before`. JWTs contain no
session version (`internal/auth/token.go:45-65`). A stolen access token remains
usable on every permitted route for up to 15 minutes after reset. This contradicts
the README and `auth_service.go:232-235` claim of immediate invalidation.

Fix: either explicitly accept and document that bounded window, or include a
session version and validate it through a short-lived cache/database lookup on
protected requests.

### DEF-023 [High] A live Redis failure disables both authentication limits

The in-memory fallback is used only when startup ping fails
(`cmd/api/main.go:124-145`). After Redis is selected, command errors from
`internal/ratelimit/ratelimit.go:54-65` are allowed through by both middleware
(`middleware.go:160-167`) and the account limiter (`auth_service.go:77-87`).
There is no runtime fallback/circuit breaker.

During a timeout, outage, connection exhaustion, or managed-service quota
rejection, attackers can make unlimited login guesses and reset requests. A
distributed bot can create arbitrary login keys/Redis commands, drain managed
quota, and then benefit from fail-open behavior. Every failed request also logs
an error.

Fix: switch to bounded local fallback on runtime errors, use a health-checked
circuit breaker, cap limiter-key length/cardinality, retain an edge limit that
does not use Redis, and test failure/recovery.

### DEF-024 [High] Configured catalogue client permits SSRF and decoded-object exhaustion

`OPENLIBRARY_BASE_URL` has no scheme, hostname, IP, or redirect validation in
`internal/config/config.go:55-64`; it is passed directly to the HTTP client at
`cmd/api/main.go:171` and requested at
`internal/books/openlibrary.go:55-64,93-103`. Go follows redirects by default.
A hostile or misconfigured base can target loopback, RFC1918, cloud metadata, or
other internal services.

The 4 MiB `LimitReader` at `openlibrary.go:111-115` bounds input bytes, which
is good, but does not cap `docs` count. A 4 MiB JSON array of many small objects
can allocate a much larger decoded slice before the loop discards empty titles.

Fix: make the base an HTTPS allowlist, disable/revalidate redirects, reject
private resolved addresses at dial time, stream at most the requested result
count, and reject an oversized body rather than accepting a prefix.

### DEF-025 [Medium] `/books/lookup` is an unbounded third-party request relay

The librarian-only lookup route has no dedicated throttle at
`internal/transport/http/router.go:102-109`; each call sends a remote request
at `internal/books/openlibrary.go:93-103`. There is no cache, query/ISBN length
bound, per-user limit, or global outbound budget. A compromised librarian token
can generate arbitrary request volume against Open Library or the SSRF target.
This is one inbound request to one outbound request, not a high-ratio amplifier,
but it can exhaust third-party quota and disrupt cataloguing.

Fix: add per-user/global lookup limits, bounded/validated inputs, a small
positive-and-negative cache, and external-client metrics.

## False Assurances

- Migration `0009` says simulator data is identifiable/removable; no runtime
  path persists or filters its flags.
- Migration `0008` says timestamp comparison removes the refresh race; it
  compares the wrong instant and lacks token issuance metadata.
- The README says sessions are void instantly; access JWTs are never checked
  against invalidation state.
- The in-memory Redis fallback applies only at startup; runtime errors fail open.
- A 4 MiB body cap does not bound decoded object cardinality.

## Reverification of Earlier Defects

| Earlier defect | Result | Evidence |
|---|---|---|
| DEF-014 borrower loan-limit race | Still fixed | `circulation_repo.go:117-127` locks user row before count. |
| DEF-015 refresh race | Regressed | DEF-021 compares current time, not token issuance/version. |
| DEF-016 pending reservation expiry | Still fixed | `reservation_repo.go:70-78,182-193` keeps pending expiry null and expires ready only. |
| DEF-017 login timing enumeration | Still fixed in source | `auth_service.go:118-129` burns Argon2 work; timing test remains. |
| DEF-018 device-token ownership | Still fixed | `outbox_repo.go:182-195` scopes revocation by `user_id`. |
| DEF-019 limiter persistence/proxy handling | Partially regressed | Redis/proxy improvements remain, but DEF-023 removes limits on runtime failure. |

## What Is Already Good

- Unknown-account login still performs comparable Argon2 work.
- Borrower-limit protection uses actual Postgres row locking.
- Pending reservations no longer age out before a copy is ready.
- Device-token revocation has ownership in the SQL predicate.
- The external client has a timeout, requests a field subset, and has a finite
  input-byte cap. The problem is cardinality and trust-boundary enforcement.
- `go test ./...` passed on 2026-09-03, but does not cover the failure paths above.

## Remediation Plan

### This Week

1. Stop simulator runs against real data; implement hard synthetic isolation.
2. Replace timestamp refresh invalidation with an atomic persisted version check.
3. Add runtime Redis fallback/circuit-breaking and an independent edge limit.

### This Month

1. Restrict external client, bound decoded records, and rate-limit/cache lookup.
2. Add real-Postgres interleaving, Redis-failure, lookup, and synthetic-isolation tests.
3. Add `govulncheck`, secret scanning, and a security review gate to CI.

### This Quarter

1. Add release tagging, rollback instructions, ownership, and `SECURITY.md`.
2. Track defect recurrence and dependency outages, then re-audit against data.
