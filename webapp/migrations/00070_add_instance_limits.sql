-- The ceilings the instance's admin sets over every project's limits
-- (limits.go): what a run may spend, what a person may spend a month in a
-- project, how many steps a person's runs have going at once in one, and
-- how much disk a run takes, a row each by name, none or 0 for no ceiling.
-- A project's owners set its own limits under them, which name its project
-- in limits; these are above the projects, so they name none.

-- +goose Up
CREATE TABLE instance_limits (
  name varchar(64) NOT NULL,
  value bigint NOT NULL,
  PRIMARY KEY (name)
);

-- +goose Down
-- Going down loses which project everything was in, so it is refused while
-- a project but Legacy is left: the row inserted fails its check, which
-- says so.
DROP TEMPORARY TABLE IF EXISTS projects_down;
CREATE TEMPORARY TABLE projects_down (others BIGINT NOT NULL,
  CONSTRAINT `delete every project but Legacy before migrating down` CHECK (others = 0));
INSERT INTO projects_down SELECT COUNT(*) FROM projects WHERE id <> 1;
DROP TEMPORARY TABLE projects_down;

-- The ceilings set go; Legacy's limits are the instance's again.
DROP TABLE instance_limits;
