-- What each run has spent on its models, with its subflows', items' and
-- follow-ups' runs when it is the run at their top, and on how many calls
-- to models without a known price: llm.go adds each call it records to the
-- run its spending counts toward, so what the Activity page and the flow
-- list read of runs (activity.go) comes from the runs alone, through the
-- indexes below: the top-level runs started in a period, with their flows,
-- how they went and what they spent; the runs going on or failed, newest
-- first; a flow's latest run of its own; and the steps of each kind
-- started in a period, with how and when they ended. What was spent before
-- is added up from the calls recorded.

-- +goose Up
ALTER TABLE runs
  ADD COLUMN spent_micros bigint NOT NULL DEFAULT 0,
  ADD COLUMN unpriced_calls int NOT NULL DEFAULT 0;
UPDATE runs r JOIN (
  SELECT root_run_id, SUM(cost_micros) AS spent, SUM(1 - priced) AS unpriced FROM llm_usage WHERE root_run_id IS NOT NULL GROUP BY root_run_id
) u ON u.root_run_id = r.id
SET r.spent_micros = u.spent, r.unpriced_calls = u.unpriced;
ALTER TABLE runs
  ADD KEY runs_started (parent_run_id, started_at, flow_id, status, spent_micros, unpriced_calls),
  ADD KEY runs_status (status, parent_run_id, started_at),
  ADD KEY runs_flow_top (flow_id, parent_run_id);
ALTER TABLE run_steps ADD KEY run_steps_kind (kind, started_at, status, finished_at, process_ref, run_id);

-- +goose Down
-- What runs spent is left to the calls recorded, as it was.
ALTER TABLE run_steps DROP KEY run_steps_kind;
ALTER TABLE runs DROP KEY runs_flow_top, DROP KEY runs_status, DROP KEY runs_started,
  DROP COLUMN unpriced_calls, DROP COLUMN spent_micros;
