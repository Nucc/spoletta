-- The replicas of the webapp running on the database, each saying it is
-- alive, and the replica owning each run, assistant's reply, follow-up and
-- proxy token, so that one starting, or one stopping, ends only what
-- replicas that stopped left; see replicas.go. What is there already has no
-- owner, as left by a replica that stopped.

-- +goose Up
CREATE TABLE replicas (
  id char(32) NOT NULL,
  host varchar(255) NOT NULL,
  started_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  seen_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id)
);
ALTER TABLE runs ADD COLUMN replica_id char(32) DEFAULT NULL, ADD KEY runs_replica (replica_id);
ALTER TABLE assistant_messages ADD COLUMN replica_id char(32) DEFAULT NULL;
ALTER TABLE run_step_followups ADD COLUMN replica_id char(32) DEFAULT NULL;
ALTER TABLE proxy_grants ADD COLUMN replica_id char(32) DEFAULT NULL, ADD KEY proxy_grants_replica (replica_id);

-- +goose Down
-- Which replica owns what goes; an older webapp ends everything running as
-- it starts.
ALTER TABLE proxy_grants DROP KEY proxy_grants_replica, DROP COLUMN replica_id;
ALTER TABLE run_step_followups DROP COLUMN replica_id;
ALTER TABLE assistant_messages DROP COLUMN replica_id;
ALTER TABLE runs DROP KEY runs_replica, DROP COLUMN replica_id;
DROP TABLE replicas;
