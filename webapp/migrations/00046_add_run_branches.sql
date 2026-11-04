-- The branch a run's checkouts are on when its flow names it after what the
-- run works on, such as feature/PROJ-123 for a ticket; empty for a run on a
-- branch of its own. Kept so that a run taken over, re-run or followed up
-- works on the same one. See branch_names.go.

-- +goose Up
ALTER TABLE runs
  ADD COLUMN branch varchar(255) NOT NULL DEFAULT '';

-- +goose Down
-- Which branches runs were named on is lost; a run taken over or re-run
-- afterwards works on a branch of its own, as its checkouts are made again.
ALTER TABLE runs
  DROP COLUMN branch;
