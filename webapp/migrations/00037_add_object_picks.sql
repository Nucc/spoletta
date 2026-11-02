-- An object picking a shared record: each run of the flow works on one
-- record of the struct's store, the one it was started on or else the one
-- the pick selects, which it holds while it goes, so no other run picks it
-- too. The run's records, by the object picking each, say which record its
-- steps write when they change the object, and the state the run last left
-- it in, which tells a move someone else made meanwhile. See picks.go.

-- +goose Up
ALTER TABLE objects ADD COLUMN pick json DEFAULT NULL;

-- No foreign key on the record, as on runs.record_id, so a deleted record
-- still tells what the run worked on.
CREATE TABLE taken_records (
  run_id bigint NOT NULL,
  object_ref bigint NOT NULL,
  record_id bigint NOT NULL,
  state varchar(255) NOT NULL DEFAULT '',
  PRIMARY KEY (run_id, object_ref),
  KEY taken_records_record (record_id, run_id),
  CONSTRAINT taken_records_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE
);

-- +goose Down
-- Which records objects pick, and which records runs held, are lost; the
-- runs and the records stay.
DROP TABLE taken_records;
ALTER TABLE objects DROP COLUMN pick;
