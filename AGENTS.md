# Spoletta

Visual builder and engine for agent workflows, modeled on OPM (Object-Process Methodology). Users draw flows of processes and objects on a board; runs execute them server-side with AI agents, bash scripts, people and Sentry fetches.

## Layout
- `webapp/` Go 1.24 HTTP server, stdlib `net/http` mux, MySQL via `database/sql` (no ORM). One package `main`, one file per domain.
- `webapp/frontend/` React 19 + TypeScript + Vite + `@xyflow/react` (React Flow) + dagre. Plain CSS in `index.css`. All API calls and types live in `api.ts`.
- `worker/` Go service that runs the `pi` coding agent (npm `@earendil-works/pi-coding-agent`) and bash scripts for the webapp; manages per-run git workspaces (`workspace.go`): what a step leaves uncommitted is committed with the run, step and pi session as trailers, and once a run is over the webapp has its checkouts released (`releaseWorkspaces` in `webapp/workspace.go`), keeping branches and sessions, so a follow-up or re-run makes them again from the branch. `*.ts` there are pi extensions.
- `compose.yaml` runs mysql + worker + webapp with podman compose. Secrets go in `.env` (never committed).

## Key concepts (start here before editing)
- Flow: a board of processes, objects, links. Kinds: job flow, module/procedure (reused by subflow processes, `modules.go`), struct (single reusable object, `structs.go`).
- Process kinds: agent, script, human, flow (nested board, `boards.go`), subflow (`calls.go`), sentry.
- Objects have states, attributes, parts; links are OPM links (instrument, consumption, effect, condition on state). Runtime instances in `instances.go`.
- `engine.go`: executes runs; header comment documents the scheduling semantics. `runs.go` stores runs/steps.
- `reruns.go`: re-running an ended run from one of its processes as a new run on the board as it is: what came before the process is copied as a `copied` step, the process starts with what it took (editable in `RerunDialog.tsx`), and it and what follows run anew (`walk.resume` in `engine.go`), on a branch starting from the commit the run's branch was at as the process started.
- `shared.go`: a struct's object marked shared keeps one store of records every copy of it reads and writes, in whatever flow (`objects.store_id`, `shared_records`), told apart by a unique attribute. A process only generating it adds and refreshes records keeping their state; one taking or querying it too moves them to the state it comes out in. Queries pick what gets worked on; no run claims or releases a record.
- `search.go`: the Data and Records pages' search, a faceted query, as a log explorer takes one (`status:failed -@Bug.state:fixed timeout`, `key:(a OR b)`, `key:>10`, `started:>now-1d`) run on the server over every stored run or record, with facet counts and sorting. Each column is a facet: the table's own bare (`status`), an object's attributes `@Object.attribute`. `frontend/src/query.ts` parses queries the same way, to colour them and rewrite them as facets are ticked; keep the two grammars in step (`search_test.go`).
- `versions.go`: full-board snapshots; `history.go` undo/redo reuses them. Adding board data means updating `snapshot` and `restoreSnapshot`, and usually `paste.go` and `opl.go`.
- `opl.go`: renders the board as OPL sentences, shown in the UI and given to agents.
- `assistant.go`: each flow's assistant, a chat at the bottom right of the board (`Assistant.tsx`). pi in the worker, run without tools and kept to the flow by its prompt, replies with the flow as a setup file, which is imported through `setup_import.go` (saving a version first); it may change only that flow.
- `mcp.go`: MCP servers via OAuth, proxied per agent step at `/mcp/{id}`. Admins configure servers; each person connects them for themselves (`mcp_tokens`), and an agent step uses the connections of whoever its run is for (`runUser`). Their tokens, like model keys, are sealed with `SECRET_KEY` (`secrets.go`), rotated through `SECRET_KEY_SECONDARY` and `webapp seal-secrets` (`docs/SECRETS.md`); either key can come from a `_FILE`.
- `model_keys.go`: model providers' API keys, set by admins, by the environment variable pi reads each from; the worker says which variables its models read (`worker/keys.go`, from `models.json`) and gets the saved keys per agent step.
- `users.go`: people sign in over OpenID Connect, with the company's provider, Google or both (off without `OIDC_ISSUER` and `GOOGLE_CLIENT_ID`), the same user with either (`user_identities`); every `/api/` request then needs a session, and runs and human decisions record who. `user_roles.go`: a user's roles name the human objects whose processes they decide ("Finance handles Approve payment"), checked when a review is decided. Not to be confused with a link's role (instrument, consumption). Admins add, remove (soft: `removed_at`) and promote people in the settings (`#/settings`, `Settings.tsx`).
- `teams.go`: teams of people, made by admins and team managers. `owners.go`: every flow, procedure and struct is global, a team's or a user's (its maker's by default); `guardFlows` turns away requests about flows the user doesn't see, finding the flow from the path, query and JSON body, so a new route with a new kind of id needs an entry there. Role holders see flows whose reviews they decide, read-only.
- `docs/FLOWS.md`: how to author flows for a user (OPM, run semantics, step contracts, worked setup-file examples). Read it before generating a flow.

## Database
- No migration files. Each domain file has `migrateX(db)` with `CREATE TABLE IF NOT EXISTS` plus `addColumn(...)` for new columns; called at startup from `main.go`. Changes must work on existing databases.

## Build & test
- Frontend: `cd webapp/frontend && npm run build` (runs `tsc -b`). Dev: `npm run dev` proxies `/api` to :8080.
- Go: `cd webapp && go build ./... && go vet ./...`; same in `worker/`.
- Unit tests: `cd webapp && go test .`
- E2E tests: `cd webapp && go test -race ./tests` need compose MySQL on 127.0.0.1:3306 (`podman compose up -d mysql`; `MYSQL_PUBLISH` and `TEST_DB_ADDR` move it); otherwise skipped. They build and start the real webapp, drive it over HTTP, and fake the worker. New behavior gets an e2e test in `tests/*_e2e_test.go`.
- Full stack: `podman compose up --build`, app on http://localhost:8080.

## Conventions
- Comments are plain English prose explaining what and why, in the domain's vocabulary; every file starts with one describing its concept. Match that.
- Commit subjects: one plain sentence in imperative mood describing the user-visible change, no prefixes (e.g. "Undo and redo changes to the board with Cmd-Z and Cmd-Shift-Z"). Body optional, prose.
- Keep dependencies minimal; prefer stdlib.
