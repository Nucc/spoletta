# Developing Spoletta

| Part | Where | Stack |
|---|---|---|
| Webapp | `webapp/` | Go, stdlib `net/http`, MySQL |
| Frontend | `webapp/frontend/` | React, TypeScript, Vite, React Flow |
| Worker | `worker/` | Go, running pi and bash |

[AGENTS.md](../AGENTS.md) maps the code and its conventions: start there
before changing a part you haven't worked on.

## Building and testing

```sh
cd webapp && go build ./... && go vet ./... && go test .
cd webapp && go test -race ./tests      # e2e, needs `podman compose up -d mysql`
cd worker && go build ./... && go vet ./... && go test .
cd webapp/frontend && npm run build     # or npm run dev, proxying /api to :8080
cd webapp/frontend && npm test
```

The e2e tests build and start the real webapp, drive it over HTTP and fake
the worker. They need compose's MySQL on 127.0.0.1:3306, and are skipped
without it; `MYSQL_PUBLISH` and `TEST_DB_ADDR` move it. New behaviour gets
an e2e test in `webapp/tests/`.

CI ([.github/workflows/ci.yml](../.github/workflows/ci.yml)) runs all of
this on every push to main and every pull request, the e2e tests on a MySQL
like compose's.

## The database

The webapp migrates its database as it starts. [MIGRATIONS.md](MIGRATIONS.md)
says how to write a migration and how to migrate down to run an older
version.

## Replicas

Any number of webapps can run on one database, such as a Deployment's pods
behind one Service. [REPLICAS.md](REPLICAS.md) says what they share and
what happens as one stops; nothing that must reach every replica may live
only in one's memory.
