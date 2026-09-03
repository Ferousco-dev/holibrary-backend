# Migrations

The SQL lives in `sql/`, embedded into the binary with `go:embed`.

It sits here rather than in a top-level `migrations/` directory for one reason:
`go:embed` cannot reach outside its own package, so a copy at the top level
would be a second source of truth. There was briefly one, and a second copy of
anything is a copy that will eventually disagree with the first.

The same files are used in development and in production, applied by the same
code, so a migration that works locally is a migration that has been tested.

## How they run

The API applies pending migrations at startup, under a Postgres advisory lock so
two instances starting together cannot both run them. Each file runs in its own
transaction; Postgres supports transactional DDL, so a file that fails leaves
nothing behind.

Applied files are recorded in `schema_migrations` with a checksum. Editing a
file after it has been applied is refused rather than reapplied: the database
and the repository would otherwise silently disagree about history. Add a new
numbered file instead.

## Seeds

`0004_seed.sql` is demonstration data and is **skipped unless explicitly asked
for**, because the accounts it creates have passwords published in a public
repository. Applying it to a real deployment would hand an administrator account
to anyone who reads the repo.

Set `SEED_DEMO_DATA=true` to include it. Development does; production must not.
