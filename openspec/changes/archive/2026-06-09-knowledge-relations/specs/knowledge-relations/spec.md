# Knowledge Relations Specification

## Purpose

Define typed directed edges between knowledge objects within a workspace. Relations allow agents, commands, and future ingestion hooks to express semantic links (supports, contradicts, supersedes, etc.) between knowledge entities, forming the foundation of a queryable knowledge graph. This is a standalone persistence layer — no ingestion integration or API surface is included in this change.

## Requirements

### Requirement: Create Relation

The system MUST create a typed directed edge between two knowledge objects, persisting it with a UUID identifier, workspace scope, timestamps, and optional metadata.

#### Scenario: Successful creation

- GIVEN two existing knowledge objects A and B in workspace W
- WHEN a relation is created with source A, target B, type "supports", confidence 0.9, and evidence "corroborated by study X"
- THEN the relation is persisted with a generated UUID, workspace W, the provided fields, and a created_at timestamp
- AND the relation is returned with all fields populated

#### Scenario: Creation with minimal fields

- GIVEN two existing knowledge objects in the same workspace
- WHEN a relation is created with only source, target, and type (no confidence or evidence)
- THEN the relation persists with NULL confidence, NULL evidence, and empty metadata

#### Scenario: Relation persists workspace isolation

- GIVEN knowledge objects A in workspace W1 and B in workspace W2
- WHEN a relation is attempted with source A and target B
- THEN the system MUST reject the creation because source and target belong to different workspaces

---

### Requirement: Relation Types

The system MUST validate the relation type against the 14 allowed values defined in PROJECT_BRAIN.md section 10.2: `relates_to`, `depends_on`, `contradicts`, `supersedes`, `supports`, `derived_from`, `mentions`, `decides`, `implements`, `compares_with`, `replaces`, `blocks`, `references`, `part_of`.

#### Scenario: Valid relation type accepted

- GIVEN two existing knowledge objects in the same workspace
- WHEN a relation is created with type "contradicts"
- THEN the relation is persisted successfully

#### Scenario: Invalid relation type rejected

- GIVEN two existing knowledge objects in the same workspace
- WHEN a relation is created with type "invalid_type"
- THEN the system MUST reject the creation with a validation error
- AND no row is inserted into the relations table

---

### Requirement: Self-Reference Prevention

The system MUST reject any relation where the source object and target object are the same entity. This is enforced at both the database level (CHECK constraint) and the application layer.

#### Scenario: Self-reference rejected by application

- GIVEN an existing knowledge object A in workspace W
- WHEN a relation is created with source A and target A
- THEN the system MUST reject the creation
- AND no database call is made

#### Scenario: Self-reference blocked by database constraint

- GIVEN the application layer validation is bypassed (e.g., direct SQL insert)
- WHEN a relation with source = target reaches the database
- THEN the CHECK constraint `source_object_id != target_object_id` MUST reject the insert

---

### Requirement: Duplicate Pair Prevention

The system MUST prevent duplicate `(source_object_id, target_object_id, relation_type)` combinations within the same workspace via a unique index.

#### Scenario: Duplicate relation rejected

- GIVEN an existing relation from A to B of type "supports" in workspace W
- WHEN another relation from A to B of type "supports" is created in workspace W
- THEN the system MUST reject the creation with a uniqueness violation error

#### Scenario: Same pair with different type allowed

- GIVEN an existing relation from A to B of type "supports" in workspace W
- WHEN a relation from A to B of type "contradicts" is created in workspace W
- THEN the relation is persisted successfully (different type, same pair is allowed)

---

### Requirement: Query by Source

The system MUST return all relations originating from a given knowledge object within a workspace.

#### Scenario: Find relations by source object

- GIVEN knowledge object A has outgoing relations to B ("supports") and C ("depends_on") in workspace W
- WHEN relations are queried by source object A in workspace W
- THEN both relations are returned
- AND the result set contains the relation type, target object, confidence, and evidence for each

#### Scenario: No relations for source

- GIVEN knowledge object A has no outgoing relations in workspace W
- WHEN relations are queried by source object A in workspace W
- THEN an empty result set is returned

#### Scenario: Source query respects workspace scope

- GIVEN knowledge object A has a relation to B in workspace W1
- WHEN relations are queried by source object A in workspace W2
- THEN an empty result set is returned (workspace isolation)

---

### Requirement: Query by Target

The system MUST return all relations targeting a given knowledge object within a workspace.

#### Scenario: Find relations by target object

- GIVEN knowledge objects A and B both have incoming relations to C in workspace W
- WHEN relations are queried by target object C in workspace W
- THEN both relations are returned with source, type, confidence, and evidence

#### Scenario: No relations for target

- GIVEN knowledge object C has no incoming relations in workspace W
- WHEN relations are queried by target object C in workspace W
- THEN an empty result set is returned

---

### Requirement: Query by Type

The system MUST return all relations of a specific type within a workspace.

#### Scenario: Find relations by type

- GIVEN relations of type "contradicts" exist from A→B and from C→D in workspace W
- WHEN relations are queried by type "contradicts" in workspace W
- THEN both relations are returned

#### Scenario: No relations of given type

- GIVEN no relations of type "blocks" exist in workspace W
- WHEN relations are queried by type "blocks" in workspace W
- THEN an empty result set is returned

#### Scenario: Type query respects workspace scope

- GIVEN a "supersedes" relation exists in workspace W1 but not in W2
- WHEN relations are queried by type "supersedes" in workspace W2
- THEN an empty result set is returned

---

### Requirement: Cascade Delete

The system MUST automatically delete all relations where the source or target knowledge object is deleted. This is enforced by foreign key cascades on both `source_object_id` and `target_object_id`.

#### Scenario: Deleting source cascades to relations

- GIVEN knowledge object A is the source of relations to B and C
- WHEN knowledge object A is deleted
- THEN both relations originating from A are automatically removed from the relations table

#### Scenario: Deleting target cascades to relations

- GIVEN knowledge object B is the target of relations from A and C
- WHEN knowledge object B is deleted
- THEN both relations targeting B are automatically removed from the relations table

#### Scenario: Unrelated relations preserved

- GIVEN knowledge object A has a relation to B, and knowledge object C has a relation to D
- WHEN knowledge object A is deleted
- THEN the relation from C to D remains intact
