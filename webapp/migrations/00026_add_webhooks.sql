-- Webhooks: other systems' events starting a flow, each at a URL of its own
-- named by token, compared byte for byte, which a delivery must sign, or
-- carry, with the webhook's secret, sealed with SECRET_KEY. Its kind says
-- how: github, slack or generic, which checks an HMAC (verify hmac) or the
-- secret itself (verify token) in the header it names; the HMAC covers the
-- timestamp in timestamp_header and the id in id_header, when it names
-- them, then the body. events are the events it takes, comma-separated;
-- mapping what each input of the flow is taken from in the payload.
-- created_by is whom its runs are for. last_payload is what the latest
-- delivery it took sent, sealed, to write the mapping by. refused_count
-- counts the requests refused before their signature held, which leave no
-- delivery. See webhooks.go.
--
-- Each webhook keeps its latest deliveries, with what came of each, and
-- the digests of the GitHub payloads it took, so one is taken once
-- whatever headers come with it. A run a webhook started says so in
-- webhook_id, and the delivery it was started for in idempotency_key, as
-- an API key's run does, so a delivery sent again starts no second run.

-- +goose Up
CREATE TABLE webhooks (
  id bigint NOT NULL AUTO_INCREMENT,
  token varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  flow_id bigint NOT NULL,
  name varchar(255) NOT NULL,
  kind varchar(16) NOT NULL,
  secret text NOT NULL,
  verify varchar(16) NOT NULL DEFAULT '',
  header varchar(64) NOT NULL DEFAULT '',
  id_header varchar(64) NOT NULL DEFAULT '',
  timestamp_header varchar(64) NOT NULL DEFAULT '',
  events varchar(2048) NOT NULL DEFAULT '',
  mapping json NOT NULL,
  enabled boolean NOT NULL DEFAULT TRUE,
  created_by bigint DEFAULT NULL,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  last_payload mediumtext,
  last_event varchar(255) NOT NULL DEFAULT '',
  last_payload_at timestamp(3) NULL DEFAULT NULL,
  refused_count bigint NOT NULL DEFAULT 0,
  last_refused_at timestamp(3) NULL DEFAULT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY webhooks_token (token),
  KEY webhooks_flow (flow_id),
  CONSTRAINT webhooks_flow FOREIGN KEY (flow_id) REFERENCES flows (id) ON DELETE CASCADE
);

CREATE TABLE webhook_deliveries (
  id bigint NOT NULL AUTO_INCREMENT,
  webhook_id bigint NOT NULL,
  received_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  delivery varchar(255) NOT NULL DEFAULT '',
  event varchar(255) NOT NULL DEFAULT '',
  outcome varchar(16) NOT NULL,
  reason varchar(1024) NOT NULL DEFAULT '',
  run_id bigint DEFAULT NULL,
  PRIMARY KEY (id),
  KEY webhook_deliveries_webhook (webhook_id, id),
  KEY webhook_deliveries_run (run_id),
  CONSTRAINT webhook_deliveries_webhook FOREIGN KEY (webhook_id) REFERENCES webhooks (id) ON DELETE CASCADE,
  CONSTRAINT webhook_deliveries_run FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE SET NULL
);

CREATE TABLE webhook_digests (
  webhook_id bigint NOT NULL,
  digest char(64) CHARACTER SET ascii NOT NULL,
  seen_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (webhook_id, digest),
  KEY webhook_digests_seen (webhook_id, seen_at),
  CONSTRAINT webhook_digests_webhook FOREIGN KEY (webhook_id) REFERENCES webhooks (id) ON DELETE CASCADE
);

ALTER TABLE runs ADD COLUMN webhook_id bigint DEFAULT NULL,
  ADD UNIQUE KEY runs_webhook_delivery (webhook_id, idempotency_key);

-- +goose Down
-- The webhooks go, their secrets and deliveries with them; their runs stay,
-- as runs their creators started, no longer saying a webhook did.
ALTER TABLE runs DROP KEY runs_webhook_delivery, DROP COLUMN webhook_id;
DROP TABLE webhook_digests;
DROP TABLE webhook_deliveries;
DROP TABLE webhooks;
