-- The shared record a person started a run on from the struct's Records
-- page, if one did: the run's query links on the record's store read that
-- record alone; see record_runs.go. No foreign key, so a deleted record
-- still tells the run, and a re-run of it, what it was started on.

-- +goose Up
ALTER TABLE runs ADD COLUMN record_id bigint DEFAULT NULL, ADD KEY runs_record (record_id);

-- +goose Down
-- Which record each run was started on is lost; the runs stay.
ALTER TABLE runs DROP KEY runs_record, DROP COLUMN record_id;
