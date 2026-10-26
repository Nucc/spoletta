-- The roles each version's human objects name, as their names lowercased
-- and trimmed, so that a person holding one sees the flow while that version
-- is published, or a run of it waits on a person, though the board as it is
-- dropped the object; see visibleFlows in owners.go. Kept as each version is
-- saved, and worked out here for the versions saved before.

-- +goose Up
CREATE TABLE version_roles (
  version_id bigint NOT NULL,
  role varchar(255) NOT NULL,
  PRIMARY KEY (version_id, role),
  CONSTRAINT version_roles_version FOREIGN KEY (version_id) REFERENCES flow_versions (id) ON DELETE CASCADE
);
INSERT IGNORE INTO version_roles (version_id, role)
SELECT v.id, LOWER(TRIM(o.name)) FROM flow_versions v,
  JSON_TABLE(v.snapshot, '$.objects[*]' COLUMNS (name varchar(255) PATH '$.name', human tinyint PATH '$.human' DEFAULT '0' ON EMPTY)) o
WHERE o.human;

-- +goose Down
-- Nothing is lost: the versions keep their human objects, which going up
-- again works the roles out from.
DROP TABLE version_roles;
