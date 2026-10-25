# Development Baselines

Fresh-install snapshots of the unreleased numbered chain, named
`<schema>_dev.sql`. Only `KIKOTO_MODE=development` reads them; see
[Migrations](../../../docs/development/migrations.md#development-baselines).

`097_dev.sql` contains the final `source_field_translation` table with its
cascading `file_source` reference. It is a fresh-install snapshot only. A
database that executed the earlier no-foreign-key 095 must start normally so
the preflight can validate its exact historical fingerprint and migration 097
can rebuild the table; do not replace that database with this baseline.

Migration 097 has no conditional branch: every database that reaches it runs
the same table rebuild, copies only rows whose `source_id` still exists,
drops orphan cache rows, and recreates both indexes.

An instance that cannot start can normalize the historical 095 ledger row
offline without migrating:

```sh
kikoto admin preflight-095 --database "$KIKOTO_DB_PATH" --backup-dir "$KIKOTO_DB_BACKUP_DIR"
```

Regenerate this baseline from the numbered chain:

```sh
cd backend/migrations
go run ../cmd/schema-baseline -migrations . -version-file ../../VERSION
```
