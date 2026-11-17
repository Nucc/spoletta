-- Everything below the projects is a project's: every table names its
-- project, but those above them (projects, their members and groups,
-- users, their identities, groups and sessions, instance_events, replicas
-- and their commands). It is 1, Legacy, for what there is.
--
-- The default is the transition's: rows inserted without naming their
-- project are Legacy's, which keeps them working while Legacy is the only
-- project, and fails on the composite keys (00053) in another. A later
-- migration drops the defaults once every insert names its project.

-- +goose Up
ALTER TABLE api_keys ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE assistant_messages ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE audit_events ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE board_changes ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE board_viewers ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE connections ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE credential_teams ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE credentials ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE egress_hosts ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE favourite_flows ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE flow_history ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE flow_starts ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE flow_tags ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE flow_versions ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE flows ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE inner_boards ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE instance_keys ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE limits ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE link_states ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE llm_usage ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE mcp_servers ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE mcp_sign_ins ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE mcp_tokens ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE model_default ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE model_keys ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE model_providers ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE notes ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE notifications ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE object_attributes ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE object_instances ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE object_links ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE object_states ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE objects ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE person_slots ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE process_samples ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE processes ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE proxy_grants ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE record_changes ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE review_comments ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE run_logs ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE run_moves ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE run_objects ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE run_step_followups ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE run_step_messages ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE run_step_output ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE run_steps ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE runs ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE schedules ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE shared_keys ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE shared_records ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE skills ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE start_targets ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE taken_records ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE task_board_columns ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE task_board_runs ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE task_boards ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE team_members ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE teams ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE user_roles ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE version_roles ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE webhook_deliveries ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE webhook_digests ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE webhooks ADD COLUMN project_id BIGINT NOT NULL DEFAULT 1;

-- +goose Down
-- Going down loses which project everything was in, so it is refused while
-- a project but Legacy is left: the row inserted fails its check, which
-- says so.
DROP TEMPORARY TABLE IF EXISTS projects_down;
CREATE TEMPORARY TABLE projects_down (others BIGINT NOT NULL,
  CONSTRAINT `delete every project but Legacy before migrating down` CHECK (others = 0));
INSERT INTO projects_down SELECT COUNT(*) FROM projects WHERE id <> 1;
DROP TEMPORARY TABLE projects_down;

ALTER TABLE api_keys DROP COLUMN project_id;
ALTER TABLE assistant_messages DROP COLUMN project_id;
ALTER TABLE audit_events DROP COLUMN project_id;
ALTER TABLE board_changes DROP COLUMN project_id;
ALTER TABLE board_viewers DROP COLUMN project_id;
ALTER TABLE connections DROP COLUMN project_id;
ALTER TABLE credential_teams DROP COLUMN project_id;
ALTER TABLE credentials DROP COLUMN project_id;
ALTER TABLE egress_hosts DROP COLUMN project_id;
ALTER TABLE favourite_flows DROP COLUMN project_id;
ALTER TABLE flow_history DROP COLUMN project_id;
ALTER TABLE flow_starts DROP COLUMN project_id;
ALTER TABLE flow_tags DROP COLUMN project_id;
ALTER TABLE flow_versions DROP COLUMN project_id;
ALTER TABLE flows DROP COLUMN project_id;
ALTER TABLE inner_boards DROP COLUMN project_id;
ALTER TABLE instance_keys DROP COLUMN project_id;
ALTER TABLE limits DROP COLUMN project_id;
ALTER TABLE link_states DROP COLUMN project_id;
ALTER TABLE llm_usage DROP COLUMN project_id;
ALTER TABLE mcp_servers DROP COLUMN project_id;
ALTER TABLE mcp_sign_ins DROP COLUMN project_id;
ALTER TABLE mcp_tokens DROP COLUMN project_id;
ALTER TABLE model_default DROP COLUMN project_id;
ALTER TABLE model_keys DROP COLUMN project_id;
ALTER TABLE model_providers DROP COLUMN project_id;
ALTER TABLE notes DROP COLUMN project_id;
ALTER TABLE notifications DROP COLUMN project_id;
ALTER TABLE object_attributes DROP COLUMN project_id;
ALTER TABLE object_instances DROP COLUMN project_id;
ALTER TABLE object_links DROP COLUMN project_id;
ALTER TABLE object_states DROP COLUMN project_id;
ALTER TABLE objects DROP COLUMN project_id;
ALTER TABLE person_slots DROP COLUMN project_id;
ALTER TABLE process_samples DROP COLUMN project_id;
ALTER TABLE processes DROP COLUMN project_id;
ALTER TABLE proxy_grants DROP COLUMN project_id;
ALTER TABLE record_changes DROP COLUMN project_id;
ALTER TABLE review_comments DROP COLUMN project_id;
ALTER TABLE run_logs DROP COLUMN project_id;
ALTER TABLE run_moves DROP COLUMN project_id;
ALTER TABLE run_objects DROP COLUMN project_id;
ALTER TABLE run_step_followups DROP COLUMN project_id;
ALTER TABLE run_step_messages DROP COLUMN project_id;
ALTER TABLE run_step_output DROP COLUMN project_id;
ALTER TABLE run_steps DROP COLUMN project_id;
ALTER TABLE runs DROP COLUMN project_id;
ALTER TABLE schedules DROP COLUMN project_id;
ALTER TABLE shared_keys DROP COLUMN project_id;
ALTER TABLE shared_records DROP COLUMN project_id;
ALTER TABLE skills DROP COLUMN project_id;
ALTER TABLE start_targets DROP COLUMN project_id;
ALTER TABLE taken_records DROP COLUMN project_id;
ALTER TABLE task_board_columns DROP COLUMN project_id;
ALTER TABLE task_board_runs DROP COLUMN project_id;
ALTER TABLE task_boards DROP COLUMN project_id;
ALTER TABLE team_members DROP COLUMN project_id;
ALTER TABLE teams DROP COLUMN project_id;
ALTER TABLE user_roles DROP COLUMN project_id;
ALTER TABLE version_roles DROP COLUMN project_id;
ALTER TABLE webhook_deliveries DROP COLUMN project_id;
ALTER TABLE webhook_digests DROP COLUMN project_id;
ALTER TABLE webhooks DROP COLUMN project_id;
