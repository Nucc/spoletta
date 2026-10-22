-- What the instance lets runs and people spend (limits.go), a row each by
-- name, and the run each call's spending counts toward: the run at the top
-- of its subflows and items (spend.go). Calls recorded before count toward
-- their own runs. Follow-ups' calls are recorded with their runs from now
-- on, where they had none.

-- +goose Up
CREATE TABLE limits (
  name varchar(64) NOT NULL,
  value bigint NOT NULL,
  PRIMARY KEY (name)
);
ALTER TABLE llm_usage ADD COLUMN root_run_id bigint DEFAULT NULL, ADD KEY llm_usage_root (root_run_id), ADD KEY llm_usage_run (run_id);
UPDATE llm_usage SET root_run_id = run_id;

-- +goose Down
-- The limits set go, and calls no longer say which run they count toward.
ALTER TABLE llm_usage DROP KEY llm_usage_run, DROP KEY llm_usage_root, DROP COLUMN root_run_id;
DROP TABLE limits;
