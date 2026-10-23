-- The tokens steps are handed for the webapp's proxies, to their models,
-- MCP servers and the internet, kept here so that every replica of the
-- webapp knows them, by their SHA-256 alone; see grants.go.

-- +goose Up
CREATE TABLE proxy_grants (
  token_hash char(64) NOT NULL,
  kind varchar(16) NOT NULL,
  user_id bigint DEFAULT NULL,
  run_id bigint DEFAULT NULL,
  step_id bigint DEFAULT NULL,
  root_run_id bigint DEFAULT NULL,
  reaches json NOT NULL,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  expires_at timestamp(3) NOT NULL,
  PRIMARY KEY (token_hash),
  KEY proxy_grants_root (root_run_id),
  KEY proxy_grants_expires (expires_at)
);

-- +goose Down
-- The tokens going go, so the steps holding them reach none of the
-- proxies; an older webapp keeps them in its memory instead.
DROP TABLE proxy_grants;
