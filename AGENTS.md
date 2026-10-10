# Genie

Visual builder and engine for agent workflows, modeled on OPM (Object-Process Methodology). Users draw flows of processes and objects on a board; runs execute them server-side with AI agents, bash scripts, people and Sentry fetches.

## Layout
- `webapp/` Go 1.24 HTTP server, stdlib `net/http` mux, MySQL via `database/sql` (no ORM). One package `main`, one file per domain.
- `webapp/frontend/` React 19 + TypeScript + Vite + `@xyflow/react` (React Flow) + dagre. Plain CSS in `index.css`. All API calls and types live in `api.ts`.
- `worker/` Go service that runs the `pi` coding agent (npm `@earendil-works/pi-coding-agent`) and bash scripts for the webapp; manages per-run git workspaces (`workspace.go`). `*.ts` there are pi extensions.
- `compose.yaml` runs mysql + worker + webapp with podman compose. Secrets go in `.env` (never committed).

## Key concepts (start here before editing)
- Flow: a board of processes, objects, links. Kinds: job flow, module/procedure (reused by subflow processes, `modules.go`), struct (single reusable object, `structs.go`).
- Process kinds: agent, script, human, flow (nested board, `boards.go`), subflow (`calls.go`), sentry.
- Objects have states, attributes, parts; links are OPM links (instrument, consumption, effect, condition on state). Runtime instances in `instances.go`.
- `engine.go`: executes runs; header comment documents the scheduling semantics. `runs.go` stores runs/steps.
- `versions.go`: full-board snapshots; `history.go` undo/redo reuses them. Adding board data means updating `snapshot` and `restoreSnapshot`, and usually `paste.go` and `opl.go`.
- `opl.go`: renders the board as OPL sentences, shown in the UI and given to agents.
- `mcp.go`: MCP servers via OAuth, proxied per agent step at `/mcp/{id}`.

## Database
- No migration files. Each domain file has `migrateX(db)` with `CREATE TABLE IF NOT EXISTS` plus `addColumn(...)` for new columns; called at startup from `main.go`. Changes must work on existing databases.

## Build & test
- Frontend: `cd webapp/frontend && npm run build` (runs `tsc -b`). Dev: `npm run dev` proxies `/api` to :8080.
- Go: `cd webapp && go build ./... && go vet ./...`; same in `worker/`.
- Unit tests: `cd webapp && go test .`
- E2E tests: `cd webapp && go test -race ./tests` need compose MySQL on 127.0.0.1:3306 (`podman compose up -d mysql`); otherwise skipped. They build and start the real webapp, drive it over HTTP, and fake the worker. New behavior gets an e2e test in `tests/*_e2e_test.go`.
- Full stack: `podman compose up --build`, app on http://localhost:8080.

## Conventions
- Comments are plain English prose explaining what and why, in the domain's vocabulary; every file starts with one describing its concept. Match that.
- Commit subjects: one plain sentence in imperative mood describing the user-visible change, no prefixes (e.g. "Undo and redo changes to the board with Cmd-Z and Cmd-Shift-Z"). Body optional, prose.
- Keep dependencies minimal; prefer stdlib.
