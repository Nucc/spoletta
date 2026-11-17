-- The groups people are in at the identity provider (user_groups) are read
-- as they sign in, and again while a session lives, so someone taken out of
-- a group loses what it gave them soon after; see users.go. A person keeps
-- when their groups were last read (groups_read_at): a session of someone
-- with groups read longer ago than GROUPS_MAX_AGE is over. Groups put there
-- otherwise than by signing in, as by hand, have none, and aren't aged.
--
-- A session signed in with the provider whose sign-in carries groups keeps
-- what it needs to read them again: the issuer it signed in with, its
-- refresh token and access token, sealed with SECRET_KEY as other secrets
-- are (secrets.go), when its access token expires, where the groups were
-- found (the ID token or userinfo), and when to read them next, which a
-- replica claims by moving it on.

-- +goose Up
ALTER TABLE users
  ADD COLUMN groups_read_at timestamp(3) NULL DEFAULT NULL;
ALTER TABLE sessions
  ADD COLUMN issuer varchar(255) NOT NULL DEFAULT '',
  ADD COLUMN refresh_token text NULL,
  ADD COLUMN access_token text NULL,
  ADD COLUMN access_expires_at timestamp(3) NULL DEFAULT NULL,
  ADD COLUMN groups_from varchar(16) NOT NULL DEFAULT '',
  ADD COLUMN groups_check_at timestamp(3) NULL DEFAULT NULL,
  ADD KEY sessions_groups_check (groups_check_at);

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

-- The sessions' tokens go, so their groups are read again only as their
-- people next sign in, and when people's groups were last read.
ALTER TABLE sessions
  DROP KEY sessions_groups_check,
  DROP COLUMN groups_check_at,
  DROP COLUMN groups_from,
  DROP COLUMN access_expires_at,
  DROP COLUMN access_token,
  DROP COLUMN refresh_token,
  DROP COLUMN issuer;
ALTER TABLE users
  DROP COLUMN groups_read_at;
