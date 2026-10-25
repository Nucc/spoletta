-- Sentry is no longer a kind of process: flows reach it through Sentry's
-- MCP server or the HTTP API connector, as any other service. Each Sentry
-- process becomes an agent process, which fails, times out and is retried
-- as it did, so what handles its failure stays connected; one without a
-- prompt is given one asking for the issues its settings fetched. Its
-- settings stay as they were, which the webapp no longer reads.

-- +goose Up
UPDATE processes SET kind = 'agent', prompt = IF(prompt <> '', prompt, CONCAT(
	'Fetch issues from Sentry with the Sentry MCP server: organization ',
	COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(settings, '$.sentry.organization')), ''), '(not set)'),
	COALESCE(CONCAT(', project ', NULLIF(JSON_UNQUOTE(JSON_EXTRACT(settings, '$.sentry.project')), '')), ''),
	COALESCE(CONCAT(', environment ', NULLIF(JSON_UNQUOTE(JSON_EXTRACT(settings, '$.sentry.environment')), '')), ''),
	', the query ', COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(settings, '$.sentry.query')), ''), 'is:unresolved'),
	', sorted by ', COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(settings, '$.sentry.sort')), ''), 'date'),
	', at most ', COALESCE(NULLIF(JSON_EXTRACT(settings, '$.sentry.limit'), 0), 10),
	'. Reply with them as the list this process generates.'))
WHERE kind = 'sentry';

-- +goose Down
-- Agent processes whose settings still hold Sentry's become Sentry
-- processes again, keeping the prompt they were given; one saved since,
-- whose settings the webapp wrote without Sentry's, stays an agent.
UPDATE processes SET kind = 'sentry' WHERE kind = 'agent' AND JSON_CONTAINS_PATH(settings, 'one', '$.sentry');
