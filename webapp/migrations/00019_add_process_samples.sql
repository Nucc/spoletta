-- The inputs saved to try a process on, each set under a name of its own;
-- see trials.go. inputs holds, for each object the process takes, the
-- object's and its state's ids, and what it holds. They are part of the
-- board, so versions keep them as they keep the rest.

-- +goose Up
CREATE TABLE process_samples (
  id bigint NOT NULL AUTO_INCREMENT,
  process_id bigint NOT NULL,
  name varchar(255) NOT NULL,
  inputs json NOT NULL,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY process_samples_name (process_id, name),
  CONSTRAINT process_samples_process FOREIGN KEY (process_id) REFERENCES processes (id) ON DELETE CASCADE
);

-- +goose Down
-- The samples saved are lost; the versions saved since keep theirs, which
-- an older webapp doesn't read.
DROP TABLE process_samples;
