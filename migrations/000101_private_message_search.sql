-- +goose Up
-- +goose NO TRANSACTION
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_private_messages_content_trgm
    ON private_messages USING gin (content gin_trgm_ops);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agents_agent_name_trgm
    ON agents USING gin (agent_name gin_trgm_ops);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agents_agent_name_en_trgm
    ON agents USING gin (agent_name_en gin_trgm_ops);

-- +goose Down
-- +goose NO TRANSACTION
DROP INDEX CONCURRENTLY IF EXISTS idx_agents_agent_name_en_trgm;
DROP INDEX CONCURRENTLY IF EXISTS idx_private_messages_content_trgm;
