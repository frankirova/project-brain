package domain

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestValidateRelationTypeAcceptsAllAllowedValues(t *testing.T) {
	allowed := []RelationType{
		RelationTypeRelatesTo,
		RelationTypeDependsOn,
		RelationTypeContradicts,
		RelationTypeSupersedes,
		RelationTypeSupports,
		RelationTypeDerivedFrom,
		RelationTypeMentions,
		RelationTypeDecides,
		RelationTypeImplements,
		RelationTypeComparesWith,
		RelationTypeReplaces,
		RelationTypeBlocks,
		RelationTypeReferences,
		RelationTypePartOf,
	}
	for _, rt := range allowed {
		if !ValidateRelationType(rt) {
			t.Errorf("ValidateRelationType(%q) = false, want true", rt)
		}
	}
}

func TestValidateRelationTypeRejectsInvalidType(t *testing.T) {
	invalid := []RelationType{
		"invalid_type",
		"",
		"RELATED_TO",  // wrong case
		"depends_on ", // trailing space
		"part_of_extra",
	}
	for _, rt := range invalid {
		if ValidateRelationType(rt) {
			t.Errorf("ValidateRelationType(%q) = true, want false", rt)
		}
	}
}

func TestRelationInputJSONUnmarshalling(t *testing.T) {
	confidence := 0.85
	inputJSON := `{
		"source_object_id": "00000000-0000-0000-0000-000000000001",
		"target_object_id": "00000000-0000-0000-0000-000000000002",
		"relation_type": "supports",
		"confidence": 0.85,
		"evidence": "corroborated by study X",
		"metadata": {"key": "value"}
	}`

	var input RelationInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatalf("Unmarshal RelationInput: %v", err)
	}

	if input.SourceObjectID != uuid.MustParse("00000000-0000-0000-0000-000000000001") {
		t.Errorf("SourceObjectID = %v, want 00000000-0000-0000-0000-000000000001", input.SourceObjectID)
	}
	if input.TargetObjectID != uuid.MustParse("00000000-0000-0000-0000-000000000002") {
		t.Errorf("TargetObjectID = %v, want 00000000-0000-0000-0000-000000000002", input.TargetObjectID)
	}
	if input.RelationType != RelationTypeSupports {
		t.Errorf("RelationType = %q, want %q", input.RelationType, RelationTypeSupports)
	}
	if input.Confidence == nil || *input.Confidence != confidence {
		t.Errorf("Confidence = %v, want %v", input.Confidence, confidence)
	}
	if input.Evidence != "corroborated by study X" {
		t.Errorf("Evidence = %q, want %q", input.Evidence, "corroborated by study X")
	}
	if input.Metadata["key"] != "value" {
		t.Errorf("Metadata[key] = %v, want %q", input.Metadata["key"], "value")
	}
}

func TestRelationInputOmitsOptionalFields(t *testing.T) {
	inputJSON := `{
		"source_object_id": "00000000-0000-0000-0000-000000000001",
		"target_object_id": "00000000-0000-0000-0000-000000000002",
		"relation_type": "mentions"
	}`

	var input RelationInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatalf("Unmarshal RelationInput: %v", err)
	}

	if input.Confidence != nil {
		t.Errorf("Confidence = %v, want nil", *input.Confidence)
	}
	if input.Evidence != "" {
		t.Errorf("Evidence = %q, want empty", input.Evidence)
	}
	if input.Metadata != nil {
		t.Errorf("Metadata = %v, want nil", input.Metadata)
	}
}
