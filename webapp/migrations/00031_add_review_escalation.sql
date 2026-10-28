-- Handing a review on: a review whose process escalates keeps when it is
-- due to, by the database's clock, so whichever replica is alive then acts
-- on it, and whether it was, after which the roles its process names may
-- decide it too; the index finds the steps waiting that are due. See
-- notify.go.

-- +goose Up
ALTER TABLE run_steps
  ADD COLUMN escalate_at timestamp(3) NULL DEFAULT NULL,
  ADD COLUMN escalated tinyint(1) NOT NULL DEFAULT 0,
  ADD KEY run_steps_escalate (status, escalate_at);

-- +goose Down
-- Reviews handed on go back to those their people name alone.
ALTER TABLE run_steps
  DROP KEY run_steps_escalate,
  DROP COLUMN escalated,
  DROP COLUMN escalate_at;
