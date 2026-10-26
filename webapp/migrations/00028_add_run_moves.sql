-- A run a stopped replica walked is taken over by one alive, which rebuilds
-- it from what it stored and carries it on (see resume.go). That needs a
-- little more kept as it goes: whether it took the version its flow
-- published, so the subflows it starts later take theirs; what it carried
-- in of its persistent objects, as the latest run to end done may be
-- another by then; a person's decision of a review as they made it, until
-- the walk has recorded what follows from it, so a decision made while no
-- replica walked the run isn't lost; and the instructions an agent left
-- with each object it decided on. Each time a run moves, it keeps from
-- which replica to which, and the steps it lost then, tried again or not.

-- +goose Up
ALTER TABLE runs
  ADD COLUMN published boolean NOT NULL DEFAULT FALSE,
  ADD COLUMN carried json DEFAULT NULL;
ALTER TABLE run_steps ADD COLUMN decision json DEFAULT NULL;
ALTER TABLE run_objects ADD COLUMN instructions text DEFAULT NULL;

CREATE TABLE run_moves (
  id bigint NOT NULL AUTO_INCREMENT,
  run_id bigint NOT NULL,
  from_replica char(32) DEFAULT NULL,
  to_replica char(32) NOT NULL,
  host varchar(255) NOT NULL DEFAULT '',
  lost json DEFAULT NULL,
  moved_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY run_moves_run (run_id),
  CONSTRAINT run_moves_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE
);

-- +goose Down
-- When runs moved is lost, as are the decisions and instructions kept for
-- rebuilding them; an older webapp ends the runs a stopped replica left.
DROP TABLE run_moves;
ALTER TABLE run_objects DROP COLUMN instructions;
ALTER TABLE run_steps DROP COLUMN decision;
ALTER TABLE runs DROP COLUMN carried, DROP COLUMN published;
