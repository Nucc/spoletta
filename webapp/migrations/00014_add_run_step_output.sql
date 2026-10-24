-- What a script step printed, stdout and stderr in the order they came, a
-- chunk at a time as the worker streams it, for a person to follow it as in
-- a terminal; see script_output.go.

-- +goose Up
CREATE TABLE run_step_output (
  id bigint NOT NULL AUTO_INCREMENT,
  step_id bigint NOT NULL,
  stream varchar(8) NOT NULL,
  text mediumtext NOT NULL,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY step_id (step_id),
  CONSTRAINT run_step_output_step FOREIGN KEY (step_id) REFERENCES run_steps (id) ON DELETE CASCADE
);

-- +goose Down
-- What scripts printed as they ran is lost; their output and log stay.
DROP TABLE run_step_output;
