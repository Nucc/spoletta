-- What each call read from and wrote to its provider's cache, and what it
-- cost in millionths of a dollar, worked out as it was recorded; see
-- spend.go. priced is false where the model had no known price and was
-- counted at the price of the largest ones, and for calls recorded before
-- costs were, which cost nothing here.

-- +goose Up
ALTER TABLE llm_usage
  ADD COLUMN cache_read_tokens bigint NOT NULL DEFAULT 0,
  ADD COLUMN cache_write_tokens bigint NOT NULL DEFAULT 0,
  ADD COLUMN cost_micros bigint NOT NULL DEFAULT 0,
  ADD COLUMN priced tinyint(1) NOT NULL DEFAULT 0;

-- +goose Down
-- What calls read from and wrote to the cache, and what they cost, goes.
ALTER TABLE llm_usage
  DROP COLUMN cache_read_tokens,
  DROP COLUMN cache_write_tokens,
  DROP COLUMN cost_micros,
  DROP COLUMN priced;
