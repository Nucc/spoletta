-- A task board's column may try its records again on its own: a record
-- whose run failed, or ended leaving it where it was, such as a pull
-- request whose checks are still going, is started again retry_minutes
-- after that run ended, at most retry_times times in a row, before it is
-- stuck. Both NULL for none, the record stuck at once. See
-- taskboard_play.go.

-- +goose Up
ALTER TABLE task_board_columns
  ADD COLUMN retry_minutes int DEFAULT NULL,
  ADD COLUMN retry_times int DEFAULT NULL;

-- +goose Down
-- How often the columns tried their records again is lost; their records
-- are stuck at once again.
ALTER TABLE task_board_columns
  DROP COLUMN retry_times,
  DROP COLUMN retry_minutes;
