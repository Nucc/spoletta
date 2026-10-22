-- The audit log: what steps were handed and did with it, and what admins
-- changed, an event each; see audit.go. Kept when a run is deleted, as a
-- record of what happened.

-- +goose Up
CREATE TABLE audit_events (
  id bigint NOT NULL AUTO_INCREMENT,
  at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  user_id bigint DEFAULT NULL,
  run_id bigint DEFAULT NULL,
  step_id bigint DEFAULT NULL,
  kind varchar(64) NOT NULL,
  detail text NOT NULL,
  PRIMARY KEY (id),
  KEY audit_events_run (run_id),
  KEY audit_events_kind (kind, at)
);

-- +goose Down
-- The audit log goes.
DROP TABLE audit_events;
