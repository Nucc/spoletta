-- What other rows refer to is unique by its id and its project too, so a
-- row referring to it can name both (00053) and be in no other project
-- than it. What no other row of a project holds to names a project there
-- is: flows, task boards and the project's settings, and what is kept of
-- it apart from them, such as its audit log and what its models cost.
--
-- Foreign keys go unchecked while they are made, as all there is is
-- Legacy's; with checks on MySQL would copy each table whole to add one.

-- +goose Up
SET FOREIGN_KEY_CHECKS = 0;

ALTER TABLE flows ADD UNIQUE KEY flows_id_project (id, project_id);
ALTER TABLE flow_versions ADD UNIQUE KEY flow_versions_id_project (id, project_id);
ALTER TABLE processes ADD UNIQUE KEY processes_id_project (id, project_id);
ALTER TABLE objects ADD UNIQUE KEY objects_id_project (id, project_id);
ALTER TABLE object_links ADD UNIQUE KEY object_links_id_project (id, project_id);
ALTER TABLE object_states ADD UNIQUE KEY object_states_id_project (id, project_id);
ALTER TABLE object_instances ADD UNIQUE KEY object_instances_id_project (id, project_id);
ALTER TABLE runs ADD UNIQUE KEY runs_id_project (id, project_id);
ALTER TABLE run_steps ADD UNIQUE KEY run_steps_id_project (id, project_id);
ALTER TABLE schedules ADD UNIQUE KEY schedules_id_project (id, project_id);
ALTER TABLE shared_records ADD UNIQUE KEY shared_records_id_project (id, project_id);
ALTER TABLE teams ADD UNIQUE KEY teams_id_project (id, project_id);
ALTER TABLE credentials ADD UNIQUE KEY credentials_id_project (id, project_id);
ALTER TABLE mcp_servers ADD UNIQUE KEY mcp_servers_id_project (id, project_id);
ALTER TABLE task_boards ADD UNIQUE KEY task_boards_id_project (id, project_id);
ALTER TABLE webhooks ADD UNIQUE KEY webhooks_id_project (id, project_id);

ALTER TABLE flows ADD KEY flows_project (project_id),
  ADD CONSTRAINT flows_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE task_boards ADD KEY task_boards_project (project_id),
  ADD CONSTRAINT task_boards_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE teams ADD CONSTRAINT teams_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE mcp_servers ADD CONSTRAINT mcp_servers_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE skills ADD CONSTRAINT skills_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE credentials ADD CONSTRAINT credentials_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE model_providers ADD CONSTRAINT model_providers_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE model_keys ADD CONSTRAINT model_keys_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE model_default ADD CONSTRAINT model_default_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE limits ADD CONSTRAINT limits_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE egress_hosts ADD CONSTRAINT egress_hosts_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE api_keys ADD KEY api_keys_project (project_id),
  ADD CONSTRAINT api_keys_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE shared_records ADD KEY shared_records_project (project_id),
  ADD CONSTRAINT shared_records_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE audit_events ADD KEY audit_events_project (project_id),
  ADD CONSTRAINT audit_events_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE llm_usage ADD KEY llm_usage_project (project_id),
  ADD CONSTRAINT llm_usage_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE proxy_grants ADD KEY proxy_grants_project (project_id),
  ADD CONSTRAINT proxy_grants_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE person_slots ADD KEY person_slots_project (project_id),
  ADD CONSTRAINT person_slots_project FOREIGN KEY (project_id) REFERENCES projects (id);
ALTER TABLE mcp_sign_ins ADD KEY mcp_sign_ins_project (project_id),
  ADD CONSTRAINT mcp_sign_ins_project FOREIGN KEY (project_id) REFERENCES projects (id);

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

ALTER TABLE mcp_sign_ins DROP FOREIGN KEY mcp_sign_ins_project, DROP INDEX mcp_sign_ins_project;
ALTER TABLE person_slots DROP FOREIGN KEY person_slots_project, DROP INDEX person_slots_project;
ALTER TABLE proxy_grants DROP FOREIGN KEY proxy_grants_project, DROP INDEX proxy_grants_project;
ALTER TABLE llm_usage DROP FOREIGN KEY llm_usage_project, DROP INDEX llm_usage_project;
ALTER TABLE audit_events DROP FOREIGN KEY audit_events_project, DROP INDEX audit_events_project;
ALTER TABLE shared_records DROP FOREIGN KEY shared_records_project, DROP INDEX shared_records_project;
ALTER TABLE api_keys DROP FOREIGN KEY api_keys_project, DROP INDEX api_keys_project;
ALTER TABLE egress_hosts DROP FOREIGN KEY egress_hosts_project;
ALTER TABLE limits DROP FOREIGN KEY limits_project;
ALTER TABLE model_default DROP FOREIGN KEY model_default_project;
ALTER TABLE model_keys DROP FOREIGN KEY model_keys_project;
ALTER TABLE model_providers DROP FOREIGN KEY model_providers_project;
ALTER TABLE credentials DROP FOREIGN KEY credentials_project;
ALTER TABLE skills DROP FOREIGN KEY skills_project;
ALTER TABLE mcp_servers DROP FOREIGN KEY mcp_servers_project;
ALTER TABLE teams DROP FOREIGN KEY teams_project;
ALTER TABLE task_boards DROP FOREIGN KEY task_boards_project, DROP INDEX task_boards_project;
ALTER TABLE flows DROP FOREIGN KEY flows_project, DROP INDEX flows_project;

ALTER TABLE webhooks DROP INDEX webhooks_id_project;
ALTER TABLE task_boards DROP INDEX task_boards_id_project;
ALTER TABLE mcp_servers DROP INDEX mcp_servers_id_project;
ALTER TABLE credentials DROP INDEX credentials_id_project;
ALTER TABLE teams DROP INDEX teams_id_project;
ALTER TABLE shared_records DROP INDEX shared_records_id_project;
ALTER TABLE schedules DROP INDEX schedules_id_project;
ALTER TABLE run_steps DROP INDEX run_steps_id_project;
ALTER TABLE runs DROP INDEX runs_id_project;
ALTER TABLE object_instances DROP INDEX object_instances_id_project;
ALTER TABLE object_states DROP INDEX object_states_id_project;
ALTER TABLE object_links DROP INDEX object_links_id_project;
ALTER TABLE objects DROP INDEX objects_id_project;
ALTER TABLE processes DROP INDEX processes_id_project;
ALTER TABLE flow_versions DROP INDEX flow_versions_id_project;
ALTER TABLE flows DROP INDEX flows_id_project;

SET FOREIGN_KEY_CHECKS = 1;
