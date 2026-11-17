-- The records a run took, a task board's runs were on, and a run was
-- started on are the run's project's: each refers to its record by a key
-- naming the project too, as 00053's keys do, so the database refuses a
-- run of one project holding another's record. A record is never deleted
-- but with its project (deleteRecords only marks it deleted, and a struct
-- deleted for good leaves its records, shared.go), so a key taking no
-- action as it goes holds; deleting a project empties it with keys
-- unchecked (projects.go).
--
-- The other references among runs, steps and records keep none: a run
-- inside another (parent_run_id) and a subflow step's run (child_run_id)
-- are of different flows, which are deleted for good apart, and a run
-- inside another may be deleted before the step naming it (engine.go); a
-- re-run (rerun_of) goes in the same statement as the run it re-ran,
-- which a key on the table itself would refuse in some order; trial_of is
-- a process, kept after the process is deleted; and a record outlives its
-- struct's object (shared_records.object_id). The code keeps them to
-- their project (run_projects.go).

-- +goose Up
-- What names a record of another project, as only a row moved between
-- projects by hand can, names none: a run started on it was started on
-- none, and what a run took of it, or a board's run on it, goes. One
-- naming a record there is none of, deleted before records were kept
-- deleted (00041), goes too, as the key would refuse it.
UPDATE runs x LEFT JOIN shared_records r ON r.id = x.record_id SET x.record_id = NULL
  WHERE x.record_id IS NOT NULL AND (r.id IS NULL OR r.project_id <> x.project_id);
DELETE t FROM taken_records t LEFT JOIN shared_records r ON r.id = t.record_id
  WHERE r.id IS NULL OR r.project_id <> t.project_id;
DELETE t FROM task_board_runs t LEFT JOIN shared_records r ON r.id = t.record_id
  WHERE r.id IS NULL OR r.project_id <> t.project_id;

ALTER TABLE taken_records ADD KEY taken_records_record_project (record_id, project_id),
  ADD CONSTRAINT taken_records_record_project FOREIGN KEY (record_id, project_id) REFERENCES shared_records (id, project_id);
ALTER TABLE task_board_runs ADD KEY task_board_runs_record_project (record_id, project_id),
  ADD CONSTRAINT task_board_runs_record_project FOREIGN KEY (record_id, project_id) REFERENCES shared_records (id, project_id);
ALTER TABLE runs ADD KEY runs_record_project (record_id, project_id),
  ADD CONSTRAINT runs_record_project FOREIGN KEY (record_id, project_id) REFERENCES shared_records (id, project_id);

-- +goose Down
-- As every migration since projects came in, going down is refused while a
-- project but Legacy is left, as going further down would lose which
-- project everything was in: the row inserted fails its check, which says
-- so.
DROP TEMPORARY TABLE IF EXISTS projects_down;
CREATE TEMPORARY TABLE projects_down (others BIGINT NOT NULL,
  CONSTRAINT `delete every project but Legacy before migrating down` CHECK (others = 0));
INSERT INTO projects_down SELECT COUNT(*) FROM projects WHERE id <> 1;
DROP TEMPORARY TABLE projects_down;

ALTER TABLE runs DROP FOREIGN KEY runs_record_project, DROP INDEX runs_record_project;
ALTER TABLE task_board_runs DROP FOREIGN KEY task_board_runs_record_project, DROP INDEX task_board_runs_record_project;
ALTER TABLE taken_records DROP FOREIGN KEY taken_records_record_project, DROP INDEX taken_records_record_project;
