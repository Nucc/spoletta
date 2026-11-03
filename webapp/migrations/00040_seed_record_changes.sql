-- Each record there is starts its history as it stands now, found so: its
-- fields and state, the run and step that last moved it on, or the person
-- who corrected it since, when it was last written. What changes from here
-- on is told apart from it. Kept apart from the table it fills, as MySQL
-- commits the table at once and a failed fill would leave it made but the
-- migration not recorded. See record_changes.go.

-- +goose Up
INSERT INTO record_changes (record_id, content, state, kind, run_id, top_run_id, step_id, process_name, step_kind, user_id, changed_at)
  SELECT sr.id, sr.content, sr.state, 'found', sr.run_id, sr.run_id, sr.step_id, COALESCE(s.process_name, ''), COALESCE(s.kind, ''),
    IF(sr.edited_at IS NULL, NULL, sr.edited_by), sr.updated_at
  FROM shared_records sr LEFT JOIN run_steps s ON s.id = sr.step_id;

-- +goose Down
-- How each record stood when its history began is lost; what changed since
-- stays.
DELETE FROM record_changes WHERE kind = 'found';
