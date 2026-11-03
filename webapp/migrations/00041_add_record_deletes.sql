-- Deleting a shared record keeps it, with its history: it is marked deleted,
-- when and by whom, and every flow, query, pick, task board and the Records
-- page leave it out from then on. Its unique values are freed as it goes, so
-- a flow writing one of them again adds a new record. See records.go.

-- +goose Up
ALTER TABLE shared_records
  ADD COLUMN deleted_at timestamp(3) NULL DEFAULT NULL,
  ADD COLUMN deleted_by bigint DEFAULT NULL;

-- +goose Down
-- The records deleted since are deleted for good, with their histories, as
-- an older webapp would otherwise show them again.
DELETE FROM shared_records WHERE deleted_at IS NOT NULL;
ALTER TABLE shared_records
  DROP COLUMN deleted_by,
  DROP COLUMN deleted_at;
