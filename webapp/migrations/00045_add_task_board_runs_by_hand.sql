-- A run a person starts on a record from a task board's card is one of the
-- board's runs on it, by_hand: the board counts the record's rounds and its
-- column's tries anew from it, as from a person trying the record again, and
-- its log says a person started it there rather than the board playing. See
-- taskboard_play.go.

-- +goose Up
ALTER TABLE task_board_runs
  ADD COLUMN by_hand boolean NOT NULL DEFAULT FALSE;

-- +goose Down
-- Which of the boards' runs people started from their cards is lost; they
-- read as the boards' own, and their records' tries count on from before.
ALTER TABLE task_board_runs
  DROP COLUMN by_hand;
