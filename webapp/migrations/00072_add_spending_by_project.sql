-- What a project's models cost is read by month, for the project's
-- Spending view, and by person and month, for each person's monthly limit
-- in it (spend.go), so llm_usage is indexed both ways.

-- +goose Up
ALTER TABLE llm_usage ADD KEY llm_usage_project_at (project_id, at),
  ADD KEY llm_usage_user_project (user_id, project_id, at);

-- +goose Down
-- Going down loses which project everything was in, so it is refused while
-- a project but Legacy is left: the row inserted fails its check, which
-- says so.
DROP TEMPORARY TABLE IF EXISTS projects_down;
CREATE TEMPORARY TABLE projects_down (others BIGINT NOT NULL,
  CONSTRAINT `delete every project but Legacy before migrating down` CHECK (others = 0));
INSERT INTO projects_down SELECT COUNT(*) FROM projects WHERE id <> 1;
DROP TEMPORARY TABLE projects_down;

ALTER TABLE llm_usage DROP KEY llm_usage_user_project, DROP KEY llm_usage_project_at;
