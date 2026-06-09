CREATE TABLE IF NOT EXISTS relations (
    id UUID PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    source_object_id UUID NOT NULL REFERENCES knowledge_objects(id) ON DELETE CASCADE,
    target_object_id UUID NOT NULL REFERENCES knowledge_objects(id) ON DELETE CASCADE,
    relation_type TEXT NOT NULL,
    confidence NUMERIC(4,3) CHECK (confidence >= 0 AND confidence <= 1),
    evidence TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (source_object_id != target_object_id),
    UNIQUE (workspace_id, source_object_id, target_object_id, relation_type)
);

CREATE INDEX IF NOT EXISTS idx_relations_workspace_source ON relations (workspace_id, source_object_id);
CREATE INDEX IF NOT EXISTS idx_relations_workspace_target ON relations (workspace_id, target_object_id);
CREATE INDEX IF NOT EXISTS idx_relations_workspace_type ON relations (workspace_id, relation_type);
