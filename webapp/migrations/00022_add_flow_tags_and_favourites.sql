-- The tags people who may change a flow, procedure or struct give it, to
-- find it by on the flow list (tags.go), and the flows each person starred
-- as their favourites, listed first (favourites.go); without sign-in they
-- are everyone's, user 0's. Both go with their flow. Tags are kept as
-- spelled, told apart by every character; the webapp keeps a flow from
-- having two that differ only in case.

-- +goose Up
CREATE TABLE flow_tags (
  flow_id bigint NOT NULL,
  tag varchar(64) COLLATE utf8mb4_bin NOT NULL,
  PRIMARY KEY (flow_id, tag),
  CONSTRAINT flow_tags_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE
);
CREATE TABLE favourite_flows (
  user_id bigint NOT NULL,
  flow_id bigint NOT NULL,
  PRIMARY KEY (user_id, flow_id),
  KEY favourite_flows_flow (flow_id),
  CONSTRAINT favourite_flows_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE
);

-- +goose Down
-- The tags flows were given and the favourites people starred are lost.
DROP TABLE favourite_flows;
DROP TABLE flow_tags;
