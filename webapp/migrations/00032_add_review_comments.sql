-- Comments on reviews: what those who may decide a review, or edit its
-- flow, say of it while it waits, each by a person, or nobody without
-- sign-in, kept with its step. See comments.go.

-- +goose Up
CREATE TABLE review_comments (
  id bigint NOT NULL AUTO_INCREMENT,
  run_id bigint NOT NULL,
  step_id bigint NOT NULL,
  user_id bigint DEFAULT NULL,
  text text NOT NULL,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY review_comments_run (run_id),
  KEY review_comments_step (step_id),
  CONSTRAINT review_comments_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE,
  CONSTRAINT review_comments_step FOREIGN KEY (step_id) REFERENCES run_steps (id) ON DELETE CASCADE
);

-- +goose Down
-- The comments on reviews are lost.
DROP TABLE review_comments;
