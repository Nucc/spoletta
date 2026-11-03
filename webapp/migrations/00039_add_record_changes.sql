-- Each shared record's history: a row per change of it, by a run's step or
-- a person by hand, holding the record as it stood after the change, its
-- fields and state, so what changed is the difference from the row before
-- and the record at any point is the row itself. A step's run, the run at
-- its top and the step have no foreign keys, so a deleted run still tells
-- which it was, as audit_events does; the process's name and kind are kept
-- for the same reason. The rows go with their record. See record_changes.go.

-- +goose Up
CREATE TABLE record_changes (
  id bigint NOT NULL AUTO_INCREMENT,
  record_id bigint NOT NULL,
  content longtext NOT NULL,
  state varchar(255) NOT NULL DEFAULT '',
  kind varchar(16) NOT NULL,
  run_id bigint DEFAULT NULL,
  top_run_id bigint DEFAULT NULL,
  step_id bigint DEFAULT NULL,
  process_name varchar(255) NOT NULL DEFAULT '',
  step_kind varchar(16) NOT NULL DEFAULT '',
  user_id bigint DEFAULT NULL,
  changed_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY record_changes_record (record_id, id),
  CONSTRAINT record_changes_record FOREIGN KEY (record_id) REFERENCES shared_records (id) ON DELETE CASCADE
);

-- +goose Down
-- The records' histories are lost; the records stay.
DROP TABLE record_changes;
