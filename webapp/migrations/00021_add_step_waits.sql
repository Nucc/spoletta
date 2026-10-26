-- How long an agent, script or connector step waited for its slots before
-- it started, in milliseconds: its person's (limits.go), then one of its
-- replica's; and which try of its process it is, 1 for the first and one
-- more for each retry after a failure; see engine.go's execute. Both are
-- null for other steps, and for steps from before they were recorded.

-- +goose Up
ALTER TABLE run_steps ADD COLUMN slot_wait_ms bigint DEFAULT NULL, ADD COLUMN try int DEFAULT NULL;

-- +goose Down
-- How long steps waited for their slots, and which try each was, is lost.
ALTER TABLE run_steps DROP COLUMN slot_wait_ms, DROP COLUMN try;
