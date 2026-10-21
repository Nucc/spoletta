-- What happened in each run, a line per row, as its Logs window shows it:
-- the webapp's own lines about the run and its steps, and those the worker
-- sent of the steps it ran; see logs.go.

-- +goose Up
CREATE TABLE run_logs (
  id bigint NOT NULL AUTO_INCREMENT,
  run_id bigint NOT NULL,
  step_id bigint DEFAULT NULL,
  source varchar(16) NOT NULL,
  level varchar(8) NOT NULL,
  message mediumtext NOT NULL,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY run_logs_run (run_id, id),
  CONSTRAINT run_logs_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE,
  CONSTRAINT run_logs_step FOREIGN KEY (step_id) REFERENCES run_steps (id) ON DELETE CASCADE
);

-- +goose Down
-- The runs' logs go.
DROP TABLE run_logs;
