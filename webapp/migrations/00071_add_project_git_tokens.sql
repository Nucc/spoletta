-- A git token a project's owners set for its steps' repositories, with the
-- username it goes with, sealed with SECRET_KEY (secrets.go) and never sent
-- back; see git_tokens.go. A project without one, and without
-- inherits_instance, gives its steps none.

-- +goose Up
CREATE TABLE project_git_tokens (
  project_id bigint NOT NULL,
  username varchar(255) NOT NULL,
  token text NOT NULL,
  updated_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (project_id),
  CONSTRAINT project_git_tokens_project FOREIGN KEY (project_id) REFERENCES projects (id)
);

-- +goose Down
-- Going down loses which project everything was in, so it is refused while
-- a project but Legacy is left: the row inserted fails its check, which
-- says so.
DROP TEMPORARY TABLE IF EXISTS projects_down;
CREATE TEMPORARY TABLE projects_down (others BIGINT NOT NULL,
  CONSTRAINT `delete every project but Legacy before migrating down` CHECK (others = 0));
INSERT INTO projects_down SELECT COUNT(*) FROM projects WHERE id <> 1;
DROP TEMPORARY TABLE projects_down;

-- Legacy's git token set in its settings goes; its steps get GIT_TOKEN and
-- the GitHub App's tokens as before.
DROP TABLE project_git_tokens;
