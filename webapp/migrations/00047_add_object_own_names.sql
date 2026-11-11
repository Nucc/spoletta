-- Whether a struct's copy on a board is named for itself, such as Gemini
-- Review for a copy of Review Comment, rather than following its struct's
-- name, so a process can take several copies of one struct told apart by
-- name. See syncCopy in modules.go.

-- +goose Up
ALTER TABLE objects
  ADD COLUMN own_name boolean NOT NULL DEFAULT FALSE;

-- +goose Down
-- Which copies were named for themselves is lost: they keep the names they
-- have until their struct changes, which names them after it again.
ALTER TABLE objects
  DROP COLUMN own_name;
