-- Projects are tenants: standalone spaces of flows, task boards, settings
-- and people; see projects.go. Who may enter one is its members, assigned
-- by hand or come in through a group or what it lets anyone do, the groups
-- the identity provider puts people in, and its others_access. What happens
-- above projects, such as one made or deleted, is in instance_events.
--
-- Project 1 is Legacy, which everything there is belongs to, inheriting
-- what the instance is configured with outside the database. Everyone there
-- is becomes its member, admins as owners and others as editors; someone
-- removed too, soft-removed, so what they still own has a member to point at.

-- +goose Up
CREATE TABLE projects (
  id BIGINT NOT NULL AUTO_INCREMENT,
  name VARCHAR(255) NOT NULL,
  inherits_instance TINYINT(1) NOT NULL DEFAULT 0,
  others_access VARCHAR(8) NOT NULL DEFAULT 'none',
  created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  archived_at TIMESTAMP(3) NULL DEFAULT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY projects_name (name)
);
-- source is how the row came: manual for someone assigned by hand, else
-- group, open or super for someone who entered that way.
CREATE TABLE project_members (
  project_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  level VARCHAR(8) NOT NULL,
  source VARCHAR(8) NOT NULL,
  created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  removed_at TIMESTAMP(3) NULL DEFAULT NULL,
  PRIMARY KEY (project_id, user_id),
  UNIQUE KEY project_members_user (user_id, project_id),
  CONSTRAINT project_members_project FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE,
  CONSTRAINT project_members_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);
-- Group names are the identity provider's, matched exactly.
CREATE TABLE project_groups (
  project_id BIGINT NOT NULL,
  group_name VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  level VARCHAR(8) NOT NULL,
  created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (project_id, group_name),
  KEY project_groups_group (group_name),
  CONSTRAINT project_groups_project FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE
);
CREATE TABLE user_groups (
  user_id BIGINT NOT NULL,
  group_name VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  PRIMARY KEY (user_id, group_name),
  KEY user_groups_group (group_name),
  CONSTRAINT user_groups_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);
CREATE TABLE instance_events (
  id BIGINT NOT NULL AUTO_INCREMENT,
  at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  user_id BIGINT DEFAULT NULL,
  kind VARCHAR(64) NOT NULL,
  detail TEXT NOT NULL,
  PRIMARY KEY (id),
  KEY instance_events_kind (kind, at)
);

INSERT INTO projects (id, name, inherits_instance) VALUES (1, 'Legacy', TRUE);
INSERT INTO project_members (project_id, user_id, level, source, removed_at)
  SELECT 1, id, IF(admin, 'owner', 'editor'), 'manual', removed_at FROM users;

-- +goose Down
-- Going down loses which project everything was in, so it is refused while
-- a project but Legacy is left: the row inserted fails its check, which
-- says so.
DROP TEMPORARY TABLE IF EXISTS projects_down;
CREATE TEMPORARY TABLE projects_down (others BIGINT NOT NULL,
  CONSTRAINT `delete every project but Legacy before migrating down` CHECK (others = 0));
INSERT INTO projects_down SELECT COUNT(*) FROM projects WHERE id <> 1;
DROP TEMPORARY TABLE projects_down;

-- Who was in Legacy, the groups people are in, and what happened above
-- projects go.
DROP TABLE instance_events;
DROP TABLE user_groups;
DROP TABLE project_groups;
DROP TABLE project_members;
DROP TABLE projects;
