-- What a board refers to across its rows is in its project too: a copy's
-- source, the object an object specializes, and what a note points at. Each
-- key names the project beside the id, so the database refuses a row of one
-- project referring to another's.
--
-- MySQL can't null a key naming project_id, which is never null, as what it
-- refers to goes, so these keys take no action: what deletes processes and
-- objects nulls what refers to them first, as the single keys did (see
-- releaseGoing in board_projects.go).
--
-- A struct's store (objects.store_id) and the struct a reference attribute
-- refers to (object_attributes.ref_id) stay without a key: they outlive the
-- struct deleted for good, so that its copies still read the records it
-- kept and references are still checked against them (shared.go). The code
-- keeps them to the project instead.
--
-- Rows referring to another project's, which nothing refused before, are
-- let go of first: a copy becomes an object of its own, as one whose source
-- is deleted does.

-- +goose Up
UPDATE objects o JOIN objects s ON s.id = o.source_id SET o.source_id = NULL WHERE s.project_id <> o.project_id;
UPDATE objects o JOIN objects g ON g.id = o.general_id SET o.general_id = NULL WHERE g.project_id <> o.project_id;
UPDATE notes n JOIN processes p ON p.id = n.process_id SET n.process_id = NULL WHERE p.project_id <> n.project_id;
UPDATE notes n JOIN objects o ON o.id = n.object_id SET n.object_id = NULL WHERE o.project_id <> n.project_id;

-- Checked by the updates above, so MySQL needn't copy the tables whole to
-- check them again.
SET FOREIGN_KEY_CHECKS = 0;

ALTER TABLE objects DROP FOREIGN KEY objects_source, DROP FOREIGN KEY objects_general;
ALTER TABLE objects ADD KEY objects_source_project (source_id, project_id),
  ADD CONSTRAINT objects_source_project FOREIGN KEY (source_id, project_id) REFERENCES objects (id, project_id),
  ADD KEY objects_general_project (general_id, project_id),
  ADD CONSTRAINT objects_general_project FOREIGN KEY (general_id, project_id) REFERENCES objects (id, project_id);
ALTER TABLE notes DROP FOREIGN KEY notes_process, DROP FOREIGN KEY notes_object;
ALTER TABLE notes ADD KEY notes_process_project (process_id, project_id),
  ADD CONSTRAINT notes_process_project FOREIGN KEY (process_id, project_id) REFERENCES processes (id, project_id),
  ADD KEY notes_object_project (object_id, project_id),
  ADD CONSTRAINT notes_object_project FOREIGN KEY (object_id, project_id) REFERENCES objects (id, project_id);

SET FOREIGN_KEY_CHECKS = 1;

-- +goose Down
-- Going down loses which project everything was in, so it is refused while
-- a project but Legacy is left: the row inserted fails its check, which
-- says so.
DROP TEMPORARY TABLE IF EXISTS projects_down;
CREATE TEMPORARY TABLE projects_down (others BIGINT NOT NULL,
  CONSTRAINT `delete every project but Legacy before migrating down` CHECK (others = 0));
INSERT INTO projects_down SELECT COUNT(*) FROM projects WHERE id <> 1;
DROP TEMPORARY TABLE projects_down;

SET FOREIGN_KEY_CHECKS = 0;

ALTER TABLE notes DROP FOREIGN KEY notes_object_project, DROP FOREIGN KEY notes_process_project;
ALTER TABLE notes DROP INDEX notes_object_project, DROP INDEX notes_process_project;
ALTER TABLE notes ADD CONSTRAINT notes_process FOREIGN KEY (process_id) REFERENCES processes (id) ON DELETE SET NULL,
  ADD CONSTRAINT notes_object FOREIGN KEY (object_id) REFERENCES objects (id) ON DELETE SET NULL;
ALTER TABLE objects DROP FOREIGN KEY objects_general_project, DROP FOREIGN KEY objects_source_project;
ALTER TABLE objects DROP INDEX objects_general_project, DROP INDEX objects_source_project;
ALTER TABLE objects ADD CONSTRAINT objects_general FOREIGN KEY (general_id) REFERENCES objects (id) ON DELETE SET NULL,
  ADD CONSTRAINT objects_source FOREIGN KEY (source_id) REFERENCES objects (id) ON DELETE SET NULL;

SET FOREIGN_KEY_CHECKS = 1;
