-- The model agent processes running with Claude Code use without a model of
-- their own, picked in the settings beside the default model; empty takes
-- the default model when Claude Code can use it. See claudeDefaultModel in
-- model_providers.go.

-- +goose Up
ALTER TABLE model_default
  ADD COLUMN claude_model varchar(512) NOT NULL DEFAULT '';

-- +goose Down
-- The default model picked for Claude Code is lost: its processes without a
-- model of their own take the default model again, failing when Claude Code
-- can't use it.
ALTER TABLE model_default
  DROP COLUMN claude_model;
