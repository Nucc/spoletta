-- The schema as it stood when migrations moved here, made whole for a new
-- database. A database the webapp kept before then has all of it already,
-- brought up to date by 00001, and skips every table.
--
-- Tables refer to each other in circles (flows to flow_versions and back),
-- so foreign keys go unchecked while they are made. The migration runs in a
-- transaction, on one connection, which turns the checks back on at the end.

-- +goose Up
SET FOREIGN_KEY_CHECKS = 0;

-- Keys other systems start flows with (api.go), known by their hash.
CREATE TABLE IF NOT EXISTS `api_keys` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `user_id` bigint DEFAULT NULL,
  `name` varchar(255) NOT NULL,
  `prefix` varchar(16) NOT NULL,
  `token_hash` char(64) NOT NULL,
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `last_used_at` timestamp(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `api_keys_token` (`token_hash`),
  KEY `user_id` (`user_id`),
  CONSTRAINT `api_keys_ibfk_1` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS `assistant_messages` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `flow_id` bigint NOT NULL,
  `role` varchar(16) NOT NULL,
  `content` longtext NOT NULL,
  `status` varchar(16) NOT NULL,
  `error` text NOT NULL,
  `applied` tinyint(1) NOT NULL DEFAULT '0',
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `finished_at` timestamp(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `flow_id` (`flow_id`),
  CONSTRAINT `assistant_messages_ibfk_1` FOREIGN KEY (`flow_id`) REFERENCES `flows` (`id`) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS `connections` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `source_id` bigint NOT NULL,
  `target_id` bigint NOT NULL,
  `kind` varchar(16) NOT NULL DEFAULT '',
  PRIMARY KEY (`id`),
  UNIQUE KEY `source_target` (`source_id`,`target_id`),
  KEY `target_id` (`target_id`),
  CONSTRAINT `connections_ibfk_1` FOREIGN KEY (`source_id`) REFERENCES `processes` (`id`) ON DELETE CASCADE,
  CONSTRAINT `connections_ibfk_2` FOREIGN KEY (`target_id`) REFERENCES `processes` (`id`) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS `flow_history` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `flow_id` bigint NOT NULL,
  `snapshot` json NOT NULL,
  `undone` tinyint(1) NOT NULL DEFAULT '0',
  PRIMARY KEY (`id`),
  KEY `flow_history_steps` (`flow_id`,`undone`,`id`),
  CONSTRAINT `flow_history_ibfk_1` FOREIGN KEY (`flow_id`) REFERENCES `flows` (`id`) ON DELETE CASCADE
);
-- The start of a flow's top board, with the columns of inner_boards' start,
-- and its end, with those of inner_boards' end: null until moved, when the
-- board places it below what is on it.
CREATE TABLE IF NOT EXISTS `flow_starts` (
  `flow_id` bigint NOT NULL,
  `x` double NOT NULL DEFAULT '0',
  `y` double NOT NULL DEFAULT '0',
  `target_id` bigint DEFAULT NULL,
  `route` json DEFAULT NULL,
  `end_x` double DEFAULT NULL,
  `end_y` double DEFAULT NULL,
  PRIMARY KEY (`flow_id`),
  KEY `target_id` (`target_id`),
  CONSTRAINT `flow_starts_ibfk_1` FOREIGN KEY (`flow_id`) REFERENCES `flows` (`id`) ON DELETE CASCADE,
  CONSTRAINT `flow_starts_ibfk_2` FOREIGN KEY (`target_id`) REFERENCES `processes` (`id`) ON DELETE SET NULL
);
CREATE TABLE IF NOT EXISTS `flow_versions` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `based_on` bigint DEFAULT NULL,
  `snapshot` json NOT NULL,
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `flow_id` bigint NOT NULL,
  PRIMARY KEY (`id`),
  KEY `flow_versions_flow` (`flow_id`),
  CONSTRAINT `flow_versions_flow` FOREIGN KEY (`flow_id`) REFERENCES `flows` (`id`)
);
-- version_id is the version the board was last saved as or jumped to. A flow
-- is global, a team's or a user's, by its owner columns (owners.go).
CREATE TABLE IF NOT EXISTS `flows` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `name` varchar(255) NOT NULL,
  `version_id` bigint DEFAULT NULL,
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `settings` json DEFAULT NULL,
  `module` tinyint(1) NOT NULL DEFAULT '0',
  `is_struct` tinyint(1) NOT NULL DEFAULT '0',
  `owner_user_id` bigint DEFAULT NULL,
  `owner_team_id` bigint DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `version_id` (`version_id`),
  KEY `flows_owner_user` (`owner_user_id`),
  KEY `flows_owner_team` (`owner_team_id`),
  CONSTRAINT `flows_ibfk_1` FOREIGN KEY (`version_id`) REFERENCES `flow_versions` (`id`) ON DELETE SET NULL,
  CONSTRAINT `flows_owner_team` FOREIGN KEY (`owner_team_id`) REFERENCES `teams` (`id`),
  CONSTRAINT `flows_owner_user` FOREIGN KEY (`owner_user_id`) REFERENCES `users` (`id`)
);
CREATE TABLE IF NOT EXISTS `inner_boards` (
  `process_id` bigint NOT NULL,
  `x` double NOT NULL DEFAULT '0',
  `y` double NOT NULL DEFAULT '0',
  `target_id` bigint DEFAULT NULL,
  `route` json DEFAULT NULL,
  `end_x` double NOT NULL DEFAULT '0',
  `end_y` double NOT NULL DEFAULT '0',
  PRIMARY KEY (`process_id`),
  KEY `target_id` (`target_id`),
  CONSTRAINT `inner_boards_ibfk_1` FOREIGN KEY (`process_id`) REFERENCES `processes` (`id`) ON DELETE CASCADE,
  CONSTRAINT `inner_boards_ibfk_2` FOREIGN KEY (`target_id`) REFERENCES `processes` (`id`) ON DELETE SET NULL
);
-- Unique values are stored as a hash, whose primary key lets only one
-- instance hold each; the value itself is kept to say what collided.
CREATE TABLE IF NOT EXISTS `instance_keys` (
  `object_id` bigint NOT NULL,
  `attribute_id` bigint NOT NULL,
  `value_hash` char(64) NOT NULL,
  `value` text NOT NULL,
  `instance_id` bigint NOT NULL,
  PRIMARY KEY (`object_id`,`attribute_id`,`value_hash`),
  KEY `instance_id` (`instance_id`),
  CONSTRAINT `instance_keys_ibfk_1` FOREIGN KEY (`instance_id`) REFERENCES `object_instances` (`id`) ON DELETE CASCADE
);
-- The states a human's output link decides between.
CREATE TABLE IF NOT EXISTS `link_states` (
  `link_id` bigint NOT NULL,
  `state_id` bigint NOT NULL,
  PRIMARY KEY (`link_id`,`state_id`),
  KEY `state_id` (`state_id`),
  CONSTRAINT `link_states_ibfk_1` FOREIGN KEY (`link_id`) REFERENCES `object_links` (`id`) ON DELETE CASCADE,
  CONSTRAINT `link_states_ibfk_2` FOREIGN KEY (`state_id`) REFERENCES `object_states` (`id`) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS `mcp_servers` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `name` varchar(64) NOT NULL,
  `url` varchar(2048) NOT NULL,
  `description` varchar(1024) NOT NULL DEFAULT '',
  `client_id` varchar(255) NOT NULL DEFAULT '',
  `redirect_uri` varchar(2048) NOT NULL DEFAULT '',
  `token_endpoint` varchar(2048) NOT NULL DEFAULT '',
  `access_token` text,
  `refresh_token` text,
  `expires_at` datetime DEFAULT NULL,
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `auth` varchar(16) NOT NULL DEFAULT 'oauth',
  `connector` varchar(64) NOT NULL DEFAULT '',
  PRIMARY KEY (`id`),
  UNIQUE KEY `name` (`name`)
);
-- Each person's connection to a server; the user 0, whom nobody is, holds
-- the one everyone uses without sign-in.
CREATE TABLE IF NOT EXISTS `mcp_tokens` (
  `server_id` bigint NOT NULL,
  `user_id` bigint NOT NULL,
  `access_token` text,
  `refresh_token` text,
  `expires_at` datetime DEFAULT NULL,
  PRIMARY KEY (`server_id`,`user_id`),
  CONSTRAINT `mcp_tokens_ibfk_1` FOREIGN KEY (`server_id`) REFERENCES `mcp_servers` (`id`) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS `model_default` (
  `id` tinyint NOT NULL,
  `model` varchar(512) NOT NULL,
  PRIMARY KEY (`id`)
);
CREATE TABLE IF NOT EXISTS `model_keys` (
  `env` varchar(255) NOT NULL,
  `api_key` text NOT NULL,
  `updated_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`env`)
);
CREATE TABLE IF NOT EXISTS `model_providers` (
  `id` varchar(64) NOT NULL,
  `name` varchar(255) NOT NULL,
  `api` varchar(64) NOT NULL DEFAULT '',
  `base_url` text NOT NULL,
  `key_source` varchar(8) NOT NULL,
  `env` varchar(255) NOT NULL,
  `api_key` text NOT NULL,
  `models` json NOT NULL,
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`)
);
CREATE TABLE IF NOT EXISTS `object_attributes` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `object_id` bigint NOT NULL,
  `name` varchar(255) NOT NULL,
  `definition` text NOT NULL,
  `is_unique` tinyint(1) NOT NULL DEFAULT '0',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `is_file` tinyint(1) NOT NULL DEFAULT '0',
  `source_id` bigint DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `object_name` (`object_id`,`name`),
  CONSTRAINT `object_attributes_ibfk_1` FOREIGN KEY (`object_id`) REFERENCES `objects` (`id`) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS `object_instances` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `object_id` bigint NOT NULL,
  `run_id` bigint NOT NULL,
  `step_id` bigint NOT NULL,
  `content` longtext NOT NULL,
  `state` varchar(255) NOT NULL DEFAULT '',
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `object_run` (`object_id`,`run_id`),
  KEY `run_id` (`run_id`),
  KEY `step_id` (`step_id`),
  CONSTRAINT `object_instances_ibfk_1` FOREIGN KEY (`run_id`) REFERENCES `runs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `object_instances_ibfk_2` FOREIGN KEY (`step_id`) REFERENCES `run_steps` (`id`) ON DELETE CASCADE
);
-- A link's state_id is 0 when it names no state; a NULL would let the unique
-- key take the same link twice.
CREATE TABLE IF NOT EXISTS `object_links` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `process_id` bigint NOT NULL,
  `object_id` bigint NOT NULL,
  `direction` varchar(8) NOT NULL,
  `state_id` bigint NOT NULL DEFAULT '0',
  `route` json DEFAULT NULL,
  `at_x` double DEFAULT NULL,
  `at_y` double DEFAULT NULL,
  `role` varchar(16) NOT NULL DEFAULT '',
  `is_condition` tinyint(1) NOT NULL DEFAULT '0',
  `query` json DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `process_object_direction_state` (`process_id`,`object_id`,`direction`,`state_id`),
  KEY `object_id` (`object_id`),
  CONSTRAINT `object_links_ibfk_1` FOREIGN KEY (`process_id`) REFERENCES `processes` (`id`) ON DELETE CASCADE,
  CONSTRAINT `object_links_ibfk_2` FOREIGN KEY (`object_id`) REFERENCES `objects` (`id`) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS `object_states` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `object_id` bigint NOT NULL,
  `name` varchar(255) NOT NULL,
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `rule` text,
  `source_id` bigint DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `object_id` (`object_id`),
  CONSTRAINT `object_states_ibfk_1` FOREIGN KEY (`object_id`) REFERENCES `objects` (`id`) ON DELETE CASCADE
);
-- A state's or attribute's source_id has no foreign key: going back to a
-- version rewrites them under the same ids, which a key would forget.
-- store_id is the shared object whose records this one reads and writes: a
-- shared struct's own object for itself, and for its copies. relation is how
-- an owned object makes up its owner, an attribute it exhibits or a part it
-- aggregates; empty for other objects. max_items is null for no most.
CREATE TABLE IF NOT EXISTS `objects` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `name` varchar(255) NOT NULL,
  `x` double NOT NULL DEFAULT '0',
  `y` double NOT NULL DEFAULT '0',
  `width` double DEFAULT NULL,
  `persistent` tinyint(1) NOT NULL DEFAULT '0',
  `many` tinyint(1) NOT NULL DEFAULT '0',
  `min_items` int NOT NULL DEFAULT '0',
  `max_items` int DEFAULT NULL,
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `human` tinyint(1) NOT NULL DEFAULT '0',
  `at_end` tinyint(1) NOT NULL DEFAULT '0',
  `parent_id` bigint DEFAULT NULL,
  `owner_id` bigint DEFAULT NULL,
  `general_id` bigint DEFAULT NULL,
  `relation` varchar(16) NOT NULL DEFAULT '',
  `flow_id` bigint NOT NULL,
  `source_id` bigint DEFAULT NULL,
  `store_id` bigint DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `objects_parent` (`parent_id`),
  KEY `objects_owner` (`owner_id`),
  KEY `objects_general` (`general_id`),
  KEY `objects_flow` (`flow_id`),
  KEY `objects_source` (`source_id`),
  CONSTRAINT `objects_flow` FOREIGN KEY (`flow_id`) REFERENCES `flows` (`id`),
  CONSTRAINT `objects_general` FOREIGN KEY (`general_id`) REFERENCES `objects` (`id`) ON DELETE SET NULL,
  CONSTRAINT `objects_owner` FOREIGN KEY (`owner_id`) REFERENCES `objects` (`id`) ON DELETE CASCADE,
  CONSTRAINT `objects_parent` FOREIGN KEY (`parent_id`) REFERENCES `processes` (`id`) ON DELETE CASCADE,
  CONSTRAINT `objects_source` FOREIGN KEY (`source_id`) REFERENCES `objects` (`id`) ON DELETE SET NULL
);
CREATE TABLE IF NOT EXISTS `processes` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `name` varchar(255) NOT NULL,
  `kind` varchar(16) NOT NULL DEFAULT 'agent',
  `prompt` text NOT NULL,
  `script` text NOT NULL,
  `model` varchar(255) NOT NULL DEFAULT '',
  `thinking` varchar(16) NOT NULL DEFAULT '',
  `x` double NOT NULL DEFAULT '0',
  `y` double NOT NULL DEFAULT '0',
  `width` double DEFAULT NULL,
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `settings` json DEFAULT NULL,
  `parent_id` bigint DEFAULT NULL,
  `recording` tinyint(1) NOT NULL DEFAULT '0',
  `flow_id` bigint NOT NULL,
  PRIMARY KEY (`id`),
  KEY `processes_parent` (`parent_id`),
  KEY `processes_flow` (`flow_id`),
  CONSTRAINT `processes_flow` FOREIGN KEY (`flow_id`) REFERENCES `flows` (`id`),
  CONSTRAINT `processes_parent` FOREIGN KEY (`parent_id`) REFERENCES `processes` (`id`) ON DELETE CASCADE
);
-- object_ref outlives the object, as run_steps' process_ref does.
CREATE TABLE IF NOT EXISTS `run_objects` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `run_id` bigint NOT NULL,
  `step_id` bigint NOT NULL,
  `object_id` bigint DEFAULT NULL,
  `object_ref` bigint DEFAULT NULL,
  `name` varchar(255) NOT NULL,
  `state` varchar(255) NOT NULL DEFAULT '',
  `content` longtext NOT NULL,
  PRIMARY KEY (`id`),
  KEY `run_id` (`run_id`),
  KEY `step_id` (`step_id`),
  KEY `object_id` (`object_id`),
  CONSTRAINT `run_objects_ibfk_1` FOREIGN KEY (`run_id`) REFERENCES `runs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `run_objects_ibfk_2` FOREIGN KEY (`step_id`) REFERENCES `run_steps` (`id`) ON DELETE CASCADE,
  CONSTRAINT `run_objects_ibfk_3` FOREIGN KEY (`object_id`) REFERENCES `objects` (`id`) ON DELETE SET NULL
);
CREATE TABLE IF NOT EXISTS `run_step_followups` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `step_id` bigint NOT NULL,
  `attempt` int NOT NULL,
  `message` text NOT NULL,
  `status` varchar(16) NOT NULL,
  `output` longtext NOT NULL,
  `error` text NOT NULL,
  `commits` json DEFAULT NULL,
  `started_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `finished_at` timestamp(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `step_id` (`step_id`),
  CONSTRAINT `run_step_followups_ibfk_1` FOREIGN KEY (`step_id`) REFERENCES `run_steps` (`id`) ON DELETE CASCADE
);
-- The conversation of an agent step, a message per row; see messages.go.
CREATE TABLE IF NOT EXISTS `run_step_messages` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `step_id` bigint NOT NULL,
  `attempt` int NOT NULL,
  `message` json NOT NULL,
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `step_id` (`step_id`),
  CONSTRAINT `run_step_messages_ibfk_1` FOREIGN KEY (`step_id`) REFERENCES `run_steps` (`id`) ON DELETE CASCADE
);
-- process_ref outlives the process: deleting it, or jumping to a version
-- without it, nulls process_id, yet the run's version still has it.
CREATE TABLE IF NOT EXISTS `run_steps` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `run_id` bigint NOT NULL,
  `process_id` bigint DEFAULT NULL,
  `process_ref` bigint DEFAULT NULL,
  `process_name` varchar(255) NOT NULL,
  `kind` varchar(16) NOT NULL,
  `model` varchar(255) NOT NULL DEFAULT '',
  `thinking` varchar(16) NOT NULL DEFAULT '',
  `prompt` longtext NOT NULL,
  `inputs` json NOT NULL,
  `change_request` json DEFAULT NULL,
  `status` varchar(16) NOT NULL,
  `output` longtext NOT NULL,
  `error` text NOT NULL,
  `outcome` varchar(255) NOT NULL DEFAULT '',
  `instructions` text NOT NULL,
  `decided_by` bigint DEFAULT NULL,
  `child_run_id` bigint DEFAULT NULL,
  `started_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `finished_at` timestamp(3) NULL DEFAULT NULL,
  `branch` varchar(255) NOT NULL DEFAULT '',
  `commits` json DEFAULT NULL,
  `session` json DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `run_id` (`run_id`),
  KEY `process_id` (`process_id`),
  CONSTRAINT `run_steps_ibfk_1` FOREIGN KEY (`run_id`) REFERENCES `runs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `run_steps_ibfk_2` FOREIGN KEY (`process_id`) REFERENCES `processes` (`id`) ON DELETE SET NULL
);
-- Processes and objects can be deleted later; their history stays. Each run
-- says which schedule, person or API key started it; an API key's caller's
-- idempotency_key makes a request sent again start no second run. starts is,
-- for a re-run, the commit each repository's branch started from.
CREATE TABLE IF NOT EXISTS `runs` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `status` varchar(16) NOT NULL,
  `version_id` bigint DEFAULT NULL,
  `parent_run_id` bigint DEFAULT NULL,
  `process_ref` bigint DEFAULT NULL,
  `process_name` varchar(255) NOT NULL DEFAULT '',
  `rerun_of` bigint DEFAULT NULL,
  `rerun_from` bigint DEFAULT NULL,
  `rerun_from_name` varchar(255) NOT NULL DEFAULT '',
  `starts` json DEFAULT NULL,
  `item_key` varchar(255) NOT NULL DEFAULT '',
  `schedule_id` bigint DEFAULT NULL,
  `started_by` bigint DEFAULT NULL,
  `started_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `finished_at` timestamp(3) NULL DEFAULT NULL,
  `flow_id` bigint NOT NULL,
  `api_key_id` bigint DEFAULT NULL,
  `idempotency_key` varchar(255) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `runs_idempotency` (`api_key_id`,`idempotency_key`),
  KEY `runs_flow` (`flow_id`),
  KEY `runs_schedule` (`schedule_id`),
  CONSTRAINT `runs_flow` FOREIGN KEY (`flow_id`) REFERENCES `flows` (`id`),
  CONSTRAINT `runs_schedule` FOREIGN KEY (`schedule_id`) REFERENCES `schedules` (`id`) ON DELETE SET NULL
);
-- Times are kept as UTC, in DATETIME so MySQL's session time zone doesn't
-- move them. created_by is whose schedule it is: its runs use their model keys
-- and MCP connections; see runUser.
CREATE TABLE IF NOT EXISTS `schedules` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `flow_id` bigint NOT NULL,
  `cron` varchar(255) NOT NULL,
  `timezone` varchar(64) NOT NULL,
  `enabled` tinyint(1) NOT NULL DEFAULT '1',
  `next_run_at` datetime(3) DEFAULT NULL,
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `created_by` bigint DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `flow_id` (`flow_id`),
  KEY `schedules_due` (`enabled`,`next_run_at`),
  CONSTRAINT `schedules_ibfk_1` FOREIGN KEY (`flow_id`) REFERENCES `flows` (`id`) ON DELETE CASCADE
);
-- A session is known by its token's hash, so the table doesn't hold what
-- signs anyone in.
CREATE TABLE IF NOT EXISTS `sessions` (
  `token_hash` char(64) NOT NULL,
  `user_id` bigint NOT NULL,
  `expires_at` timestamp(3) NOT NULL,
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`token_hash`),
  KEY `sessions_expiry` (`expires_at`),
  KEY `user_id` (`user_id`),
  CONSTRAINT `sessions_ibfk_1` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS `shared_keys` (
  `object_id` bigint NOT NULL,
  `attribute_id` bigint NOT NULL,
  `value_hash` char(64) NOT NULL,
  `record_id` bigint NOT NULL,
  PRIMARY KEY (`object_id`,`attribute_id`,`value_hash`),
  KEY `record_id` (`record_id`),
  CONSTRAINT `shared_keys_ibfk_1` FOREIGN KEY (`record_id`) REFERENCES `shared_records` (`id`) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS `shared_records` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `object_id` bigint NOT NULL,
  `content` longtext NOT NULL,
  `state` varchar(255) NOT NULL DEFAULT '',
  `run_id` bigint DEFAULT NULL,
  `step_id` bigint DEFAULT NULL,
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `edited_by` bigint DEFAULT NULL,
  `edited_at` timestamp(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `object_updated` (`object_id`,`updated_at`),
  KEY `step` (`step_id`),
  KEY `run_id` (`run_id`),
  CONSTRAINT `shared_records_ibfk_1` FOREIGN KEY (`run_id`) REFERENCES `runs` (`id`) ON DELETE SET NULL,
  CONSTRAINT `shared_records_ibfk_2` FOREIGN KEY (`step_id`) REFERENCES `run_steps` (`id`) ON DELETE SET NULL
);
CREATE TABLE IF NOT EXISTS `skills` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `kind` varchar(16) NOT NULL,
  `name` varchar(64) NOT NULL,
  `description` text NOT NULL,
  `argument_hint` varchar(255) NOT NULL DEFAULT '',
  `content` mediumtext NOT NULL,
  `owner_user_id` bigint DEFAULT NULL,
  `owner_team_id` bigint DEFAULT NULL,
  `updated_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `skills_name` (`kind`,`name`),
  KEY `owner_user_id` (`owner_user_id`),
  KEY `owner_team_id` (`owner_team_id`),
  CONSTRAINT `skills_ibfk_1` FOREIGN KEY (`owner_user_id`) REFERENCES `users` (`id`),
  CONSTRAINT `skills_ibfk_2` FOREIGN KEY (`owner_team_id`) REFERENCES `teams` (`id`)
);
CREATE TABLE IF NOT EXISTS `team_members` (
  `team_id` bigint NOT NULL,
  `user_id` bigint NOT NULL,
  PRIMARY KEY (`team_id`,`user_id`),
  KEY `team_members_user` (`user_id`),
  CONSTRAINT `team_members_ibfk_1` FOREIGN KEY (`team_id`) REFERENCES `teams` (`id`) ON DELETE CASCADE,
  CONSTRAINT `team_members_ibfk_2` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS `teams` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `name` varchar(255) NOT NULL,
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `teams_name` (`name`)
);
CREATE TABLE IF NOT EXISTS `user_identities` (
  `issuer` varchar(255) NOT NULL,
  `subject` varchar(255) NOT NULL,
  `user_id` bigint NOT NULL,
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`issuer`,`subject`),
  KEY `user_identities_user` (`user_id`),
  CONSTRAINT `user_identities_ibfk_1` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS `user_roles` (
  `user_id` bigint NOT NULL,
  `name` varchar(255) NOT NULL,
  PRIMARY KEY (`user_id`,`name`),
  CONSTRAINT `user_roles_ibfk_1` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
);
-- Someone an admin added has no subject until they first sign in; removed_at
-- is set for someone an admin removed. issuer and subject are only what users
-- had before user_identities, which says who a user is to each provider.
CREATE TABLE IF NOT EXISTS `users` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `email` varchar(320) NOT NULL,
  `issuer` varchar(255) NOT NULL DEFAULT '',
  `subject` varchar(255) DEFAULT NULL,
  `name` varchar(255) NOT NULL DEFAULT '',
  `admin` tinyint(1) NOT NULL DEFAULT '0',
  `team_manager` tinyint(1) NOT NULL DEFAULT '0',
  `created_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `signed_in_at` timestamp(3) NULL DEFAULT NULL,
  `removed_at` timestamp(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `users_email` (`email`),
  UNIQUE KEY `users_subject` (`issuer`,`subject`)
);

SET FOREIGN_KEY_CHECKS = 1;
