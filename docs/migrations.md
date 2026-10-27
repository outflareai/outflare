# Migrations

The engine migrates its database when it starts (`outflare serve`) or when you run `outflare migrate`. Migrations are the numbered SQL files in `engine/internal/db/migrations`, applied in filename order, each in its own transaction, under an advisory lock so several replicas can start at once.

Two versions of the code always share one database for a while:

- **During a deploy**, the new version migrates and starts while the old one is still serving.
- **After a rollback**, the previous version runs on the schema the newer one left behind. Migrations never run backwards: the older binary skips the ones it doesn't know.

So every migration must leave a schema that the version before it still works on. CI checks this on every pull request (the `migrations` job): `scripts/checkmigrations` reads the files the branch adds, and the base branch's tests then run on a database migrated by the branch.

## Rules

1. **Append only.** Never edit, rename or delete a migration that is on main. Fix a mistake with a new migration.
2. **One number each.** Name the file `NNNN_what_it_does.sql` with the next free number. Parallel branches collide: check main and the open pull requests before you pick one, and renumber if someone merges first. (A few early files share a number; they stay as they are, because renaming would run them again.)
3. **Expand, then contract.** A migration may add tables, columns, indexes and constraints the old code doesn't break. It may not, in the same release as the code that stops using them:
   - drop or rename a table, column, view, type or function;
   - change a column's type;
   - add `NOT NULL` to an existing column, or add a `NOT NULL` column without a `DEFAULT` (the old code's inserts leave it out);
   - `TRUNCATE` or delete data the old code still reads.

   To rename or remove something, do it in steps that each ship on their own: add the new column and write both; backfill and switch reads; once no deployed version uses the old one (a release later, so a rollback can't bring it back), drop it in a migration that says why it is safe now with a line like

   ```sql
   -- migration-safety: nothing reads mailboxes.old_status since 0061 (v0.4.0)
   ```

   That line is the only way past the check; reviewers read it.
4. **Keep it quick.** A migration runs while the old version serves, and holds its locks until its transaction ends. Backfill large tables in a job, not in the migration.

## Upgrading and rolling back a self-hosted install

Upgrading is pulling the newer image and starting it; it migrates on start. Take a database backup first (`pg_dump`). To roll back, start the previous image on the same database: by rule 3 the schema still suits it. Restore the backup only if the newer version wrote data you need to undo.
