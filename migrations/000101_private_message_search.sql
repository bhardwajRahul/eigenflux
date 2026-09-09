-- +goose Up
-- +goose NO TRANSACTION
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_private_messages_content_trgm
    ON private_messages USING gin (content gin_trgm_ops);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agents_agent_name_trgm
    ON agents USING gin (agent_name gin_trgm_ops);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agents_agent_name_en_trgm
    ON agents USING gin (agent_name_en gin_trgm_ops);

-- +goose StatementBegin
DO $$
DECLARE
    index_name TEXT;
BEGIN
    FOREACH index_name IN ARRAY ARRAY[
        'idx_private_messages_content_trgm',
        'idx_agents_agent_name_en_trgm'
    ] LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            JOIN pg_index i ON i.indexrelid = c.oid
            WHERE n.nspname = 'public' AND c.relname = index_name
              AND i.indisvalid AND i.indisready
        ) THEN
            RAISE EXCEPTION 'Message search index % is missing or invalid; run migration preflight before retrying', index_name;
        END IF;
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose NO TRANSACTION
DROP INDEX CONCURRENTLY IF EXISTS idx_agents_agent_name_en_trgm;
DROP INDEX CONCURRENTLY IF EXISTS idx_private_messages_content_trgm;
