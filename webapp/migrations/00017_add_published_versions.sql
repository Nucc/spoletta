-- A flow's published version: the one runs nobody starts by hand from the
-- board take, such as a schedule's, the API's and those on records, while
-- the board is a draft; and when it was published, by whom. Each version
-- keeps why it was saved (empty when a person saved it; run, assistant or
-- import when saved on its own first), who saved it and the note they left.
-- An assistant's reply that changed its flow keeps the version saved before,
-- which the board shows what it changed against. See publish.go.

-- +goose Up
ALTER TABLE flows
  ADD COLUMN published_version_id bigint DEFAULT NULL,
  ADD COLUMN published_at timestamp(3) NULL DEFAULT NULL,
  ADD COLUMN published_by bigint DEFAULT NULL,
  ADD CONSTRAINT flows_published_version FOREIGN KEY (published_version_id) REFERENCES flow_versions (id) ON DELETE SET NULL;
ALTER TABLE flow_versions
  ADD COLUMN note varchar(2000) NOT NULL DEFAULT '',
  ADD COLUMN saved_by bigint DEFAULT NULL,
  ADD COLUMN reason varchar(32) NOT NULL DEFAULT '';
ALTER TABLE assistant_messages ADD COLUMN version_id bigint DEFAULT NULL;

-- +goose Down
-- Which version each flow published is lost, so its runs take its board as
-- it is again; so are the versions' notes and who saved them. The versions
-- stay, and so do the assistant's replies.
ALTER TABLE assistant_messages DROP COLUMN version_id;
ALTER TABLE flow_versions DROP COLUMN reason, DROP COLUMN saved_by, DROP COLUMN note;
ALTER TABLE flows DROP FOREIGN KEY flows_published_version;
ALTER TABLE flows DROP COLUMN published_by, DROP COLUMN published_at, DROP COLUMN published_version_id;
