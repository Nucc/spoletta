-- What each call an agent made to its model took in and gave out, as the
-- provider's answer said, for whom and which run and step; see llm.go. A
-- follow-up's or an assistant's calls have no run. Kept when the run is
-- deleted, as what was spent.

-- +goose Up
CREATE TABLE llm_usage (
  id bigint NOT NULL AUTO_INCREMENT,
  user_id bigint DEFAULT NULL,
  run_id bigint DEFAULT NULL,
  step_id bigint DEFAULT NULL,
  provider varchar(64) NOT NULL,
  model varchar(255) NOT NULL,
  input_tokens bigint NOT NULL,
  output_tokens bigint NOT NULL,
  at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY llm_usage_user (user_id, at),
  KEY llm_usage_step (step_id)
);

-- +goose Down
-- What the calls took goes.
DROP TABLE llm_usage;
