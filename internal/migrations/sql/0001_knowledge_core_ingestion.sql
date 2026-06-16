CREATE TABLE IF NOT EXISTS sources (
    id UUID PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    type TEXT NOT NULL,
    uri TEXT,
    external_id TEXT,
    title TEXT,
    checksum TEXT NOT NULL,
    identity_key TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, identity_key)
);

CREATE TABLE IF NOT EXISTS knowledge_objects (
    id UUID PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    type TEXT NOT NULL,
    title TEXT,
    summary TEXT,
    content TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    metadata JSONB NOT NULL DEFAULT '{}',
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS object_sources (
    object_id UUID NOT NULL REFERENCES knowledge_objects(id) ON DELETE CASCADE,
    source_id UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    relevance NUMERIC(4,3) NOT NULL DEFAULT 1.000,
    CHECK (relevance >= 0 AND relevance <= 1),
    PRIMARY KEY (object_id, source_id)
);

CREATE TABLE IF NOT EXISTS audit_events (
    id UUID PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    actor_id TEXT,
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id UUID NOT NULL REFERENCES knowledge_objects(id) ON DELETE CASCADE,
    after JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sources_workspace_identity ON sources (workspace_id, identity_key);
CREATE INDEX IF NOT EXISTS idx_knowledge_objects_workspace_id ON knowledge_objects (workspace_id);
CREATE INDEX IF NOT EXISTS idx_object_sources_source_id ON object_sources (source_id);
CREATE INDEX IF NOT EXISTS idx_audit_events_workspace_id ON audit_events (workspace_id);
CREATE INDEX IF NOT EXISTS idx_audit_events_target_created_at ON audit_events (target_type, target_id, created_at DESC);
