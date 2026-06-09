# Delta for knowledge-core-ingestion

## ADDED Requirements

### Requirement: Schema Supports Project Scope and Importance

The system SHOULD accept project_id, tags, confidence, and importance fields in ingestion requests and persist them on the knowledge object.

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

The system MUST continue to enforce the 4-write contract per ingest, regardless of new schema fields.

#### Scenario: 4-write contract holds with new fields

- GIVEN an ingestion request with project_id, tags, confidence, importance
- WHEN the request is accepted
- THEN exactly 4 application writes occur (source, object, link, audit)
- AND the FTS index is updated by the database, not the application

## MODIFIED Requirements

### Requirement: Preserve Workspace Scope and Metadata

The system MUST associate created records with the submitted workspace and SHOULD preserve submitted type, title, summary, status, timestamps, project_id, tags, confidence, importance, and additional metadata when provided.
(Previously: The system MUST associate created records with the submitted workspace and SHOULD preserve submitted type, title, summary, status, timestamps, and additional metadata when provided.)

## REMOVED Requirements

None
