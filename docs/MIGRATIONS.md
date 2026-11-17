# Database migrations

The webapp keeps the MySQL database's schema with migrations: numbered SQL
files in [webapp/migrations/](../webapp/migrations), run in order by
[goose](https://github.com/pressly/goose) as the webapp starts, each once.
Which ran is recorded in the `goose_db_version` table. The files are
embedded in the webapp's binary, so an image carries the migrations of its
own version and needs nothing else to migrate its database. How it works is
described at the top of [webapp/migrations.go](../webapp/migrations.go).
A webapp built on Spoletta's code keeps migrations of its own apart, as
[local migrations](#local-migrations).

## What there is

- `00001` brings a database from before migrations up to date, running the
  code that kept the schema then ([webapp/legacy_schema.go](../webapp/legacy_schema.go)).
  A new database has nothing to bring and skips it.
- `00002_schema.sql` is the whole schema as it stood when migrations began,
  which a new database gets in one go. A database from before then has all
  of it already, from `00001`.

Neither can be undone, so a database goes no lower than version 2.

## Upgrading

Nothing to do: a webapp starting migrates its database to its latest
migration before serving, and logs each one it ran:

```
migrated the database to version 3 in 20ms
```

If one fails the webapp stops with its error and serves nothing, so a
broken migration doesn't leave a webapp running against a schema it doesn't
expect. Take a backup before deploying a version with migrations: MySQL
commits every `CREATE`, `ALTER` and `DROP` at once, so a migration failing
half way leaves what it had done in place, to be finished or put back by
hand.

Goose takes no lock on MySQL, so two webapps starting at once against the
same database may both run a migration. With more than one replica, start a
version with migrations on one first, or deploy with `strategy: Recreate`.

## Seeing where the database is

```sh
webapp migrate status
```

lists every migration the webapp knows, applied or pending, and when it
ran:

```
VERSION  STATE    APPLIED AT (UTC)     MIGRATION
1        applied  2026-10-21 22:05:52  upgrade from before migrations
2        applied  2026-10-21 22:05:52  00002_schema.sql
3        pending                       00003_add_run_labels.sql
```

It takes the database from the same settings as the webapp
([config/database.yaml](../config/database.yaml.example), `APP_ENV` or the
`DB_*` variables), and changes nothing.

## Migrating down

Going back to an older version of the webapp means taking the database back
to the migrations that version knows first:

1. **Find the version to go to**: the older webapp's last migration, the
   highest number in its `webapp/migrations/`.
2. **Migrate down with the webapp you have now**, the one whose migrations
   are to be undone; the older one doesn't know them:

   ```sh
   webapp migrate down-to 2
   ```

   It undoes every applied migration above that version, newest first, each
   by the statements under its `-- +goose Down`, and says what it undid.
   Before undoing anything it checks each has a Down, and refuses if one
   hasn't, naming it.
3. **Deploy the older webapp.**

An older webapp started on a database still ahead of it logs a warning
naming the command to run, and starts all the same, since a newer schema
usually only adds what the older code doesn't use:

```
WARNING: the database is at version 3, past this webapp's last migration, 2; migrate it down with the webapp that migrated it (`webapp migrate down-to 2`)
```

Undoing can lose data for good: a Down dropping a column drops what people
saved in it since. Each migration says so above its Down when it does, and a
backup taken before migrating down keeps it.

### With compose

```sh
podman compose run --rm webapp webapp migrate status
podman compose run --rm webapp webapp migrate down-to 2
```

run with the image built from the current code, then check out the older
version and `podman compose up --build`.

### In Kubernetes

Step 2 is a Job with the current image, as for sealing secrets
([SECRETS.md](SECRETS.md)), before rolling the Deployment back:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: spoletta-migrate-down
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate-down
          image: <the current webapp's image>
          args: ["webapp", "migrate", "down-to", "2"]
          # plus DB_HOST, DB_NAME, DB_USER, DB_PASSWORD as the Deployment has them
```

```sh
kubectl scale deployment/spoletta-webapp --replicas=0
kubectl apply -f migrate-down-job.yaml
kubectl wait --for=condition=complete job/spoletta-migrate-down --timeout=5m
kubectl logs job/spoletta-migrate-down
kubectl rollout undo deployment/spoletta-webapp
kubectl scale deployment/spoletta-webapp --replicas=<as before>
```

Scaling down first keeps the current webapps from writing to what is being
undone. `backoffLimit: 0` keeps a failed Job from running again on a
database it left half way.

## Writing a migration

A change to the schema is a new file in `webapp/migrations/`, numbered one
above the last, with a name saying what it does:

```sql
-- Runs can be given a label people search by.

-- +goose Up
ALTER TABLE runs ADD COLUMN label VARCHAR(255) NOT NULL DEFAULT '';
CREATE INDEX runs_label ON runs (label);

-- +goose Down
-- The labels given since go.
DROP INDEX runs_label ON runs;
ALTER TABLE runs DROP COLUMN label;
```

- **Never edit a migration that ran** anywhere but on your machine: the
  databases it ran on won't run it again. Change it with a new one.
- **Give every migration a Down** that undoes its Up, in reverse order:
  dropping what it added, putting back what it changed. Where undoing loses
  data, say so in a comment above it.
- **Keep each small**, one change where you can: with MySQL committing each
  statement, a migration of one statement either happens or doesn't.
- **Make it work on the data already there.** A `NOT NULL` column needs a
  default, or an `UPDATE` filling it before the constraint; a unique key
  needs the duplicates gone first.
- **Data changes go in migrations too**, as `UPDATE`s next to the schema
  change they follow from.
- What must happen on every start rather than once, such as taking over or
  ending runs a stopped replica left running, isn't a migration: it goes in
  `endStopped` in [webapp/replicas.go](../webapp/replicas.go).

Numbers needn't follow one another, as `00053` and `00065` show, but goose
runs migrations in order and refuses one numbered below the highest a
database ran: a new migration is numbered above the highest on any database
it will meet.

A statement with semicolons inside it, such as a trigger, goes between
`-- +goose StatementBegin` and `-- +goose StatementEnd`. A migration that
can't be written in SQL can be one in Go, registered in
[webapp/migrations.go](../webapp/migrations.go) as `00001` is.

Try it both ways before committing: start the webapp so it migrates up,
check with `webapp migrate status`, run `webapp migrate down-to` the
version before it, and start again. From `webapp/`, `go run . migrate status`
does the same as the binary. The e2e tests (`go test -race ./tests`) start
the webapp on a new database, so they run every migration up.

## Projects

Everything below the projects is a project's ([PROJECTS.md](PROJECTS.md)),
which the schema says, so a new table follows these rules:

- It has `project_id BIGINT NOT NULL`, with **no default**, so a row
  inserted without naming its project is refused. A table above the projects,
  as `users` and `sessions` are, is listed in `aboveProjects` in
  `webapp/tests/projects_e2e_test.go` with why; `TestProjectsEveryTableNamesItsProject`
  fails for any other without the column, or with a default.
- A table other rows refer to has `UNIQUE (id, project_id)`, and each
  foreign key to a row of a project names the project beside the id,
  `FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id)`, so
  the database refuses a row in another project than its parent. One naming
  a person as one of a project's, an owner or a member, refers to
  `project_members (user_id, project_id)`.
- MySQL can't null `project_id`, so such a key can't be `ON DELETE SET
  NULL`: it takes no action, and the code nulls the column before deleting
  what it refers to (`releaseGoing` and `deleteFlow` do).
- A name unique before projects is unique within its project, the key
  starting with `project_id`.
- Deleting a project for good empties every table with a `project_id`
  column, read from `information_schema` (`projectTables` in
  `webapp/projects.go`), so a new table needs nothing more for it.

Every migration from `00049`, which made the projects, on has the same check
at the top of its Down, which fails while a project other than Legacy is
left, since going back past any of them would lose which project everything
was in:

```sql
-- +goose Down
-- As every migration since projects came in, going down is refused while a
-- project but Legacy is left, as going further down would lose which
-- project everything was in: the row inserted fails its check, which says
-- so.
DROP TEMPORARY TABLE IF EXISTS projects_down;
CREATE TEMPORARY TABLE projects_down (others BIGINT NOT NULL,
  CONSTRAINT `delete every project but Legacy before migrating down` CHECK (others = 0));
INSERT INTO projects_down SELECT COUNT(*) FROM projects WHERE id <> 1;
DROP TEMPORARY TABLE projects_down;
```

A new migration's Down starts with it too. So undoing any of them while
other projects exist is refused, naming the check: delete every project but
Legacy for good first, from the Projects page.
Going below `00049` drops the projects, who was given them and the groups
people are in, and everything is the instance's again.

## Local migrations

A webapp built on Spoletta's code, merging its releases into a repository
of its own, may need tables or columns Spoletta doesn't have. The
migrations making them are kept apart from Spoletta's, so that the two never
take the same number: Spoletta's next migration may well be numbered as
yours would have been.

- They go in `webapp/local_migrations/`, numbered from `00001`, written as
  [above](#writing-a-migration), each with its Down. Spoletta ships the
  directory with only its README: add files beside it and leave the README
  as it is, and Spoletta's releases merge without touching them.
- Goose records them in `goose_local_version`, apart from Spoletta's in
  `goose_db_version`.
- They run as the webapp starts, after Spoletta's, so they may change
  Spoletta's tables, adding a column to `users`, say. Whoever merges a
  Spoletta release changing what a local migration changes looks out for
  it.
- A table they make is a project's, or above the projects, by the same
  rules as Spoletta's ([Projects](#projects)).
- A webapp built as Spoletta ships it can take them from a directory
  instead: `LOCAL_MIGRATIONS=/path/to/dir` replaces the built-in ones (mount
  it into the container). Naming a directory that isn't there stops the
  webapp before it migrates anything.

`webapp migrate status` lists them after Spoletta's, each named `local …`:

```
VERSION  STATE    APPLIED AT (UTC)     MIGRATION
53       applied  2026-11-17 09:12:40  00053_add_project_foreign_keys.sql
1        applied  2026-11-17 09:12:41  local 00001_add_user_badges.sql
2        pending                       local 00002_add_badge_colours.sql
```

and `webapp migrate local down-to <version>` undoes them as `down-to` undoes
Spoletta's, leaving Spoletta's as they are. Going back to an older webapp,
undo the local migrations it doesn't know first, then Spoletta's, since a
local migration may stand on what a Down of Spoletta's takes away:

```sh
webapp migrate local down-to 1
webapp migrate down-to 48
```

The second, going below the projects, is refused while a project other than
Legacy is left ([Projects](#projects)).

A webapp without local migrations started on a database that ran some logs
a warning saying so, and starts all the same.

### Moving migrations of your own here

Migrations of your own numbered among Spoletta's are recorded in
`goose_db_version` under numbers Spoletta's own migrations come to take, and
goose, finding a number recorded, skips Spoletta's migration of that number
without a word, leaving the schema short of it. Move them before merging the
Spoletta release whose numbers reach theirs: the files into
`local_migrations/`, numbered from `00001`, and, on each database that ran
them, their records, before the new webapp starts on it and with every
webapp stopped:

```sql
CREATE TABLE goose_local_version LIKE goose_db_version;
INSERT INTO goose_local_version (version_id, is_applied, tstamp)
  SELECT 0, TRUE, NOW()
  UNION ALL SELECT version_id - 48, is_applied, tstamp
            FROM goose_db_version WHERE version_id IN (49, 50) AND is_applied;
DELETE FROM goose_db_version WHERE version_id IN (49, 50);
```

Here your migrations were numbered 49 and 50 and become local 1 and 2. Goose
needs the row for version 0, which it would have written itself had it made
the table. A database that never ran them needs nothing.
