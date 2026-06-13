CREATE TABLE sdd_documents (
    workspace_id TEXT        PRIMARY KEY,
    sections     JSONB       NOT NULL DEFAULT '{}',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
