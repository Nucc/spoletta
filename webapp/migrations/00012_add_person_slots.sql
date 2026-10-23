-- A row for each agent, script, Sentry and connector step of a person's
-- runs going, at whichever replica, so the steps a person may have going
-- at once are counted across replicas; see engine.go's personSlot.

-- +goose Up
CREATE TABLE person_slots (
  id bigint NOT NULL AUTO_INCREMENT,
  user_id bigint NOT NULL,
  run_id bigint NOT NULL,
  replica_id char(32) NOT NULL,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY person_slots_user (user_id),
  KEY person_slots_replica (replica_id)
);

-- +goose Down
-- The steps going are no longer counted; an older webapp counts its own.
DROP TABLE person_slots;
