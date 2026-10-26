-- A trial is a run in which a person tried one process on inputs they gave
-- it, with nothing before or after it running; see trials.go. trial_of is
-- that process, kept after it is deleted, as rerun_from is, and
-- trial_of_name its name; null for the runs of a flow. Counting runs leaves
-- trials out, and its key finds the few there are.

-- +goose Up
ALTER TABLE runs ADD COLUMN trial_of bigint DEFAULT NULL, ADD COLUMN trial_of_name varchar(255) NOT NULL DEFAULT '',
  ADD KEY runs_trial (trial_of);

-- +goose Down
-- Which runs were trials is lost; they stay, as runs of their flows.
ALTER TABLE runs DROP KEY runs_trial, DROP COLUMN trial_of_name, DROP COLUMN trial_of;
