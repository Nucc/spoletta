-- Who has a flow's board open now, each browser tab showing it a row that
-- says so every few seconds, and the changes made to boards lately, by
-- whom and from which tab, so that the others showing a board load it
-- again; see presence.go. Both are kept in the database so that every
-- replica answers alike, and neither lasts: a row of a tab not heard from
-- is gone in a minute, and a change in an hour.

-- +goose Up
CREATE TABLE board_viewers (
  flow_id bigint NOT NULL,
  user_id bigint NOT NULL,
  client varchar(64) NOT NULL,
  seen_at timestamp(3) NOT NULL,
  PRIMARY KEY (flow_id, user_id, client),
  KEY board_viewers_seen (seen_at),
  CONSTRAINT board_viewers_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE,
  CONSTRAINT board_viewers_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);
CREATE TABLE board_changes (
  id bigint NOT NULL AUTO_INCREMENT,
  flow_id bigint NOT NULL,
  user_id bigint DEFAULT NULL,
  client varchar(64) NOT NULL DEFAULT '',
  changed_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY board_changes_flow (flow_id, id),
  KEY board_changes_by (flow_id, user_id, changed_at),
  CONSTRAINT board_changes_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE,
  CONSTRAINT board_changes_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE SET NULL
);

-- +goose Down
-- Who has boards open and what changed them lately is lost, which lasts
-- only a while anyway.
DROP TABLE board_changes;
DROP TABLE board_viewers;
