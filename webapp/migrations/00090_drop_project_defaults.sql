-- Every row names its project as it is inserted, so project_id has no
-- default any more: an insert forgetting it fails, whatever project it is
-- in, rather than landing in Legacy (00050's default, kept while the
-- inserts came to name their project). Only the column's default goes,
-- which MySQL changes without touching the rows.

-- +goose Up
ALTER TABLE api_keys ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE assistant_messages ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE audit_events ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE board_changes ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE board_viewers ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE connections ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE credential_teams ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE credentials ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE egress_hosts ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE favourite_flows ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE flow_history ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE flow_starts ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE flow_tags ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE flow_versions ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE flows ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE inner_boards ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE instance_keys ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE limits ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE link_states ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE llm_usage ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE mcp_servers ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE mcp_sign_ins ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE mcp_tokens ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE model_default ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE model_keys ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE model_providers ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE notes ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE notifications ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE object_attributes ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE object_instances ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE object_links ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE object_states ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE objects ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE person_slots ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE process_samples ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE processes ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE proxy_grants ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE record_changes ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE review_comments ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE run_logs ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE run_moves ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE run_objects ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE run_step_followups ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE run_step_messages ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE run_step_output ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE run_steps ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE runs ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE schedules ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE shared_keys ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE shared_records ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE skills ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE start_targets ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE taken_records ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE task_board_columns ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE task_board_runs ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE task_boards ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE team_members ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE teams ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE user_roles ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE version_roles ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE webhook_deliveries ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE webhook_digests ALTER COLUMN project_id DROP DEFAULT;
ALTER TABLE webhooks ALTER COLUMN project_id DROP DEFAULT;

-- +goose Down
-- As every migration since projects came in, going down is refused while a
-- project but Legacy is left, as going further down would lose which
-- project everything was in: the row inserted fails its check, which says
-- so.
DROP TEMPORARY TABLE IF EXISTS projects_down;
CREATE TEMPORARY TABLE projects_down (others BIGINT NOT NULL,
  CONSTRAINT `delete every project but Legacy before migrating down` CHECK (others = 0));
INSERT INTO projects_down SELECT COUNT(*) FROM projects WHERE id <> 1;
DROP TEMPORARY TABLE projects_down;

-- Rows inserted without naming their project are Legacy's again.
ALTER TABLE api_keys ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE assistant_messages ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE audit_events ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE board_changes ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE board_viewers ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE connections ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE credential_teams ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE credentials ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE egress_hosts ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE favourite_flows ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE flow_history ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE flow_starts ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE flow_tags ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE flow_versions ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE flows ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE inner_boards ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE instance_keys ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE limits ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE link_states ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE llm_usage ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE mcp_servers ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE mcp_sign_ins ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE mcp_tokens ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE model_default ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE model_keys ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE model_providers ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE notes ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE notifications ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE object_attributes ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE object_instances ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE object_links ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE object_states ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE objects ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE person_slots ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE process_samples ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE processes ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE proxy_grants ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE record_changes ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE review_comments ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE run_logs ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE run_moves ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE run_objects ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE run_step_followups ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE run_step_messages ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE run_step_output ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE run_steps ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE runs ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE schedules ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE shared_keys ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE shared_records ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE skills ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE start_targets ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE taken_records ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE task_board_columns ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE task_board_runs ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE task_boards ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE team_members ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE teams ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE user_roles ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE version_roles ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE webhook_deliveries ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE webhook_digests ALTER COLUMN project_id SET DEFAULT 1;
ALTER TABLE webhooks ALTER COLUMN project_id SET DEFAULT 1;
