# Knowledge Core Ingestion Specification

## Purpose

Define interface-neutral ingestion of plain text into auditable Knowledge Core records. This capability stores text as a Source, a KnowledgeObject, their link, and an audit event, with optional project scope, importance, confidence, tags, and a full-text search index. The capability is channel-neutral: it does not require Telegram, embeddings, RAG, NATS, agents, workers, or S3-compatible storage to ingest text knowledge. (Full-text search is part of this capability — it is a database internal, not an external retrieval system.)

## Requirements

### Requirement: Accept Plain Text Ingestion

The system MUST accept an ingestion request containing workspace identity, plain text content, and source metadata. The system MUST reject requests that cannot create an auditable knowledge record.

#### Scenario: Valid text is accepted

- GIVEN a workspace and non-empty plain text content
- WHEN the ingestion request is submitted with source metadata
- THEN the system accepts the request for Knowledge Core persistence

#### Scenario: Empty content is rejected

- GIVEN a workspace and text content that is empty or only whitespace
- WHEN the ingestion request is submitted
- THEN the system rejects the request with a validation error
- AND no Source, KnowledgeObject, link, or audit event is created

### Requirement: Create Auditable Knowledge Records

The system MUST persist one Source, one KnowledgeObject, one Source-to-object link, and one audit event for each accepted new text ingestion.

#### Scenario: New text creates complete records

- GIVEN a valid text ingestion request that has not been ingested before
- WHEN the request is accepted
- THEN a Source is created for the submitted origin metadata
- AND a KnowledgeObject is created with the submitted text and workspace scope
- AND the Source and KnowledgeObject are linked
- AND an audit event records the ingestion action

#### Scenario: Persistence is all-or-nothing

- GIVEN a valid text ingestion request
- WHEN any required record cannot be persisted
- THEN the ingestion fails
- AND the system does not leave a partial Source, KnowledgeObject, link, or audit event for that request

### Requirement: Preserve Workspace Scope and Metadata

The system MUST associate created records with the submitted workspace and SHOULD preserve submitted type, title, summary, status, timestamps, project_id, tags, confidence, importance, and additional metadata when provided.

#### Scenario: Metadata is stored with the records

- GIVEN a valid ingestion request with title, summary, type, status, timestamps, and metadata
- WHEN the request is accepted
- THEN the resulting records expose the same workspace scope
- AND the submitted metadata is available on the appropriate Source or KnowledgeObject

#### Scenario: Project ID is stored

- GIVEN an ingestion request with a project_id
- WHEN the request is accepted
- THEN the knowledge object is stored with the project_id

#### Scenario: Tags are stored as array

- GIVEN an ingestion request with tags ["go", "postgres"]
- WHEN the request is accepted
- THEN the knowledge object is stored with tags ["go", "postgres"]

#### Scenario: Confidence and importance are stored

- GIVEN an ingestion request with confidence 0.9 and importance 80
- WHEN the request is accepted
- THEN the knowledge object is stored with confidence 0.9 and importance 80

### Requirement: Full-Text Search Index Available

The system MUST provide a full-text search index on knowledge objects via a generated tsvector column.

#### Scenario: FTS index is auto-populated on insert

- GIVEN a knowledge object is inserted with content "PostgreSQL full text search"
- WHEN the insert completes
- THEN the search_vector column is automatically populated
- AND a query using to_tsquery returns the inserted row

#### Scenario: FTS index includes title, summary, and content

- GIVEN a knowledge object with title "Intro", summary "Quick overview", content "Detailed text"
- WHEN the FTS index is queried for any of those words
- THEN the row is returned in results

#### Scenario: FTS language config is neutral

- GIVEN bilingual content in Spanish and English
- WHEN the FTS index is queried
- THEN words from both languages are searchable

### Requirement: Idempotency Contract Preserved

The system SHOULD provide idempotent behavior for repeated submissions carrying the same idempotency key or equivalent source identity within the same workspace.

The system MUST continue to enforce the 4-write contract per ingest, regardless of new schema fields.

#### Scenario: Duplicate request returns existing result

- GIVEN a text ingestion request has already created records for a workspace and idempotency key
- WHEN the same workspace and idempotency key are submitted again
- THEN the system returns the existing ingestion result
- AND it does not create duplicate Source, KnowledgeObject, link, or audit event records

#### Scenario: Same content from distinct sources may be stored

- GIVEN two valid requests with the same text but different source identities or no idempotency match
- WHEN both requests are submitted
- THEN the system MAY create separate ingestion records
- AND each record remains traceable to its own Source

#### Scenario: 4-write contract holds with new fields

- GIVEN an ingestion request with project_id, tags, confidence, importance
- WHEN the request is accepted
- THEN exactly 4 application writes occur (source, object, link, audit)
- AND the FTS index is updated by the database, not the application

### Requirement: Exclude Retrieval and Channel Behavior

The system MUST NOT require Telegram, embeddings, RAG, vector search, NATS, agents, workers, or S3-compatible storage to ingest text knowledge.

#### Scenario: Ingestion completes without deferred capabilities

- GIVEN a valid text ingestion request
- WHEN Telegram, embeddings, RAG, messaging, agents, and object storage are unavailable
- THEN the system can still create the required Knowledge Core records
