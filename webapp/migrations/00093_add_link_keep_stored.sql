-- Whether a process only generating a shared struct's object adds only the
-- records that are new, leaving those already stored as they are, rather
-- than refreshing their fields (shared.go): an import fetching tickets
-- again then leaves the tickets it imported before alone, while another
-- flow syncing them refreshes them. It is the output link's, so each
-- process writing the store says so for itself.

-- +goose Up
ALTER TABLE object_links ADD COLUMN keep_stored BOOLEAN NOT NULL DEFAULT FALSE;

-- Going down loses which links keep the records already stored, so those
-- processes refresh them again.
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

ALTER TABLE object_links DROP COLUMN keep_stored;
