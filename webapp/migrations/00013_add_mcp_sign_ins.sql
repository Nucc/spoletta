-- MCP sign-ins under way, by their OAuth state's SHA-256, so the sign-in
-- page coming back to any replica of the webapp finishes them; see mcp.go.

-- +goose Up
CREATE TABLE mcp_sign_ins (
  state_hash char(64) NOT NULL,
  server_id bigint NOT NULL,
  user_id bigint NOT NULL,
  verifier text NOT NULL,
  redirect_uri text NOT NULL,
  client_id varchar(255) NOT NULL,
  token_endpoint text NOT NULL,
  resource text NOT NULL,
  expires_at timestamp(3) NOT NULL,
  PRIMARY KEY (state_hash)
);

-- +goose Down
-- Sign-ins under way are lost; their people sign in again.
DROP TABLE mcp_sign_ins;
