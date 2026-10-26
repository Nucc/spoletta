-- Credentials scripts and agents use without seeing them: a service's base
-- URL, how its secret goes on each request, and the secret, sealed with
-- SECRET_KEY; who may use each, everyone or the teams given it. See
-- credentials.go.

-- +goose Up
CREATE TABLE credentials (
  id bigint NOT NULL AUTO_INCREMENT,
  name varchar(64) NOT NULL,
  variable varchar(64) NOT NULL,
  description text NOT NULL,
  base_url varchar(2048) NOT NULL,
  auth varchar(16) NOT NULL,
  header varchar(64) NOT NULL DEFAULT '',
  template varchar(255) NOT NULL DEFAULT '',
  username varchar(255) NOT NULL DEFAULT '',
  param varchar(64) NOT NULL DEFAULT '',
  secret text NOT NULL,
  everyone boolean NOT NULL DEFAULT TRUE,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY credentials_name (name),
  UNIQUE KEY credentials_variable (variable)
);

CREATE TABLE credential_teams (
  credential_id bigint NOT NULL,
  team_id bigint NOT NULL,
  PRIMARY KEY (credential_id, team_id),
  KEY credential_teams_team (team_id),
  CONSTRAINT credential_teams_credential FOREIGN KEY (credential_id) REFERENCES credentials (id) ON DELETE CASCADE,
  CONSTRAINT credential_teams_team FOREIGN KEY (team_id) REFERENCES teams (id) ON DELETE CASCADE
);

-- +goose Down
-- The credentials go, their secrets with them; processes picking them keep
-- their ids in their settings, which an older webapp doesn't read.
DROP TABLE credential_teams;
DROP TABLE credentials;
