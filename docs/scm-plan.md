# Software Configuration Management Plan
## HOLibrary Backend

**Course:** SEN 106 / SEN 216, Introduction to Web Technologies
**Phase:** 06 Configuration Management · **Gate:** G6
**Structure:** IEEE 828

---

## 1. Purpose

This plan states how the project controls what exists, who changed it, how a
change reaches students, and how it is taken back if it should not have.

Configuration management is usually described as bookkeeping. It is better
understood as the answer to one question asked in a hurry: **the library is
behaving oddly, what is actually running, and how do we get back to when it
worked?** Everything below exists to make that answerable in minutes.

## 2. Configuration items

| Item | Where | Controlled by |
|---|---|---|
| Application source | `Ferousco-dev/holibrary-backend` | Git |
| Database schema | `internal/migrate/sql/` | Git + `schema_migrations` |
| API contract | `internal/transport/http/docs/openapi.yaml` | Git, embedded in the binary |
| Container image | built from `Dockerfile` | Git, rebuilt per deploy |
| Infrastructure | `render.yaml` | Git |
| Secrets | Render environment | **Not** in Git, by design |
| Process record | `.ilana/` | Git |
| Documents | `docs/` | Git |

`.ilana/` is committed on purpose. Process history is project history, and a
decision record that lives only on one laptop is not a record.

## 3. Version control

**One repository per deployable.** Backend and frontend are separate, because
they deploy separately and to different providers. They are coupled only through
the OpenAPI contract, which the backend serves.

**Branching.** `main` is always deployable. Work happens on `main` directly for
a project this size; a team of five would branch per change and merge through a
pull request, which the pipeline already supports (it runs on `pull_request`).

**Commit messages** state what changed and why, in prose. A message that only
restates the diff wastes the one place where reasoning survives.

**Authorship.** Commits are authored `Ferousco-dev <ferouslos6@gmail.com>`. This
was corrected once: the whole history was originally committed under an address
not linked to the GitHub account, so no commit was attributed. History was
rewritten and force-pushed, with the original kept in `refs/original/` as an
undo point.

## 4. The schema is version-controlled too

Migrations are numbered, forward-only, and embedded in the binary. The
application applies pending ones at startup, under a Postgres advisory lock so
two instances starting together cannot both run them.

Three rules make this trustworthy:

1. **Applied migrations are recorded with a checksum.** Editing one after it has
   run is refused rather than reapplied, because the database and the repository
   would otherwise disagree about history without saying so.
2. **The pipeline enforces the same rule earlier.** A pull request that modifies
   an existing migration fails, so the problem surfaces in review rather than at
   startup in production.
3. **Seeds are opt-in.** `0004_seed.sql` creates accounts whose passwords are
   published in a public repository. Production refuses to start with seeding
   enabled.

## 5. Change control

Every change follows: **submit, review, verify, deploy, record.**

| Stage | What happens |
|---|---|
| Submit | A commit, or a pull request for a team |
| Review | The pipeline: build, vet, gofmt, race tests, coverage gate, vulnerability scan, migration safety, image build |
| Verify | Smoke tests against the deployed service |
| Deploy | Only from `main`, only after every check passes |
| Record | Ledger entry; decisions as `DEC-###`; defects as `DEF-###` |

Scope changes are recorded as decisions with their reasoning, not absorbed
silently. Thirty are recorded so far, several of which say **no** to something
that was asked for, or explain why an obvious-looking option was rejected.

## 6. Build and release

```
git push main
      │
      ▼
  test · security · migrations · docker      (all must pass)
      │
      ▼
  deploy to Render via API, poll for outcome
      │
      ▼
  smoke test the running service
```

**Deployment does not happen on push.** Render's own auto-deploy builds straight
from a commit, before tests run, which would put an untested version in front of
students. It is switched off (DEC-028).

It was also silently broken: a service created through the API has no GitHub
webhook, so four commits sat undeployed while the dashboard reported auto-deploy
enabled. **A deployment serving old code looks healthy in every respect except
being current**, which is why the pipeline polls for the outcome rather than
firing and hoping.

**Build reproducibility.** Multi-stage Docker build, pinned Go version, no
toolchain in the final image. `scratch` base plus CA certificates and the static
binary: about 15 MB.

## 7. Environments

| | Database | Redis | Seeds | Secrets |
|---|---|---|---|---|
| Local | Postgres in Docker | Redis in Docker | yes | `docker-compose.yml`, published, development only |
| Production | Neon | Upstash, shared, namespaced | **refused** | Render environment |

The same migration code runs in both, so a migration that works locally has been
tested rather than merely having worked under a different mechanism.

## 8. Release identification

A release is a commit on `main` that reached `live`. Render records the deploy
against its commit, so *what is running* is answerable exactly:

```bash
render deploys list srv-dacuhqad0e5s73fhol80 --output json --confirm
```

## 9. Rollback

**Application.** Render keeps previous deploys and can roll back to one from the
dashboard, which is faster than reverting and rebuilding. Reverting the commit
afterwards keeps the repository honest about what is deployed.

**Schema.** Migrations are forward-only. A mistake is corrected by a new
migration, not by editing or reversing an old one. This is a deliberate
constraint: a rollback that runs `DROP COLUMN` destroys the data in it, and a
library's loan history is not recoverable from a redeploy.

**Practical consequence.** A migration must be additive wherever possible, so an
older application version can still run against a newer schema for the minutes a
rollback takes.

## 10. Backup and restore

Neon retains point-in-time history on the free plan, and the schema can be
rebuilt from migrations at any time.

**The restore has never been performed.** That is stated plainly because a backup
nobody has restored is a theory about a backup. Testing it is recorded as an
outstanding condition on gate G6 rather than assumed away.

## 11. Access and accountability

| Who | Can |
|---|---|
| Repository owner | Push, deploy, rotate secrets |
| Pipeline | Deploy, using an API key held as a GitHub secret |
| Administrator (in the system) | Create staff, read the audit log |
| Librarian | Manage catalogue, members and circulation |
| Member | Read the catalogue and their own record |

The first administrator is bootstrapped once against an empty database, because
there is nobody to attribute that action to. Every administrator after the first
is created through the API, where it is attributable and audited.

## 12. Tooling

Git and GitHub · GitHub Actions · Docker · Render · Neon · Upstash · Cloudflare ·
`govulncheck` · the Ìlànà ledger in `.ilana/`.

## 13. Records

| Record | Location |
|---|---|
| Decisions | `.ilana/decisions.md` (DEC-001 to DEC-030) |
| Defects | `.ilana/defects.md` (DEF-001 to DEF-027, all closed) |
| Risks | `.ilana/risks.md` (RSK-001 to RSK-009) |
| Gates | `.ilana/gates/` (G0, G1, G2, G4, G5, G6) |
| Traceability | `.ilana/traceability.csv` |
| Narrative | `.ilana/ledger.md` |

The ledger has one `CORRECTION` entry. Ten commits were made without being
recorded, and the gap was noticed by the stakeholder rather than by the process.
It is written down rather than backdated: a record that quietly fills its own
holes is worth less than one that shows them.
