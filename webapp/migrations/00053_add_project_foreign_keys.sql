-- A row can't be in another project than what it belongs to or refers to:
-- each foreign key to a row of a project names its project too, so the
-- database checks it. One naming a person refers to them as a member of
-- the project (project_members), so it can't name someone outside it: an
-- owner, a team's member, someone holding a role, an API key's maker.
--
-- MySQL nulls every column of a key ON DELETE SET NULL, which project_id,
-- never null, can't be; such keys stay on the id alone, but those to a flow
-- and its versions, which only deleting the flow for good deletes, so it
-- nulls them itself first (deleteFlow): a flow's version and the one it
-- published, and a task board column's struct and flow.
--
-- Foreign keys go unchecked while they are made, as all there is is
-- Legacy's; with checks on MySQL would copy each table whole to add one.

-- +goose Up
SET FOREIGN_KEY_CHECKS = 0;

ALTER TABLE api_keys DROP FOREIGN KEY api_keys_ibfk_1;
ALTER TABLE api_keys ADD KEY api_keys_member (user_id, project_id),
  ADD CONSTRAINT api_keys_member FOREIGN KEY (user_id, project_id) REFERENCES project_members (user_id, project_id) ON DELETE CASCADE;
ALTER TABLE assistant_messages DROP FOREIGN KEY assistant_messages_ibfk_1;
ALTER TABLE assistant_messages ADD KEY assistant_messages_flow_project (flow_id, project_id),
  ADD CONSTRAINT assistant_messages_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id) ON DELETE CASCADE;
ALTER TABLE board_changes DROP FOREIGN KEY board_changes_flow;
ALTER TABLE board_changes ADD KEY board_changes_flow_project (flow_id, project_id),
  ADD CONSTRAINT board_changes_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id) ON DELETE CASCADE;
ALTER TABLE board_viewers DROP FOREIGN KEY board_viewers_flow;
ALTER TABLE board_viewers ADD KEY board_viewers_flow_project (flow_id, project_id),
  ADD CONSTRAINT board_viewers_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id) ON DELETE CASCADE;
ALTER TABLE connections DROP FOREIGN KEY connections_ibfk_1;
ALTER TABLE connections ADD KEY connections_source_project (source_id, project_id),
  ADD CONSTRAINT connections_source_project FOREIGN KEY (source_id, project_id) REFERENCES processes (id, project_id) ON DELETE CASCADE;
ALTER TABLE connections DROP FOREIGN KEY connections_ibfk_2;
ALTER TABLE connections ADD KEY connections_target_project (target_id, project_id),
  ADD CONSTRAINT connections_target_project FOREIGN KEY (target_id, project_id) REFERENCES processes (id, project_id) ON DELETE CASCADE;
ALTER TABLE credential_teams DROP FOREIGN KEY credential_teams_credential;
ALTER TABLE credential_teams ADD KEY credential_teams_credential_project (credential_id, project_id),
  ADD CONSTRAINT credential_teams_credential_project FOREIGN KEY (credential_id, project_id) REFERENCES credentials (id, project_id) ON DELETE CASCADE;
ALTER TABLE credential_teams DROP FOREIGN KEY credential_teams_team;
ALTER TABLE credential_teams ADD KEY credential_teams_team_project (team_id, project_id),
  ADD CONSTRAINT credential_teams_team_project FOREIGN KEY (team_id, project_id) REFERENCES teams (id, project_id) ON DELETE CASCADE;
ALTER TABLE favourite_flows DROP FOREIGN KEY favourite_flows_flow;
ALTER TABLE favourite_flows ADD KEY favourite_flows_flow_project (flow_id, project_id),
  ADD CONSTRAINT favourite_flows_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id) ON DELETE CASCADE;
ALTER TABLE flow_history DROP FOREIGN KEY flow_history_ibfk_1;
ALTER TABLE flow_history ADD KEY flow_history_flow_project (flow_id, project_id),
  ADD CONSTRAINT flow_history_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id) ON DELETE CASCADE;
ALTER TABLE flow_starts DROP FOREIGN KEY flow_starts_ibfk_1;
ALTER TABLE flow_starts ADD KEY flow_starts_flow_project (flow_id, project_id),
  ADD CONSTRAINT flow_starts_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id) ON DELETE CASCADE;
ALTER TABLE flow_tags DROP FOREIGN KEY flow_tags_flow;
ALTER TABLE flow_tags ADD KEY flow_tags_flow_project (flow_id, project_id),
  ADD CONSTRAINT flow_tags_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id) ON DELETE CASCADE;
ALTER TABLE flow_versions DROP FOREIGN KEY flow_versions_flow;
ALTER TABLE flow_versions ADD KEY flow_versions_flow_project (flow_id, project_id),
  ADD CONSTRAINT flow_versions_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id);
ALTER TABLE flows DROP FOREIGN KEY flows_ibfk_1;
ALTER TABLE flows ADD KEY flows_version_project (version_id, project_id),
  ADD CONSTRAINT flows_version_project FOREIGN KEY (version_id, project_id) REFERENCES flow_versions (id, project_id);
ALTER TABLE flows DROP FOREIGN KEY flows_published_version;
ALTER TABLE flows ADD KEY flows_published_version_project (published_version_id, project_id),
  ADD CONSTRAINT flows_published_version_project FOREIGN KEY (published_version_id, project_id) REFERENCES flow_versions (id, project_id);
ALTER TABLE flows DROP FOREIGN KEY flows_owner_team;
ALTER TABLE flows ADD KEY flows_owner_team_project (owner_team_id, project_id),
  ADD CONSTRAINT flows_owner_team_project FOREIGN KEY (owner_team_id, project_id) REFERENCES teams (id, project_id);
ALTER TABLE flows DROP FOREIGN KEY flows_owner_user;
ALTER TABLE flows ADD KEY flows_owner_member (owner_user_id, project_id),
  ADD CONSTRAINT flows_owner_member FOREIGN KEY (owner_user_id, project_id) REFERENCES project_members (user_id, project_id);
ALTER TABLE inner_boards DROP FOREIGN KEY inner_boards_ibfk_1;
ALTER TABLE inner_boards ADD KEY inner_boards_process_project (process_id, project_id),
  ADD CONSTRAINT inner_boards_process_project FOREIGN KEY (process_id, project_id) REFERENCES processes (id, project_id) ON DELETE CASCADE;
ALTER TABLE instance_keys DROP FOREIGN KEY instance_keys_ibfk_1;
ALTER TABLE instance_keys ADD KEY instance_keys_instance_project (instance_id, project_id),
  ADD CONSTRAINT instance_keys_instance_project FOREIGN KEY (instance_id, project_id) REFERENCES object_instances (id, project_id) ON DELETE CASCADE;
ALTER TABLE link_states DROP FOREIGN KEY link_states_ibfk_1;
ALTER TABLE link_states ADD KEY link_states_link_project (link_id, project_id),
  ADD CONSTRAINT link_states_link_project FOREIGN KEY (link_id, project_id) REFERENCES object_links (id, project_id) ON DELETE CASCADE;
ALTER TABLE link_states DROP FOREIGN KEY link_states_ibfk_2;
ALTER TABLE link_states ADD KEY link_states_state_project (state_id, project_id),
  ADD CONSTRAINT link_states_state_project FOREIGN KEY (state_id, project_id) REFERENCES object_states (id, project_id) ON DELETE CASCADE;
ALTER TABLE mcp_tokens DROP FOREIGN KEY mcp_tokens_ibfk_1;
ALTER TABLE mcp_tokens ADD KEY mcp_tokens_server_project (server_id, project_id),
  ADD CONSTRAINT mcp_tokens_server_project FOREIGN KEY (server_id, project_id) REFERENCES mcp_servers (id, project_id) ON DELETE CASCADE;
ALTER TABLE notes DROP FOREIGN KEY notes_flow;
ALTER TABLE notes ADD KEY notes_flow_project (flow_id, project_id),
  ADD CONSTRAINT notes_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id) ON DELETE CASCADE;
ALTER TABLE notes DROP FOREIGN KEY notes_parent;
ALTER TABLE notes ADD KEY notes_parent_project (parent_id, project_id),
  ADD CONSTRAINT notes_parent_project FOREIGN KEY (parent_id, project_id) REFERENCES processes (id, project_id) ON DELETE CASCADE;
ALTER TABLE notifications DROP FOREIGN KEY notifications_run;
ALTER TABLE notifications ADD KEY notifications_run_project (run_id, project_id),
  ADD CONSTRAINT notifications_run_project FOREIGN KEY (run_id, project_id) REFERENCES runs (id, project_id) ON DELETE CASCADE;
ALTER TABLE object_attributes DROP FOREIGN KEY object_attributes_ibfk_1;
ALTER TABLE object_attributes ADD KEY object_attributes_object_project (object_id, project_id),
  ADD CONSTRAINT object_attributes_object_project FOREIGN KEY (object_id, project_id) REFERENCES objects (id, project_id) ON DELETE CASCADE;
ALTER TABLE object_instances DROP FOREIGN KEY object_instances_ibfk_1;
ALTER TABLE object_instances ADD KEY object_instances_run_project (run_id, project_id),
  ADD CONSTRAINT object_instances_run_project FOREIGN KEY (run_id, project_id) REFERENCES runs (id, project_id) ON DELETE CASCADE;
ALTER TABLE object_instances DROP FOREIGN KEY object_instances_ibfk_2;
ALTER TABLE object_instances ADD KEY object_instances_step_project (step_id, project_id),
  ADD CONSTRAINT object_instances_step_project FOREIGN KEY (step_id, project_id) REFERENCES run_steps (id, project_id) ON DELETE CASCADE;
ALTER TABLE object_links DROP FOREIGN KEY object_links_ibfk_1;
ALTER TABLE object_links ADD KEY object_links_process_project (process_id, project_id),
  ADD CONSTRAINT object_links_process_project FOREIGN KEY (process_id, project_id) REFERENCES processes (id, project_id) ON DELETE CASCADE;
ALTER TABLE object_links DROP FOREIGN KEY object_links_ibfk_2;
ALTER TABLE object_links ADD KEY object_links_object_project (object_id, project_id),
  ADD CONSTRAINT object_links_object_project FOREIGN KEY (object_id, project_id) REFERENCES objects (id, project_id) ON DELETE CASCADE;
ALTER TABLE object_states DROP FOREIGN KEY object_states_ibfk_1;
ALTER TABLE object_states ADD KEY object_states_object_project (object_id, project_id),
  ADD CONSTRAINT object_states_object_project FOREIGN KEY (object_id, project_id) REFERENCES objects (id, project_id) ON DELETE CASCADE;
ALTER TABLE objects DROP FOREIGN KEY objects_flow;
ALTER TABLE objects ADD KEY objects_flow_project (flow_id, project_id),
  ADD CONSTRAINT objects_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id);
ALTER TABLE objects DROP FOREIGN KEY objects_owner;
ALTER TABLE objects ADD KEY objects_owner_project (owner_id, project_id),
  ADD CONSTRAINT objects_owner_project FOREIGN KEY (owner_id, project_id) REFERENCES objects (id, project_id) ON DELETE CASCADE;
ALTER TABLE objects DROP FOREIGN KEY objects_parent;
ALTER TABLE objects ADD KEY objects_parent_project (parent_id, project_id),
  ADD CONSTRAINT objects_parent_project FOREIGN KEY (parent_id, project_id) REFERENCES processes (id, project_id) ON DELETE CASCADE;
ALTER TABLE process_samples DROP FOREIGN KEY process_samples_process;
ALTER TABLE process_samples ADD KEY process_samples_process_project (process_id, project_id),
  ADD CONSTRAINT process_samples_process_project FOREIGN KEY (process_id, project_id) REFERENCES processes (id, project_id) ON DELETE CASCADE;
ALTER TABLE processes DROP FOREIGN KEY processes_flow;
ALTER TABLE processes ADD KEY processes_flow_project (flow_id, project_id),
  ADD CONSTRAINT processes_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id);
ALTER TABLE processes DROP FOREIGN KEY processes_parent;
ALTER TABLE processes ADD KEY processes_parent_project (parent_id, project_id),
  ADD CONSTRAINT processes_parent_project FOREIGN KEY (parent_id, project_id) REFERENCES processes (id, project_id) ON DELETE CASCADE;
ALTER TABLE record_changes DROP FOREIGN KEY record_changes_record;
ALTER TABLE record_changes ADD KEY record_changes_record_project (record_id, project_id),
  ADD CONSTRAINT record_changes_record_project FOREIGN KEY (record_id, project_id) REFERENCES shared_records (id, project_id) ON DELETE CASCADE;
ALTER TABLE review_comments DROP FOREIGN KEY review_comments_run;
ALTER TABLE review_comments ADD KEY review_comments_run_project (run_id, project_id),
  ADD CONSTRAINT review_comments_run_project FOREIGN KEY (run_id, project_id) REFERENCES runs (id, project_id) ON DELETE CASCADE;
ALTER TABLE review_comments DROP FOREIGN KEY review_comments_step;
ALTER TABLE review_comments ADD KEY review_comments_step_project (step_id, project_id),
  ADD CONSTRAINT review_comments_step_project FOREIGN KEY (step_id, project_id) REFERENCES run_steps (id, project_id) ON DELETE CASCADE;
ALTER TABLE run_logs DROP FOREIGN KEY run_logs_run;
ALTER TABLE run_logs ADD KEY run_logs_run_project (run_id, project_id),
  ADD CONSTRAINT run_logs_run_project FOREIGN KEY (run_id, project_id) REFERENCES runs (id, project_id) ON DELETE CASCADE;
ALTER TABLE run_logs DROP FOREIGN KEY run_logs_step;
ALTER TABLE run_logs ADD KEY run_logs_step_project (step_id, project_id),
  ADD CONSTRAINT run_logs_step_project FOREIGN KEY (step_id, project_id) REFERENCES run_steps (id, project_id) ON DELETE CASCADE;
ALTER TABLE run_moves DROP FOREIGN KEY run_moves_run;
ALTER TABLE run_moves ADD KEY run_moves_run_project (run_id, project_id),
  ADD CONSTRAINT run_moves_run_project FOREIGN KEY (run_id, project_id) REFERENCES runs (id, project_id) ON DELETE CASCADE;
ALTER TABLE run_objects DROP FOREIGN KEY run_objects_ibfk_1;
ALTER TABLE run_objects ADD KEY run_objects_run_project (run_id, project_id),
  ADD CONSTRAINT run_objects_run_project FOREIGN KEY (run_id, project_id) REFERENCES runs (id, project_id) ON DELETE CASCADE;
ALTER TABLE run_objects DROP FOREIGN KEY run_objects_ibfk_2;
ALTER TABLE run_objects ADD KEY run_objects_step_project (step_id, project_id),
  ADD CONSTRAINT run_objects_step_project FOREIGN KEY (step_id, project_id) REFERENCES run_steps (id, project_id) ON DELETE CASCADE;
ALTER TABLE run_step_followups DROP FOREIGN KEY run_step_followups_ibfk_1;
ALTER TABLE run_step_followups ADD KEY run_step_followups_step_project (step_id, project_id),
  ADD CONSTRAINT run_step_followups_step_project FOREIGN KEY (step_id, project_id) REFERENCES run_steps (id, project_id) ON DELETE CASCADE;
ALTER TABLE run_step_messages DROP FOREIGN KEY run_step_messages_ibfk_1;
ALTER TABLE run_step_messages ADD KEY run_step_messages_step_project (step_id, project_id),
  ADD CONSTRAINT run_step_messages_step_project FOREIGN KEY (step_id, project_id) REFERENCES run_steps (id, project_id) ON DELETE CASCADE;
ALTER TABLE run_step_output DROP FOREIGN KEY run_step_output_step;
ALTER TABLE run_step_output ADD KEY run_step_output_step_project (step_id, project_id),
  ADD CONSTRAINT run_step_output_step_project FOREIGN KEY (step_id, project_id) REFERENCES run_steps (id, project_id) ON DELETE CASCADE;
ALTER TABLE run_steps DROP FOREIGN KEY run_steps_ibfk_1;
ALTER TABLE run_steps ADD KEY run_steps_run_project (run_id, project_id),
  ADD CONSTRAINT run_steps_run_project FOREIGN KEY (run_id, project_id) REFERENCES runs (id, project_id) ON DELETE CASCADE;
ALTER TABLE runs DROP FOREIGN KEY runs_flow;
ALTER TABLE runs ADD KEY runs_flow_project (flow_id, project_id),
  ADD CONSTRAINT runs_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id);
ALTER TABLE schedules DROP FOREIGN KEY schedules_ibfk_1;
ALTER TABLE schedules ADD KEY schedules_flow_project (flow_id, project_id),
  ADD CONSTRAINT schedules_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id) ON DELETE CASCADE;
ALTER TABLE shared_keys DROP FOREIGN KEY shared_keys_ibfk_1;
ALTER TABLE shared_keys ADD KEY shared_keys_record_project (record_id, project_id),
  ADD CONSTRAINT shared_keys_record_project FOREIGN KEY (record_id, project_id) REFERENCES shared_records (id, project_id) ON DELETE CASCADE;
ALTER TABLE skills DROP FOREIGN KEY skills_ibfk_1;
ALTER TABLE skills ADD KEY skills_owner_member (owner_user_id, project_id),
  ADD CONSTRAINT skills_owner_member FOREIGN KEY (owner_user_id, project_id) REFERENCES project_members (user_id, project_id);
ALTER TABLE skills DROP FOREIGN KEY skills_ibfk_2;
ALTER TABLE skills ADD KEY skills_owner_team_project (owner_team_id, project_id),
  ADD CONSTRAINT skills_owner_team_project FOREIGN KEY (owner_team_id, project_id) REFERENCES teams (id, project_id);
ALTER TABLE start_targets DROP FOREIGN KEY start_targets_process;
ALTER TABLE start_targets ADD KEY start_targets_process_project (process_id, project_id),
  ADD CONSTRAINT start_targets_process_project FOREIGN KEY (process_id, project_id) REFERENCES processes (id, project_id) ON DELETE CASCADE;
ALTER TABLE taken_records DROP FOREIGN KEY taken_records_run;
ALTER TABLE taken_records ADD KEY taken_records_run_project (run_id, project_id),
  ADD CONSTRAINT taken_records_run_project FOREIGN KEY (run_id, project_id) REFERENCES runs (id, project_id) ON DELETE CASCADE;
ALTER TABLE task_board_columns DROP FOREIGN KEY task_board_columns_board;
ALTER TABLE task_board_columns ADD KEY task_board_columns_board_project (board_id, project_id),
  ADD CONSTRAINT task_board_columns_board_project FOREIGN KEY (board_id, project_id) REFERENCES task_boards (id, project_id) ON DELETE CASCADE;
ALTER TABLE task_board_columns DROP FOREIGN KEY task_board_columns_flow;
ALTER TABLE task_board_columns ADD KEY task_board_columns_flow_project (flow_id, project_id),
  ADD CONSTRAINT task_board_columns_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id);
ALTER TABLE task_board_columns DROP FOREIGN KEY task_board_columns_struct;
ALTER TABLE task_board_columns ADD KEY task_board_columns_struct_project (struct_id, project_id),
  ADD CONSTRAINT task_board_columns_struct_project FOREIGN KEY (struct_id, project_id) REFERENCES flows (id, project_id);
ALTER TABLE task_board_runs DROP FOREIGN KEY task_board_runs_board;
ALTER TABLE task_board_runs ADD KEY task_board_runs_board_project (board_id, project_id),
  ADD CONSTRAINT task_board_runs_board_project FOREIGN KEY (board_id, project_id) REFERENCES task_boards (id, project_id) ON DELETE CASCADE;
ALTER TABLE task_board_runs DROP FOREIGN KEY task_board_runs_run;
ALTER TABLE task_board_runs ADD KEY task_board_runs_run_project (run_id, project_id),
  ADD CONSTRAINT task_board_runs_run_project FOREIGN KEY (run_id, project_id) REFERENCES runs (id, project_id) ON DELETE CASCADE;
ALTER TABLE task_boards DROP FOREIGN KEY task_boards_owner_team;
ALTER TABLE task_boards ADD KEY task_boards_owner_team_project (owner_team_id, project_id),
  ADD CONSTRAINT task_boards_owner_team_project FOREIGN KEY (owner_team_id, project_id) REFERENCES teams (id, project_id);
ALTER TABLE task_boards DROP FOREIGN KEY task_boards_owner_user;
ALTER TABLE task_boards ADD KEY task_boards_owner_member (owner_user_id, project_id),
  ADD CONSTRAINT task_boards_owner_member FOREIGN KEY (owner_user_id, project_id) REFERENCES project_members (user_id, project_id);
ALTER TABLE team_members DROP FOREIGN KEY team_members_ibfk_1;
ALTER TABLE team_members ADD KEY team_members_team_project (team_id, project_id),
  ADD CONSTRAINT team_members_team_project FOREIGN KEY (team_id, project_id) REFERENCES teams (id, project_id) ON DELETE CASCADE;
ALTER TABLE team_members DROP FOREIGN KEY team_members_ibfk_2;
ALTER TABLE team_members ADD KEY team_members_member (user_id, project_id),
  ADD CONSTRAINT team_members_member FOREIGN KEY (user_id, project_id) REFERENCES project_members (user_id, project_id) ON DELETE CASCADE;
ALTER TABLE user_roles DROP FOREIGN KEY user_roles_ibfk_1;
ALTER TABLE user_roles ADD CONSTRAINT user_roles_member FOREIGN KEY (user_id, project_id) REFERENCES project_members (user_id, project_id) ON DELETE CASCADE;
ALTER TABLE version_roles DROP FOREIGN KEY version_roles_version;
ALTER TABLE version_roles ADD KEY version_roles_version_project (version_id, project_id),
  ADD CONSTRAINT version_roles_version_project FOREIGN KEY (version_id, project_id) REFERENCES flow_versions (id, project_id) ON DELETE CASCADE;
ALTER TABLE webhook_deliveries DROP FOREIGN KEY webhook_deliveries_webhook;
ALTER TABLE webhook_deliveries ADD KEY webhook_deliveries_webhook_project (webhook_id, project_id),
  ADD CONSTRAINT webhook_deliveries_webhook_project FOREIGN KEY (webhook_id, project_id) REFERENCES webhooks (id, project_id) ON DELETE CASCADE;
ALTER TABLE webhook_digests DROP FOREIGN KEY webhook_digests_webhook;
ALTER TABLE webhook_digests ADD KEY webhook_digests_webhook_project (webhook_id, project_id),
  ADD CONSTRAINT webhook_digests_webhook_project FOREIGN KEY (webhook_id, project_id) REFERENCES webhooks (id, project_id) ON DELETE CASCADE;
ALTER TABLE webhooks DROP FOREIGN KEY webhooks_flow;
ALTER TABLE webhooks ADD KEY webhooks_flow_project (flow_id, project_id),
  ADD CONSTRAINT webhooks_flow_project FOREIGN KEY (flow_id, project_id) REFERENCES flows (id, project_id) ON DELETE CASCADE;

SET FOREIGN_KEY_CHECKS = 1;

-- +goose Down
-- Going down loses which project everything was in, so it is refused while
-- a project but Legacy is left: the row inserted fails its check, which
-- says so.
DROP TEMPORARY TABLE IF EXISTS projects_down;
CREATE TEMPORARY TABLE projects_down (others BIGINT NOT NULL,
  CONSTRAINT `delete every project but Legacy before migrating down` CHECK (others = 0));
INSERT INTO projects_down SELECT COUNT(*) FROM projects WHERE id <> 1;
DROP TEMPORARY TABLE projects_down;

SET FOREIGN_KEY_CHECKS = 0;

ALTER TABLE webhooks DROP FOREIGN KEY webhooks_flow_project, DROP INDEX webhooks_flow_project;
ALTER TABLE webhooks ADD CONSTRAINT webhooks_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE;
ALTER TABLE webhook_digests DROP FOREIGN KEY webhook_digests_webhook_project, DROP INDEX webhook_digests_webhook_project;
ALTER TABLE webhook_digests ADD CONSTRAINT webhook_digests_webhook FOREIGN KEY (webhook_id) REFERENCES webhooks (id) ON DELETE CASCADE;
ALTER TABLE webhook_deliveries DROP FOREIGN KEY webhook_deliveries_webhook_project, DROP INDEX webhook_deliveries_webhook_project;
ALTER TABLE webhook_deliveries ADD CONSTRAINT webhook_deliveries_webhook FOREIGN KEY (webhook_id) REFERENCES webhooks (id) ON DELETE CASCADE;
ALTER TABLE version_roles DROP FOREIGN KEY version_roles_version_project, DROP INDEX version_roles_version_project;
ALTER TABLE version_roles ADD CONSTRAINT version_roles_version FOREIGN KEY (version_id) REFERENCES flow_versions (id) ON DELETE CASCADE;
ALTER TABLE user_roles DROP FOREIGN KEY user_roles_member;
ALTER TABLE user_roles ADD CONSTRAINT user_roles_ibfk_1 FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE team_members DROP FOREIGN KEY team_members_member, DROP INDEX team_members_member;
ALTER TABLE team_members ADD CONSTRAINT team_members_ibfk_2 FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE team_members DROP FOREIGN KEY team_members_team_project, DROP INDEX team_members_team_project;
ALTER TABLE team_members ADD CONSTRAINT team_members_ibfk_1 FOREIGN KEY (team_id) REFERENCES teams (id) ON DELETE CASCADE;
ALTER TABLE task_boards DROP FOREIGN KEY task_boards_owner_member, DROP INDEX task_boards_owner_member;
ALTER TABLE task_boards ADD CONSTRAINT task_boards_owner_user FOREIGN KEY (owner_user_id) REFERENCES users (id);
ALTER TABLE task_boards DROP FOREIGN KEY task_boards_owner_team_project, DROP INDEX task_boards_owner_team_project;
ALTER TABLE task_boards ADD CONSTRAINT task_boards_owner_team FOREIGN KEY (owner_team_id) REFERENCES teams (id);
ALTER TABLE task_board_runs DROP FOREIGN KEY task_board_runs_run_project, DROP INDEX task_board_runs_run_project;
ALTER TABLE task_board_runs ADD CONSTRAINT task_board_runs_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;
ALTER TABLE task_board_runs DROP FOREIGN KEY task_board_runs_board_project, DROP INDEX task_board_runs_board_project;
ALTER TABLE task_board_runs ADD CONSTRAINT task_board_runs_board FOREIGN KEY (board_id) REFERENCES task_boards (id) ON DELETE CASCADE;
ALTER TABLE task_board_columns DROP FOREIGN KEY task_board_columns_struct_project, DROP INDEX task_board_columns_struct_project;
ALTER TABLE task_board_columns ADD CONSTRAINT task_board_columns_struct FOREIGN KEY (struct_id) REFERENCES flows (id) ON DELETE SET NULL;
ALTER TABLE task_board_columns DROP FOREIGN KEY task_board_columns_flow_project, DROP INDEX task_board_columns_flow_project;
ALTER TABLE task_board_columns ADD CONSTRAINT task_board_columns_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE SET NULL;
ALTER TABLE task_board_columns DROP FOREIGN KEY task_board_columns_board_project, DROP INDEX task_board_columns_board_project;
ALTER TABLE task_board_columns ADD CONSTRAINT task_board_columns_board FOREIGN KEY (board_id) REFERENCES task_boards (id) ON DELETE CASCADE;
ALTER TABLE taken_records DROP FOREIGN KEY taken_records_run_project, DROP INDEX taken_records_run_project;
ALTER TABLE taken_records ADD CONSTRAINT taken_records_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;
ALTER TABLE start_targets DROP FOREIGN KEY start_targets_process_project, DROP INDEX start_targets_process_project;
ALTER TABLE start_targets ADD CONSTRAINT start_targets_process FOREIGN KEY (process_id) REFERENCES processes (id) ON DELETE CASCADE;
ALTER TABLE skills DROP FOREIGN KEY skills_owner_team_project, DROP INDEX skills_owner_team_project;
ALTER TABLE skills ADD CONSTRAINT skills_ibfk_2 FOREIGN KEY (owner_team_id) REFERENCES teams (id);
ALTER TABLE skills DROP FOREIGN KEY skills_owner_member, DROP INDEX skills_owner_member;
ALTER TABLE skills ADD CONSTRAINT skills_ibfk_1 FOREIGN KEY (owner_user_id) REFERENCES users (id);
ALTER TABLE shared_keys DROP FOREIGN KEY shared_keys_record_project, DROP INDEX shared_keys_record_project;
ALTER TABLE shared_keys ADD CONSTRAINT shared_keys_ibfk_1 FOREIGN KEY (record_id) REFERENCES shared_records (id) ON DELETE CASCADE;
ALTER TABLE schedules DROP FOREIGN KEY schedules_flow_project, DROP INDEX schedules_flow_project;
ALTER TABLE schedules ADD CONSTRAINT schedules_ibfk_1 FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE;
ALTER TABLE runs DROP FOREIGN KEY runs_flow_project, DROP INDEX runs_flow_project;
ALTER TABLE runs ADD CONSTRAINT runs_flow FOREIGN KEY (flow_id) REFERENCES flows (id);
ALTER TABLE run_steps DROP FOREIGN KEY run_steps_run_project, DROP INDEX run_steps_run_project;
ALTER TABLE run_steps ADD CONSTRAINT run_steps_ibfk_1 FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;
ALTER TABLE run_step_output DROP FOREIGN KEY run_step_output_step_project, DROP INDEX run_step_output_step_project;
ALTER TABLE run_step_output ADD CONSTRAINT run_step_output_step FOREIGN KEY (step_id) REFERENCES run_steps (id) ON DELETE CASCADE;
ALTER TABLE run_step_messages DROP FOREIGN KEY run_step_messages_step_project, DROP INDEX run_step_messages_step_project;
ALTER TABLE run_step_messages ADD CONSTRAINT run_step_messages_ibfk_1 FOREIGN KEY (step_id) REFERENCES run_steps (id) ON DELETE CASCADE;
ALTER TABLE run_step_followups DROP FOREIGN KEY run_step_followups_step_project, DROP INDEX run_step_followups_step_project;
ALTER TABLE run_step_followups ADD CONSTRAINT run_step_followups_ibfk_1 FOREIGN KEY (step_id) REFERENCES run_steps (id) ON DELETE CASCADE;
ALTER TABLE run_objects DROP FOREIGN KEY run_objects_step_project, DROP INDEX run_objects_step_project;
ALTER TABLE run_objects ADD CONSTRAINT run_objects_ibfk_2 FOREIGN KEY (step_id) REFERENCES run_steps (id) ON DELETE CASCADE;
ALTER TABLE run_objects DROP FOREIGN KEY run_objects_run_project, DROP INDEX run_objects_run_project;
ALTER TABLE run_objects ADD CONSTRAINT run_objects_ibfk_1 FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;
ALTER TABLE run_moves DROP FOREIGN KEY run_moves_run_project, DROP INDEX run_moves_run_project;
ALTER TABLE run_moves ADD CONSTRAINT run_moves_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;
ALTER TABLE run_logs DROP FOREIGN KEY run_logs_step_project, DROP INDEX run_logs_step_project;
ALTER TABLE run_logs ADD CONSTRAINT run_logs_step FOREIGN KEY (step_id) REFERENCES run_steps (id) ON DELETE CASCADE;
ALTER TABLE run_logs DROP FOREIGN KEY run_logs_run_project, DROP INDEX run_logs_run_project;
ALTER TABLE run_logs ADD CONSTRAINT run_logs_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;
ALTER TABLE review_comments DROP FOREIGN KEY review_comments_step_project, DROP INDEX review_comments_step_project;
ALTER TABLE review_comments ADD CONSTRAINT review_comments_step FOREIGN KEY (step_id) REFERENCES run_steps (id) ON DELETE CASCADE;
ALTER TABLE review_comments DROP FOREIGN KEY review_comments_run_project, DROP INDEX review_comments_run_project;
ALTER TABLE review_comments ADD CONSTRAINT review_comments_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;
ALTER TABLE record_changes DROP FOREIGN KEY record_changes_record_project, DROP INDEX record_changes_record_project;
ALTER TABLE record_changes ADD CONSTRAINT record_changes_record FOREIGN KEY (record_id) REFERENCES shared_records (id) ON DELETE CASCADE;
ALTER TABLE processes DROP FOREIGN KEY processes_parent_project, DROP INDEX processes_parent_project;
ALTER TABLE processes ADD CONSTRAINT processes_parent FOREIGN KEY (parent_id) REFERENCES processes (id) ON DELETE CASCADE;
ALTER TABLE processes DROP FOREIGN KEY processes_flow_project, DROP INDEX processes_flow_project;
ALTER TABLE processes ADD CONSTRAINT processes_flow FOREIGN KEY (flow_id) REFERENCES flows (id);
ALTER TABLE process_samples DROP FOREIGN KEY process_samples_process_project, DROP INDEX process_samples_process_project;
ALTER TABLE process_samples ADD CONSTRAINT process_samples_process FOREIGN KEY (process_id) REFERENCES processes (id) ON DELETE CASCADE;
ALTER TABLE objects DROP FOREIGN KEY objects_parent_project, DROP INDEX objects_parent_project;
ALTER TABLE objects ADD CONSTRAINT objects_parent FOREIGN KEY (parent_id) REFERENCES processes (id) ON DELETE CASCADE;
ALTER TABLE objects DROP FOREIGN KEY objects_owner_project, DROP INDEX objects_owner_project;
ALTER TABLE objects ADD CONSTRAINT objects_owner FOREIGN KEY (owner_id) REFERENCES objects (id) ON DELETE CASCADE;
ALTER TABLE objects DROP FOREIGN KEY objects_flow_project, DROP INDEX objects_flow_project;
ALTER TABLE objects ADD CONSTRAINT objects_flow FOREIGN KEY (flow_id) REFERENCES flows (id);
ALTER TABLE object_states DROP FOREIGN KEY object_states_object_project, DROP INDEX object_states_object_project;
ALTER TABLE object_states ADD CONSTRAINT object_states_ibfk_1 FOREIGN KEY (object_id) REFERENCES objects (id) ON DELETE CASCADE;
ALTER TABLE object_links DROP FOREIGN KEY object_links_object_project, DROP INDEX object_links_object_project;
ALTER TABLE object_links ADD CONSTRAINT object_links_ibfk_2 FOREIGN KEY (object_id) REFERENCES objects (id) ON DELETE CASCADE;
ALTER TABLE object_links DROP FOREIGN KEY object_links_process_project, DROP INDEX object_links_process_project;
ALTER TABLE object_links ADD CONSTRAINT object_links_ibfk_1 FOREIGN KEY (process_id) REFERENCES processes (id) ON DELETE CASCADE;
ALTER TABLE object_instances DROP FOREIGN KEY object_instances_step_project, DROP INDEX object_instances_step_project;
ALTER TABLE object_instances ADD CONSTRAINT object_instances_ibfk_2 FOREIGN KEY (step_id) REFERENCES run_steps (id) ON DELETE CASCADE;
ALTER TABLE object_instances DROP FOREIGN KEY object_instances_run_project, DROP INDEX object_instances_run_project;
ALTER TABLE object_instances ADD CONSTRAINT object_instances_ibfk_1 FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;
ALTER TABLE object_attributes DROP FOREIGN KEY object_attributes_object_project, DROP INDEX object_attributes_object_project;
ALTER TABLE object_attributes ADD CONSTRAINT object_attributes_ibfk_1 FOREIGN KEY (object_id) REFERENCES objects (id) ON DELETE CASCADE;
ALTER TABLE notifications DROP FOREIGN KEY notifications_run_project, DROP INDEX notifications_run_project;
ALTER TABLE notifications ADD CONSTRAINT notifications_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;
ALTER TABLE notes DROP FOREIGN KEY notes_parent_project, DROP INDEX notes_parent_project;
ALTER TABLE notes ADD CONSTRAINT notes_parent FOREIGN KEY (parent_id) REFERENCES processes (id) ON DELETE CASCADE;
ALTER TABLE notes DROP FOREIGN KEY notes_flow_project, DROP INDEX notes_flow_project;
ALTER TABLE notes ADD CONSTRAINT notes_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE;
ALTER TABLE mcp_tokens DROP FOREIGN KEY mcp_tokens_server_project, DROP INDEX mcp_tokens_server_project;
ALTER TABLE mcp_tokens ADD CONSTRAINT mcp_tokens_ibfk_1 FOREIGN KEY (server_id) REFERENCES mcp_servers (id) ON DELETE CASCADE;
ALTER TABLE link_states DROP FOREIGN KEY link_states_state_project, DROP INDEX link_states_state_project;
ALTER TABLE link_states ADD CONSTRAINT link_states_ibfk_2 FOREIGN KEY (state_id) REFERENCES object_states (id) ON DELETE CASCADE;
ALTER TABLE link_states DROP FOREIGN KEY link_states_link_project, DROP INDEX link_states_link_project;
ALTER TABLE link_states ADD CONSTRAINT link_states_ibfk_1 FOREIGN KEY (link_id) REFERENCES object_links (id) ON DELETE CASCADE;
ALTER TABLE instance_keys DROP FOREIGN KEY instance_keys_instance_project, DROP INDEX instance_keys_instance_project;
ALTER TABLE instance_keys ADD CONSTRAINT instance_keys_ibfk_1 FOREIGN KEY (instance_id) REFERENCES object_instances (id) ON DELETE CASCADE;
ALTER TABLE inner_boards DROP FOREIGN KEY inner_boards_process_project, DROP INDEX inner_boards_process_project;
ALTER TABLE inner_boards ADD CONSTRAINT inner_boards_ibfk_1 FOREIGN KEY (process_id) REFERENCES processes (id) ON DELETE CASCADE;
ALTER TABLE flows DROP FOREIGN KEY flows_owner_member, DROP INDEX flows_owner_member;
ALTER TABLE flows ADD CONSTRAINT flows_owner_user FOREIGN KEY (owner_user_id) REFERENCES users (id);
ALTER TABLE flows DROP FOREIGN KEY flows_owner_team_project, DROP INDEX flows_owner_team_project;
ALTER TABLE flows ADD CONSTRAINT flows_owner_team FOREIGN KEY (owner_team_id) REFERENCES teams (id);
ALTER TABLE flows DROP FOREIGN KEY flows_published_version_project, DROP INDEX flows_published_version_project;
ALTER TABLE flows ADD CONSTRAINT flows_published_version FOREIGN KEY (published_version_id) REFERENCES flow_versions (id) ON DELETE SET NULL;
ALTER TABLE flows DROP FOREIGN KEY flows_version_project, DROP INDEX flows_version_project;
ALTER TABLE flows ADD CONSTRAINT flows_ibfk_1 FOREIGN KEY (version_id) REFERENCES flow_versions (id) ON DELETE SET NULL;
ALTER TABLE flow_versions DROP FOREIGN KEY flow_versions_flow_project, DROP INDEX flow_versions_flow_project;
ALTER TABLE flow_versions ADD CONSTRAINT flow_versions_flow FOREIGN KEY (flow_id) REFERENCES flows (id);
ALTER TABLE flow_tags DROP FOREIGN KEY flow_tags_flow_project, DROP INDEX flow_tags_flow_project;
ALTER TABLE flow_tags ADD CONSTRAINT flow_tags_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE;
ALTER TABLE flow_starts DROP FOREIGN KEY flow_starts_flow_project, DROP INDEX flow_starts_flow_project;
ALTER TABLE flow_starts ADD CONSTRAINT flow_starts_ibfk_1 FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE;
ALTER TABLE flow_history DROP FOREIGN KEY flow_history_flow_project, DROP INDEX flow_history_flow_project;
ALTER TABLE flow_history ADD CONSTRAINT flow_history_ibfk_1 FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE;
ALTER TABLE favourite_flows DROP FOREIGN KEY favourite_flows_flow_project, DROP INDEX favourite_flows_flow_project;
ALTER TABLE favourite_flows ADD CONSTRAINT favourite_flows_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE;
ALTER TABLE credential_teams DROP FOREIGN KEY credential_teams_team_project, DROP INDEX credential_teams_team_project;
ALTER TABLE credential_teams ADD CONSTRAINT credential_teams_team FOREIGN KEY (team_id) REFERENCES teams (id) ON DELETE CASCADE;
ALTER TABLE credential_teams DROP FOREIGN KEY credential_teams_credential_project, DROP INDEX credential_teams_credential_project;
ALTER TABLE credential_teams ADD CONSTRAINT credential_teams_credential FOREIGN KEY (credential_id) REFERENCES credentials (id) ON DELETE CASCADE;
ALTER TABLE connections DROP FOREIGN KEY connections_target_project, DROP INDEX connections_target_project;
ALTER TABLE connections ADD CONSTRAINT connections_ibfk_2 FOREIGN KEY (target_id) REFERENCES processes (id) ON DELETE CASCADE;
ALTER TABLE connections DROP FOREIGN KEY connections_source_project, DROP INDEX connections_source_project;
ALTER TABLE connections ADD CONSTRAINT connections_ibfk_1 FOREIGN KEY (source_id) REFERENCES processes (id) ON DELETE CASCADE;
ALTER TABLE board_viewers DROP FOREIGN KEY board_viewers_flow_project, DROP INDEX board_viewers_flow_project;
ALTER TABLE board_viewers ADD CONSTRAINT board_viewers_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE;
ALTER TABLE board_changes DROP FOREIGN KEY board_changes_flow_project, DROP INDEX board_changes_flow_project;
ALTER TABLE board_changes ADD CONSTRAINT board_changes_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE;
ALTER TABLE assistant_messages DROP FOREIGN KEY assistant_messages_flow_project, DROP INDEX assistant_messages_flow_project;
ALTER TABLE assistant_messages ADD CONSTRAINT assistant_messages_ibfk_1 FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE;
ALTER TABLE api_keys DROP FOREIGN KEY api_keys_member, DROP INDEX api_keys_member;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_ibfk_1 FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;

SET FOREIGN_KEY_CHECKS = 1;
