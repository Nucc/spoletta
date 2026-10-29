-- What a task board column's cards show of each record: the fields picked,
-- and whether its state, when it was last written and the run that last
-- moved it on show; NULL for what they show unless told. See taskboards.go.

-- +goose Up
ALTER TABLE task_board_columns ADD COLUMN card json DEFAULT NULL;

-- +goose Down
-- What the columns' cards show is lost; they show what they do unless told.
ALTER TABLE task_board_columns DROP COLUMN card;
