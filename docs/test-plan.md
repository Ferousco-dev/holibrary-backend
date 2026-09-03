# Software Test Plan
## HOLibrary Backend: Group 4 Online Library Management System

**Course:** SEN 106 / SEN 216, Introduction to Web Technologies
**Structure:** IEEE 829
**Phase:** 05 Verification · **Gate:** G5
**Traces to:** `docs/srs.md` (REQ-001..074, NFR-001..020, DOM-001..009)

---

## 1. Test plan identifier

`HOL-STP-001`

## 2. Introduction

This plan describes how the HOLibrary backend is verified at four levels, unit
integration, system and acceptance, and what evidence each level produces.

The system records which named student is holding which physical book. Two kinds
of failure matter more than the rest, and the plan is shaped around them:

1. **Silent data corruption.** A double loan, an abandoned loan record, a
   negative copy count. These do not raise errors; the library simply becomes
   wrong about the physical world and nobody notices until a student is accused
   of losing a book they returned.
2. **Broken access control.** A borrowing history is a record of what a named
   person reads. A member reading another member's history is a privacy failure,
   not a bug report.

Ordinary functional defects are cheaper than either, and the plan weights effort
accordingly.

## 3. Test items

| Item | Version |
|---|---|
| `holibrary-backend` HTTP API | `main` |
| PostgreSQL schema | migrations `0001` to `0007` |
| Container image | multi-stage build, ~15.5 MB |
| OpenAPI contract | `docs/openapi.yaml`, 25 paths, 29 operations |

## 4. Features to be tested

Prioritised by consequence of failure, not by ease of testing.

| Priority | Feature | Requirements |
|---|---|---|
| **Critical** | Concurrent borrowing of a single copy | REQ-047, NFR-009, I-01 |
| **Critical** | Role-based access control | NFR-004, REQ-062, I-11 |
| **Critical** | Authentication and session lifecycle | REQ-001..008, NFR-002, NFR-003 |
| **High** | Loan and return lifecycle | REQ-041..051 |
| **High** | Copy state transitions | REQ-024..026, I-04 |
| **High** | Availability derivation and last-copy retention | REQ-038..040, DEC-018 |
| **High** | Overdue detection and time handling | REQ-052, REQ-053, DES-010 |
| **Medium** | Catalogue search across the three access points | REQ-028..035, DOM-007 |
| **Medium** | Member registration and CSV import | REQ-009..015 |
| **Medium** | Reservations and the queue | REQ-055..059 |
| **Medium** | Notification delivery and state re-check | REQ-069..072 |
| **Low** | Reporting and audit | REQ-065..068 |

## 5. Features not to be tested

| Not tested | Why |
|---|---|
| Resend and FCM internals | Third-party services. Our boundary is tested with fakes; theirs is their responsibility. |
| The frontend | A separate repository, built later against the OpenAPI contract. |
| PostgreSQL's own correctness | We rely on its constraints; we do not re-verify them. |
| Load beyond 50 concurrent users | NFR-001 is stated at that figure. Beyond it we would be measuring an unstated requirement. |

## 6. Approach: the four levels

### 6.1 Unit testing

**Scope.** `internal/domain`, `internal/auth`, `internal/service`, `internal/queue`.
**Technique.** Go's testing package, table-driven, against fakes.
**Isolation.** No database, no network, no HTTP.

The layering is what makes this possible: `internal/domain` imports nothing from
the project, and services depend on repository *interfaces*. Every library rule
can therefore be exercised in milliseconds.

**Principle: test the refusal, not the happy path.** A test proving a librarian
can lend an available book proves almost nothing. The tests that matter prove the
system *refuses*, an inactive member, a reference volume, a limit already
reached, an illegal state transition.

| Package | Tests | Coverage |
|---|---|---|
| `domain` | 22 | 94.7% |
| `service` | 44 | 78.7% |
| `auth` | 9 | 83.8% |
| `queue` | 8 | |
| **Total** | **85** | gate: ≥ 70% |

The threshold is enforced in CI, so it cannot regress unnoticed.

### 6.2 Integration testing

**Scope.** Repository layer against a real PostgreSQL; the router against the spec.
**Technique.** `docker compose` stack; `EXPLAIN ANALYZE` for query plans.

This level exists because of a defect. **DEF-002 broke every listing endpoint
with a SQL syntax error, and every unit test still passed**, fakes never execute
SQL. Unit tests verify logic; integration tests verify that the parts fit
together. Neither substitutes for the other, and this project has the scar to
prove it.

Also at this level:

- **Contract tests.** Every route must appear in `openapi.yaml`, and every
  documented path must have a route. Both fail the build. They have already
  caught two undocumented endpoints before commit.
- **Index validation.** `EXPLAIN ANALYZE` against 155,000 books and 38,000
  members. Two defects (DEF-012, DEF-013) were found this way, neither visible
  in the schema, neither catchable by a functional test, because both queries
  returned correct rows all along, just slowly.

### 6.3 System testing

**Scope.** The whole containerised stack over HTTP, as a client sees it.
**Technique.** Scripted end-to-end runs against `docker compose`.

This is where concurrency is proven rather than asserted: **20 simultaneous
requests to borrow one copy**, checked against the database afterwards.

### 6.4 Acceptance testing

**Scope.** The scenarios from the project brief, in the language of the library.
**Technique.** Walkthrough against the running system, witnessed at the defence.

Acceptance asks a different question from system testing: not "does the endpoint
work" but **"does this system model Hezekiah Oluwasanmi Library"**. A system can
pass every functional test and still be wrong about the library, by lending a
dictionary, or by treating five copies of *Clean Code* as five unrelated books.

---

## 7. Test cases

Identifiers are stable and traceable. `.ilana/traceability.csv` is the register.

### 7.1 Unit level

| ID | Test | Requirement | Expected |
|---|---|---|---|
| TC-001 | Wing derived from LCC class A to J | DOM-003 | `South` |
| TC-002 | Wing derived from LCC class K to Z | DOM-003 | `North` |
| TC-003 | A non-letter class mark | DOM-003 | `Unknown` |
| TC-004 | `circulating` copy is borrowable | DOM-004 | true |
| TC-005 | `reference_only` is not borrowable or reservable | DOM-004 | false, false |
| TC-006 | `on_display` is reservable but not borrowable | DOM-004 | false, true |
| TC-007 | Loan period per category: 14 / 21 / 28 days | DEC-005 | matches category |
| TC-008 | Loan limit per category: 2 / 4 / 6 | DEC-005 | matches category |
| TC-009 | Overdue is false before the due instant | REQ-053 | false |
| TC-010 | Overdue is true strictly after it | REQ-053 | true |
| TC-011 | A returned loan is never overdue | REQ-053 | false |
| TC-012 | Overdue is independent of the reader's timezone | DES-010 | identical in UTC, Lagos, host |
| TC-013 | 14-day period is identical computed in UTC or Lagos | DES-010 | same instant |
| TC-014 | Timestamps serialise as RFC 3339 | DES-010 | `...Z` |
| TC-015 | Retention: 2+ copies keeps one on the shelf | DEC-018 | borrowable = available − 1 |
| TC-016 | Retention: a single copy still circulates | DEC-018 | borrowable = 1 |
| TC-017 | A lost copy leaves stock, relaxing retention | DEC-018 | circulates again |
| TC-018 | `on_loan` → `available` is refused | I-04 | `ErrInvalidTransition` |
| TC-019 | `on_loan` → `lost` closes the loan | I-04 | loan closed |
| TC-020 | `withdrawn` is terminal | I-04 | refused |
| TC-021 | A member cannot manage the library | NFR-004 | false |
| TC-022 | Argon2id hash is salted per password | NFR-002 | two hashes differ |
| TC-023 | A malformed hash is reported, not accepted | NFR-002 | error |
| TC-024 | Password policy: length, letter, digit | NFR-002 | rejected |
| TC-025 | JWT round-trip preserves id and role | NFR-003 | matches |
| TC-026 | A token signed with another secret is rejected | NFR-003 | error |
| TC-027 | An expired token is rejected | NFR-003 | error |
| TC-028 | Opaque tokens are random and stored hashed | NFR-003 | differ |
| TC-029 | `pending` flag rides in the access token | REQ-007 | true |
| TC-030 | Borrow computes the due date from category | REQ-042 | correct period |
| TC-031 | Borrow refuses a suspended member | REQ-045 | `ErrMemberNotActive` |
| TC-032 | Borrow refuses a member with no category | REQ-043 | `ErrNoCategory` |
| TC-033 | Borrow propagates copy-unavailable | REQ-046 | `ErrCopyNotAvailable` |
| TC-034 | Borrow propagates non-circulating | REQ-044 | `ErrCopyNotBorrowable` |
| TC-035 | Due-soon and overdue pick the right template | REQ-069, REQ-070 | one each |
| TC-036 | No reminder for a returned loan | DEF-001 | not queued |
| TC-037 | Login rejects a wrong password | REQ-001 | `ErrInvalidCredentials` |
| TC-038 | Unknown account and wrong password are indistinguishable | DOM-009 | identical error |
| TC-039 | Login rejects suspended and inactive members | REQ-045 | `ErrMemberNotActive` |
| TC-040 | Refresh rotates the token | NFR-003 | new token issued |
| TC-041 | Refresh rejects a spent token | NFR-003 | `ErrTokenInvalid` |
| TC-042 | Refresh rejects a suspended member | REQ-045 | `ErrMemberNotActive` |
| TC-043 | Change password requires the current one | REQ-003 | `ErrInvalidCredentials` |
| TC-044 | Change password revokes every session | I-14 | all revoked |
| TC-045 | Reset password revokes every session | I-14 | all revoked |
| TC-046 | Reset is silent about unknown addresses | DOM-009 | no error, nothing queued |
| TC-047 | A librarian cannot create staff accounts | I-11 | `ErrForbidden` |
| TC-048 | Temporary passwords are unpredictable | REQ-007 | not derived from matric |
| TC-049 | CSV dry run writes nothing | REQ-010 | 0 created |
| TC-050 | CSV reports valid / duplicate / invalid per line | REQ-011 | counts match |
| TC-051 | One bad row does not abort the batch | REQ-011 | good rows created |
| TC-052 | An unusable CSV header is rejected whole | REQ-011 | error names the column |
| TC-053 | LCC call number validated; Dewey rejected | DOM-001 | `ErrInvalidCallNumber` |
| TC-054 | ISBN punctuation normalised | REQ-016 | hyphens stripped |
| TC-055 | Page size is clamped | NFR-001 | ≤ 100 |
| TC-056 | Reserve refused when a copy is borrowable | REQ-055 | `ErrCopiesAvailable` |
| TC-057 | Reserve refused for non-reservable titles | DOM-004 | `ErrNotReservable` |
| TC-058 | Reserve refused for a duplicate | REQ-056 | `ErrAlreadyReserved` |
| TC-059 | A return promotes and notifies the next in queue | REQ-058 | push + email |
| TC-060 | An empty queue is not an error | REQ-058 | no error |
| TC-061 | Worker delivers and marks sent | REQ-072 | marked sent |
| TC-062 | **Worker does not send a superseded message** | REQ-072 | superseded |
| TC-063 | Transient failure is retried | REQ-072 | attempt recorded |
| TC-064 | Permanent failure is closed, not retried | REQ-072 | closed |
| TC-065 | Push fans out to every device | REQ-071 | all devices |
| TC-066 | A dead device is retired, a live one still delivered | REQ-071 | revoked + sent |
| TC-067 | An unconfigured channel leaves messages queued | REQ-072 | untouched |

### 7.2 Integration level

| ID | Test | Requirement | Expected |
|---|---|---|---|
| TC-068 | Every route appears in the OpenAPI spec | REQ-073 | 29/29 |
| TC-069 | Every documented path has a route | REQ-073 | no phantoms |
| TC-070 | Migrations apply to an empty database | | schema created |
| TC-071 | Duplicate accession number rejected by constraint | I-06 | unique violation |
| TC-072 | Title search uses the trigram index at scale | NFR-001 | `Bitmap Index Scan` |
| TC-073 | Member search combines three indexes | NFR-001 | `BitmapOr` |
| TC-074 | Overdue query uses the partial index | NFR-001 | `Index Only Scan` |
| TC-075 | Loan history survives an attempt to delete a member | I-07 | FK restricts |

### 7.3 System level

| ID | Test | Requirement | Expected |
|---|---|---|---|
| TC-076 | **20 simultaneous borrows of one copy** | REQ-047, I-01 | 1×201, 19×409, exactly 1 loan |
| TC-077 | Health endpoint reports database reachability | REQ-074 | 200, `reachable` |
| TC-078 | Catalogue search is public | REQ-037 | 200 without a token |
| TC-079 | Member data requires a token | NFR-004 | 401 |
| TC-080 | A member is refused every staff route | NFR-004 | 403 ×5 |
| TC-081 | A temporary password reaches only the change route | REQ-007 | `MUST_CHANGE_PASSWORD` |
| TC-082 | A pre-change refresh token stops working | I-14 | `TOKEN_INVALID` |
| TC-083 | A librarian cannot mint an admin | I-11 | 403 |
| TC-084 | Unknown JSON fields are rejected | NFR-007 | 400 |
| TC-085 | Borrowing a reference volume is refused | DOM-004 | `COPY_NOT_BORROWABLE` |
| TC-086 | The category limit is enforced end to end | REQ-043 | `LOAN_LIMIT_REACHED` |
| TC-087 | Retention refuses the last shelf copy | DEC-018 | `LAST_COPY_RETAINED` |
| TC-088 | A single-copy title still circulates | DEC-018 | 201 |
| TC-089 | Returning twice is refused | REQ-051 | `LOAN_ALREADY_CLOSED` |
| TC-090 | History survives return | DOM-008 | still listed |
| TC-091 | A member cannot cancel another's reservation | I-11 | 404, row survives |
| TC-092 | A shared terminal's device token moves owner | REQ-071 | reassigned |
| TC-093 | The worker supersedes a reminder for a returned book | REQ-072 | not sent |
| TC-094 | Auth endpoints are rate limited | NFR-005 | 429 |
| TC-095 | Timestamps are RFC 3339 UTC, period exactly 14 days | DES-010 | `...Z`, 14d |

### 7.4 Acceptance level

Run as a walkthrough at the defence. Each is phrased as the library would.

| ID | Scenario | Basis |
|---|---|---|
| TC-096 | A student searches "Clean Code" by title, author and subject, sees how many copies are free and which wing shelves them | DOM-007, DOM-003 |
| TC-097 | Five copies of one title appear as **one book with five accession numbers**, not five books | DOM-002 |
| TC-098 | A student registers at the desk; the librarian creates the account and hands over a temporary password; the student must change it at first sign-in | DOM-006 |
| TC-099 | A librarian imports a departmental CSV, previews it, fixes the rows named, and commits | REQ-010 |
| TC-100 | A librarian records a loan; the student sees it with its due date; the copy count drops | REQ-041, REQ-060 |
| TC-101 | The student returns it; the copy becomes available; the loan stays in their history | REQ-048, DOM-008 |
| TC-102 | A student cannot borrow a dictionary from the Reference Room | DOM-004 |
| TC-103 | A student joins the queue for a fully-borrowed title and is notified when a copy returns | REQ-055, REQ-058 |
| TC-104 | A librarian lists overdue loans and sees who holds what | REQ-052, REQ-054 |
| TC-105 | A book lost while on loan is recorded as lost, **without faking a return** | I-04 |

---

## 8. Pass and fail criteria

**A test case passes** when its expected result is observed and no other
assertion in the run regresses.

**The suite passes**, and G5 may be attempted, when all of:

| Criterion | Threshold | Status |
|---|---|---|
| Unit and integration suite | 100% pass | ✅ 85 tests |
| Coverage on business logic | ≥ 70% per package | ✅ 94.7 / 78.7 / 83.8 |
| `go vet` | clean | ✅ |
| `gofmt` | clean | ✅ |
| Race detector | no data races | ✅ `-race` in CI |
| Contract tests | route ↔ spec agree | ✅ 29/29 |
| Open defects at severity High or above | 0 | ✅ 13 found, 13 closed |
| System-level concurrency (TC-076) | exactly 1 loan | ✅ |

**Suspension.** Testing stops if the schema fails to migrate, or if a defect of
severity Critical is found, that defect is fixed before the run continues.

## 9. Test deliverables

| Deliverable | Location |
|---|---|
| This plan | `docs/test-plan.md` |
| Test code | `internal/**/*_test.go` |
| Coverage report | `go tool cover`, CI-enforced |
| Defect log | `.ilana/defects.md` |
| Traceability matrix | `.ilana/traceability.csv` |
| Gate records | `.ilana/gates/G*.md` |
| CI results | GitHub Actions, per push |

## 10. Environment

| | |
|---|---|
| Unit | Go 1.27, no external services |
| Integration & system | `docker compose`: PostgreSQL 17, Redis 7, the API image |
| Production-like | Render container, Neon Postgres, Upstash Redis, Cloudflare edge |

## 11. Responsibilities

| Role | Responsibility |
|---|---|
| Developer | Unit tests written with the code, not after |
| Verifier | Integration and system runs; defect log |
| Group | Acceptance walkthrough at the defence |

## 12. Risks

| Risk | Mitigation |
|---|---|
| RSK-001 Render cold start stalls the live demo | Warm the URL before presenting |
| RSK-002 Resend needs a verified domain; TC-046 cannot be shown end to end in production | The console sender demonstrates the pipeline; the limitation is stated rather than hidden |
| Concurrency defects hide in single-user testing | TC-076 runs 20 simultaneous requests; CI runs `-race` |
| Coverage regressing quietly | CI fails below 70% |

## 13. Defect summary

Thirteen defects found, thirteen closed. **What found them is the interesting
part**, and the honest answer to "what did your testing miss":

| Found by | Defects | Note |
|---|---|---|
| Unit test | DEF-001 | Before the code ever met a database |
| First run against real Postgres | DEF-002, DEF-003 | **Every unit test passed while every listing endpoint returned 500**: fakes never execute SQL |
| Review against a checklist | DEF-004..DEF-010 | Including the three most severe. Prevention outranks detection. |
| `EXPLAIN ANALYZE` | DEF-012, DEF-013 | Invisible in the schema, uncatchable by a functional test: the queries were correct all along, just slow |

No single technique found more than half of them. That is the argument for
having four levels rather than one.
