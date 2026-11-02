-- A board's start can point at several processes, which a run begins with
-- all at once: a row per process the start of its board points at, with
-- where its link runs if moved by hand. The board is the process's own, so
-- the rows go with their processes, into a flow process wrapping them too.
-- They replace the one target and route the start of a top board and of a
-- flow process's board kept. See flow.go.

-- +goose Up
CREATE TABLE start_targets (
  process_id bigint NOT NULL,
  route json DEFAULT NULL,
  PRIMARY KEY (process_id),
  CONSTRAINT start_targets_process FOREIGN KEY (process_id) REFERENCES processes (id) ON DELETE CASCADE
);
INSERT INTO start_targets (process_id, route)
  SELECT target_id, route FROM flow_starts WHERE target_id IS NOT NULL;
INSERT IGNORE INTO start_targets (process_id, route)
  SELECT target_id, route FROM inner_boards WHERE target_id IS NOT NULL;
ALTER TABLE flow_starts
  DROP FOREIGN KEY flow_starts_ibfk_2,
  DROP KEY target_id,
  DROP COLUMN target_id,
  DROP COLUMN route;
ALTER TABLE inner_boards
  DROP FOREIGN KEY inner_boards_ibfk_2,
  DROP KEY target_id,
  DROP COLUMN target_id,
  DROP COLUMN route;

-- +goose Down
-- Each start keeps only the first process it points at, by id, with its
-- route; the others it pointed at are lost.
ALTER TABLE flow_starts
  ADD COLUMN target_id bigint DEFAULT NULL AFTER y,
  ADD COLUMN route json DEFAULT NULL AFTER target_id,
  ADD KEY target_id (target_id),
  ADD CONSTRAINT flow_starts_ibfk_2 FOREIGN KEY (target_id) REFERENCES processes (id) ON DELETE SET NULL;
ALTER TABLE inner_boards
  ADD COLUMN target_id bigint DEFAULT NULL AFTER y,
  ADD COLUMN route json DEFAULT NULL AFTER target_id,
  ADD KEY target_id (target_id),
  ADD CONSTRAINT inner_boards_ibfk_2 FOREIGN KEY (target_id) REFERENCES processes (id) ON DELETE SET NULL;
UPDATE flow_starts s
  JOIN (SELECT p.flow_id, MIN(t.process_id) AS first FROM start_targets t JOIN processes p ON p.id = t.process_id
    WHERE p.parent_id IS NULL GROUP BY p.flow_id) f ON f.flow_id = s.flow_id
  JOIN start_targets t ON t.process_id = f.first
  SET s.target_id = t.process_id, s.route = t.route;
UPDATE inner_boards b
  JOIN (SELECT p.parent_id, MIN(t.process_id) AS first FROM start_targets t JOIN processes p ON p.id = t.process_id
    WHERE p.parent_id IS NOT NULL GROUP BY p.parent_id) f ON f.parent_id = b.process_id
  JOIN start_targets t ON t.process_id = f.first
  SET b.target_id = t.process_id, b.route = t.route;
DROP TABLE start_targets;
