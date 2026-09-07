# Physical catalogue discovery — CR-001

This change implements the requested physical-catalogue discovery API under the
existing `/api/v1` prefix. It adds no document upload, digital file storage,
submission, research-item download, or notification delivery.

## Requirements and verification

| Requirement | Implementation | Verification |
| --- | --- | --- |
| REQ-075: validated, parameterised catalogue filtering and pagination | `service/catalogue_query.go`, `postgres/catalogue_repo.go`, catalogue handler | TC-106: `TestCatalogueQueryValidation`, `TestCatalogueHTTPPaginationAndValidation`, `TestCatalogueDiscoveryPostgres` |
| REQ-076: public catalogue facet values and title counts | `CatalogueRepo.Facets`, `CatalogueHandler.Facets` | TC-107: `TestCatalogueDiscoveryPostgres/facets_exclude_archived_values_and_count_titles` |
| REQ-077: new arrivals by cataloguing time | `CatalogueRepo.NewArrivals`, discovery service/handler | TC-108: `TestCatalogueDiscoveryPostgres`, `TestCatalogueHTTPPaginationAndValidation` |
| REQ-078: related active titles, shared subjects before authors | `CatalogueRepo.Related`, discovery service/handler | TC-109: `TestCatalogueDiscoveryPostgres/related_subjects_before_authors_and_archive_exclusion`, `TestRelatedHTTPBoundsAndDefault` |
| REQ-079: optional metadata and explicit public response projection | migration 0013, book scans, `BookView.MarshalJSON` | TC-110: `TestBookViewPublicMetadataAndRetention`, `TestCatalogueDiscoveryPostgres` |
| REQ-080: private member saved searches, validation, ownership, maximum 20 | migration 0014, saved-search repository/service/handler/router | TC-111: `TestSavedSearchHTTPIdentityAndOwnership`, `TestSavedSearchOwnershipAndConcurrentLimit`, saved-search service tests |
| REQ-081: future notifications disabled; preserve desk loan route and OpenAPI conventions | versioned saved-query model, disabled notification state, route/spec additions | TC-112: `TestSavedSearchOwnershipAndConcurrentLimit`, `TestDeskLoanRouteStillRejectsMember`, `catalogue_contract_test.go` |

## Decisions and interface

DEC-031: preserve the existing `available` meaning: at least one circulating copy
can leave the library after final-copy retention. `borrowable` uses the same
server calculation. Explicit false selects titles failing that condition.
Single-copy circulating titles remain borrowable; a title with two or more usable
circulating copies retains one. Lost, damaged, withdrawn, and non-circulating
copies do not inflate lending stock. These are catalogue availability indicators;
actual lending still uses the existing circulation transactions and member rules.

DEC-032: optional faculty and department describe a title's catalogue affiliation.
They are never inferred from member records. New columns are nullable and no
metadata is backfilled or invented. Librarians may supply edition, language,
faculty and department when cataloguing a title. The public book projection keeps
legacy bibliographic field names and adds requested lowercase optional metadata;
internal stock and affiliation fields are not serialized. Creation timestamps use
UTC. Existing `published_year` input and legacy `PublishedYear` response remain;
`publication_year` is the new response alias.

`GET /books` accepts all requested parameters and the existing title, ISBN,
call-number and LCC-class access points. Query strings reject unknown/repeated
keys, blank values, malformed encoding, invalid booleans/enums/year ranges, and
out-of-bound pagination. Page defaults to 1 and is bounded at 1,000,000; per-page
defaults to 20 and is bounded at 100. Search strings are bounded at 200 characters,
except free text at 500. `newest`/`oldest` (aliases `year_desc`/`year_asc`) sort by publication year, with missing
years last. New arrivals sort by creation time. All orders have UUID tie-breakers.
The count and page execute in a read-only repeatable-read transaction, preserving
correct totals even for empty pages beyond the end.

Facets count distinct active titles, not copies. Empty facets return arrays.
Related titles are ordered by number of shared subjects, then shared authors,
then title and ID; the source and archived candidates are excluded. An absent or
archived source returns an empty related-title array. Related results default to
six titles and accept only `per_page`.

Saved-search endpoints require the existing authentication and member middleware.
The owner comes only from the token. Query JSON requires actual booleans/integers,
rejects nulls and unknown fields, and shares search validation. Names are trimmed,
nonempty, at most 100 characters and free of control characters. Creation locks
the member row, checks the limit and inserts the search and audit entry in one
transaction. Deletes are scoped by both search and owner and report NOT_FOUND
for missing or foreign IDs. There is no endpoint to read another owner's search.
Audit records contain the action/entity/actor, not the private name or query.
Future matching can use query_version, notifications_enabled (false by default),
and last_checked_at (NULL). No matcher, notification opt-in endpoint or outbox
producer was added.

## Migration and release

0013 adds nullable catalogue metadata plus public creation/year/language/
affiliation and reverse subject/author indexes. 0014 creates saved searches with
owner/creation and future notification-check indexes. Existing migrations are
unchanged. Both use the existing embedded, checksummed, transactional runner.

All migrations, including opt-in development seed data, were applied successfully
to an isolated local database. No hosted database was changed. Deploy using the
existing pipeline; do not manually modify applied migrations. A code rollback
can leave these additive columns/table in place without discarding saved data.

## Verification evidence — 2026-09-07

- `go test -race ./...` passed with TEST_DATABASE_URL set to isolated PostgreSQL.
- `go test -race -tags livedb ./...` passed with DATABASE_URL set to a separate,
  seeded local test database and TEST_DATABASE_URL set for isolated schema tests.
- `go vet ./...` and `git diff --check` passed.
- New database tests create and clean random schemas. Concurrent save testing
  observed 20 successes and 10 limit errors from 30 simultaneous requests.
- Independent review found and resolved malformed query parsing and three
  documentation mismatches. No additional actionable findings remained.
- `POST /api/v1/loans` registration and circulation logic were unchanged.

Process improvement: parse RawQuery explicitly for strict endpoints because
URL.Query silently discards malformed pairs. This is now a regression-tested
validation requirement. Existing unrelated risk entries remain open; this change
makes no claim to resolve them or to have been deployed.
