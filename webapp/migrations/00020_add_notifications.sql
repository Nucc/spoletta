-- Telling people what waits on them; see notify.go. A review whose process
-- reminds or has a time limit keeps when each is due, by the database's
-- clock, so whichever replica is alive then acts on it, and how many times
-- it reminded; the indexes find the steps waiting that are due. Each
-- message is claimed before it is sent, once per step (or run, 0), person,
-- kind and round of reminders (0 for any other), and records when it went.
-- Each person says which channels they are told on, and whether of runs
-- they started failing.

-- +goose Up
ALTER TABLE run_steps
  ADD COLUMN remind_at timestamp(3) NULL DEFAULT NULL,
  ADD COLUMN expires_at timestamp(3) NULL DEFAULT NULL,
  ADD COLUMN reminded int NOT NULL DEFAULT 0,
  ADD KEY run_steps_remind (status, remind_at),
  ADD KEY run_steps_expires (status, expires_at);

CREATE TABLE notifications (
  id bigint NOT NULL AUTO_INCREMENT,
  run_id bigint NOT NULL,
  step_id bigint NOT NULL DEFAULT 0,
  user_id bigint NOT NULL,
  kind varchar(16) NOT NULL,
  round int NOT NULL DEFAULT 0,
  replica_id char(32) NOT NULL,
  claimed_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  sent_at timestamp(3) NULL DEFAULT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY notifications_once (run_id, step_id, user_id, kind, round),
  KEY notifications_unsent (sent_at),
  CONSTRAINT notifications_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE
);

ALTER TABLE users
  ADD COLUMN notify_email tinyint(1) NOT NULL DEFAULT 1,
  ADD COLUMN notify_slack tinyint(1) NOT NULL DEFAULT 1,
  ADD COLUMN notify_failures tinyint(1) NOT NULL DEFAULT 0;

-- +goose Down
-- What was sent and what people chose go, and reviews waiting are no longer
-- reminded or timed out.
ALTER TABLE users DROP COLUMN notify_email, DROP COLUMN notify_slack, DROP COLUMN notify_failures;
DROP TABLE notifications;
ALTER TABLE run_steps
  DROP KEY run_steps_remind,
  DROP KEY run_steps_expires,
  DROP COLUMN remind_at,
  DROP COLUMN expires_at,
  DROP COLUMN reminded;
