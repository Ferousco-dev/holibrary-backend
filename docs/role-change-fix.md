# Role change category correction — CR-002 / DEF-029

REQ-082: an administrator can change a librarian into a normal member with an
explicit borrowing category. A member without a category must produce an
actionable domain error, not a database constraint error or an invented category.

The production failure was reproduced against local PostgreSQL: `role=member`
with `category=NULL` violates `members_need_a_category` (SQLSTATE 23514).
Accounts created as librarians intentionally have no borrowing category.

`PATCH /api/v1/members/{id}/role` now accepts optional `category` alongside role.
For example, the stakeholder-selected transition is:

```json
{"role":"member","category":"staff"}
```

Existing categories are preserved when omitted. Missing categories required by a
member transition produce 422 `NO_CATEGORY`; invalid categories or a category
supplied with a staff authorization role produce 400 `INVALID_MEMBER_CATEGORY`.
Role and category changes, the session-revocation timestamp, and the audit entry
commit together. Last-administrator protection remains in place. Administrator
rows are locked in consistent order before target rows to avoid deadlocks.

Session checks compare JWT role against the current database role in the existing
primary-key lookup, so whole-second JWT timestamps cannot preserve stale staff
claims following demotion. Revocation uses clock_timestamp after lock acquisition
rather than the transaction-start now(). Existing middleware failure handling and
password-change timestamp tolerance are unchanged.

Verification:

- TC-113: TestLibrarianDemotionNeedsCategory reproduced the old SQLSTATE 23514,
  then passed with the domain error.
- TC-114: TestRoleChangeCategoryAndAuditAreAtomic covers all categories,
  promotion/demotion preservation, category-only edits, rollback, audit metadata,
  stale-role JWTs, and missing targets.
- TC-115: TestConcurrentAdministratorDemotionsPreserveOneAdmin proves one admin
  remains and neither request deadlocks.
- TC-116: TestRoleRevocationTimestampFollowsLockWait and
  TestAuthenticateRejectsOutdatedRole cover session invalidation.
- TC-117: TestMemberRoleHTTPCategoryAndAuthorisation and
  TestSetRoleValidatesBorrowingCategory cover HTTP/service validation and access.

The complete race-enabled backend suite passed against isolated local PostgreSQL,
and go vet passed. OpenAPI was updated. No schema migration or weakened database
constraint is needed. Deployment uses the existing GitHub CI -> Render pipeline.
