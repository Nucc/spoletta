-- Names unique in the instance are unique in their project: teams, MCP
-- servers, skills and commands, credentials (name and variable), model
-- providers and keys, the default model, limits and internet hosts. A
-- person's roles are the project's too, so one may hold a role in each.

-- +goose Up
ALTER TABLE teams DROP INDEX teams_name, ADD UNIQUE KEY teams_name (project_id, name);
ALTER TABLE mcp_servers DROP INDEX name, ADD UNIQUE KEY name (project_id, name);
ALTER TABLE skills DROP INDEX skills_name, ADD UNIQUE KEY skills_name (project_id, kind, name);
ALTER TABLE credentials DROP INDEX credentials_name, ADD UNIQUE KEY credentials_name (project_id, name),
  DROP INDEX credentials_variable, ADD UNIQUE KEY credentials_variable (project_id, variable);
ALTER TABLE model_providers DROP PRIMARY KEY, ADD PRIMARY KEY (project_id, id);
ALTER TABLE model_keys DROP PRIMARY KEY, ADD PRIMARY KEY (project_id, env);
ALTER TABLE model_default DROP PRIMARY KEY, ADD PRIMARY KEY (project_id, id);
ALTER TABLE limits DROP PRIMARY KEY, ADD PRIMARY KEY (project_id, name);
ALTER TABLE egress_hosts DROP PRIMARY KEY, ADD PRIMARY KEY (project_id, host);
ALTER TABLE user_roles DROP PRIMARY KEY, ADD PRIMARY KEY (user_id, project_id, name);

-- +goose Down
-- Going down loses which project everything was in, so it is refused while
-- a project but Legacy is left: the row inserted fails its check, which
-- says so.
DROP TEMPORARY TABLE IF EXISTS projects_down;
CREATE TEMPORARY TABLE projects_down (others BIGINT NOT NULL,
  CONSTRAINT `delete every project but Legacy before migrating down` CHECK (others = 0));
INSERT INTO projects_down SELECT COUNT(*) FROM projects WHERE id <> 1;
DROP TEMPORARY TABLE projects_down;

ALTER TABLE user_roles DROP PRIMARY KEY, ADD PRIMARY KEY (user_id, name);
ALTER TABLE egress_hosts DROP PRIMARY KEY, ADD PRIMARY KEY (host);
ALTER TABLE limits DROP PRIMARY KEY, ADD PRIMARY KEY (name);
ALTER TABLE model_default DROP PRIMARY KEY, ADD PRIMARY KEY (id);
ALTER TABLE model_keys DROP PRIMARY KEY, ADD PRIMARY KEY (env);
ALTER TABLE model_providers DROP PRIMARY KEY, ADD PRIMARY KEY (id);
ALTER TABLE credentials DROP INDEX credentials_name, ADD UNIQUE KEY credentials_name (name),
  DROP INDEX credentials_variable, ADD UNIQUE KEY credentials_variable (variable);
ALTER TABLE skills DROP INDEX skills_name, ADD UNIQUE KEY skills_name (kind, name);
ALTER TABLE mcp_servers DROP INDEX name, ADD UNIQUE KEY name (name);
ALTER TABLE teams DROP INDEX teams_name, ADD UNIQUE KEY teams_name (name);
