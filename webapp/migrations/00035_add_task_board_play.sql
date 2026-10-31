-- Playing a task board: each column may name the flow that moves its
-- records on, and a board playing starts that flow on each record its
-- columns hold, a run per record, as many at once as it allows, for
-- whoever pressed Play. The runs it started, with the state each record was
-- in, tell a record its flow left where it was (stuck). A replica claims
-- each turn of a board by moving its dispatched_at on, and a board that
-- stopped playing by itself says why (stopped). See taskboard_play.go.

-- +goose Up
ALTER TABLE task_boards
  ADD COLUMN playing boolean NOT NULL DEFAULT FALSE,
  ADD COLUMN played_by bigint DEFAULT NULL,
  ADD COLUMN max_runs int NOT NULL DEFAULT 2,
  ADD COLUMN dispatched_at timestamp(3) NULL DEFAULT NULL,
  ADD COLUMN stopped varchar(1024) NOT NULL DEFAULT '',
  ADD CONSTRAINT task_boards_played_by FOREIGN KEY (played_by) REFERENCES users (id);

-- A column's flow is NULL once the flow is deleted for good; its max_runs is
-- NULL for the board's own limit, 0 for its records resting.
ALTER TABLE task_board_columns
  ADD COLUMN flow_id bigint DEFAULT NULL,
  ADD COLUMN max_runs int DEFAULT NULL,
  ADD KEY task_board_columns_flow (flow_id),
  ADD CONSTRAINT task_board_columns_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE SET NULL;

-- No foreign key on the record, as on runs.record_id, so a deleted record
-- still tells what the run was started on. retried_at is when a person
-- asked for the record to go again after the run.
CREATE TABLE task_board_runs (
  run_id bigint NOT NULL,
  board_id bigint NOT NULL,
  record_id bigint NOT NULL,
  state varchar(255) NOT NULL,
  retried_at timestamp(3) NULL DEFAULT NULL,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (run_id),
  KEY task_board_runs_record (board_id, record_id, run_id),
  CONSTRAINT task_board_runs_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE,
  CONSTRAINT task_board_runs_board FOREIGN KEY (board_id) REFERENCES task_boards (id) ON DELETE CASCADE
);

-- +goose Down
-- Which boards play, their columns' flows and limits, and which runs boards
-- started are lost; the runs stay.
DROP TABLE task_board_runs;
ALTER TABLE task_board_columns
  DROP FOREIGN KEY task_board_columns_flow,
  DROP KEY task_board_columns_flow,
  DROP COLUMN max_runs,
  DROP COLUMN flow_id;
ALTER TABLE task_boards
  DROP FOREIGN KEY task_boards_played_by,
  DROP KEY task_boards_played_by,
  DROP COLUMN stopped,
  DROP COLUMN dispatched_at,
  DROP COLUMN max_runs,
  DROP COLUMN played_by,
  DROP COLUMN playing;
