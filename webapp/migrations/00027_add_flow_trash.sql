-- A flow, procedure or struct deleted goes to the trash: when, and by whom.
-- It stays there, out of every list and starting no runs, until someone
-- restores it or deletes it for good, or the trash empties itself of it.
-- See trash.go.

-- +goose Up
ALTER TABLE flows
  ADD COLUMN deleted_at timestamp(3) NULL DEFAULT NULL,
  ADD COLUMN deleted_by bigint DEFAULT NULL,
  ADD KEY flows_deleted (deleted_at);

-- +goose Down
-- What was in the trash comes back among the flows, with its schedules: an
-- older webapp has no trash.
ALTER TABLE flows DROP KEY flows_deleted, DROP COLUMN deleted_by, DROP COLUMN deleted_at;
