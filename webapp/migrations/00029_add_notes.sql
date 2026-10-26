-- Notes on a board: boxes of Markdown text people place anywhere on a flow's
-- top board or a flow process's, sized and coloured, each pointing at a
-- process or an object, or at nothing; see notes.go. They are part of the
-- board, so versions and history keep them as they keep the rest, but no
-- run reads them.

-- +goose Up
CREATE TABLE notes (
  id bigint NOT NULL AUTO_INCREMENT,
  flow_id bigint NOT NULL,
  parent_id bigint DEFAULT NULL,
  x double NOT NULL DEFAULT 0,
  y double NOT NULL DEFAULT 0,
  width double NOT NULL,
  height double NOT NULL,
  text text NOT NULL,
  colour varchar(16) NOT NULL,
  process_id bigint DEFAULT NULL,
  object_id bigint DEFAULT NULL,
  PRIMARY KEY (id),
  KEY notes_flow (flow_id),
  CONSTRAINT notes_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE,
  CONSTRAINT notes_parent FOREIGN KEY (parent_id) REFERENCES processes (id) ON DELETE CASCADE,
  CONSTRAINT notes_process FOREIGN KEY (process_id) REFERENCES processes (id) ON DELETE SET NULL,
  CONSTRAINT notes_object FOREIGN KEY (object_id) REFERENCES objects (id) ON DELETE SET NULL
);

-- +goose Down
-- The notes on boards are lost; the versions saved since keep theirs, which
-- an older webapp doesn't read.
DROP TABLE notes;
