-- What a replica asks of the replica owning a run, an assistant's reply or
-- a follow-up, such as stopping it, which the owner takes and does; see
-- commands.go.

-- +goose Up
CREATE TABLE replica_commands (
  id bigint NOT NULL AUTO_INCREMENT,
  replica_id char(32) NOT NULL,
  kind varchar(32) NOT NULL,
  target_id bigint NOT NULL,
  step_id bigint NOT NULL DEFAULT 0,
  payload json DEFAULT NULL,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY replica_commands_replica (replica_id, id)
);

-- +goose Down
-- Commands not yet taken go undone.
DROP TABLE replica_commands;
