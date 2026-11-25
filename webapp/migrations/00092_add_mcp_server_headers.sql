-- Headers the proxy puts on every request to an MCP server, such as the
-- keys of a server that takes its API keys in headers of its own rather
-- than a bearer token (mcp.go). Its project's owners set them for
-- everyone in it; each value is sealed with SECRET_KEY (secrets.go) and
-- never shown again. A header is its server's, and goes with it.

-- +goose Up
CREATE TABLE mcp_server_headers (
  project_id BIGINT NOT NULL,
  server_id BIGINT NOT NULL,
  -- As the owner spelled it, which the proxy sends; HTTP's names are the
  -- same whatever their case, and so is each server's header here.
  name VARCHAR(128) CHARACTER SET ascii COLLATE ascii_general_ci NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (server_id, name),
  KEY mcp_server_headers_server_project (server_id, project_id),
  CONSTRAINT mcp_server_headers_server_project FOREIGN KEY (server_id, project_id) REFERENCES mcp_servers (id, project_id) ON DELETE CASCADE
);

-- Going down loses the headers set, and a server taking only headers
-- takes a token instead, which nobody has set, so its agents fail to reach
-- it until an owner sets one.
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

UPDATE mcp_servers SET auth = 'token' WHERE auth = 'headers';
DROP TABLE mcp_server_headers;
