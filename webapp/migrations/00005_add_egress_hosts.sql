-- The internet hosts an admin allows steps to reach when they run in
-- containers of their own, through the webapp's egress proxy; see egress.go.

-- +goose Up
CREATE TABLE egress_hosts (
  host varchar(255) NOT NULL,
  note varchar(255) NOT NULL DEFAULT '',
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (host)
);

-- +goose Down
-- The hosts allowed go; steps in containers then reach none.
DROP TABLE egress_hosts;
