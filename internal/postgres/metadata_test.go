package postgres

import (
	"testing"

	"github.com/frankirova/project-brain/internal/domain"
)

// TestMarshalMetadataNilProducesEmptyObject locks the fix for the
// NOT NULL constraint on sources.metadata and knowledge_objects.metadata:
// a nil map must serialize to '{}', not SQL NULL. Passing an explicit
// NULL violates the constraint even though the column has DEFAULT '{}'
// (the default only applies when the column is omitted from the INSERT).
func TestMarshalMetadataNilProducesEmptyObject(t *testing.T) {
	got, err := marshalMetadata(nil)
	if err != nil {
		t.Fatalf("marshalMetadata(nil) error: %v", err)
	}
	if string(got) != "{}" {
		t.Fatalf("marshalMetadata(nil) = %q, want %q", string(got), "{}")
	}
}

func TestMarshalMetadataEmptyProducesEmptyObject(t *testing.T) {
	got, err := marshalMetadata(domain.Metadata{})
	if err != nil {
		t.Fatalf("marshalMetadata(empty) error: %v", err)
	}
	if string(got) != "{}" {
		t.Fatalf("marshalMetadata(empty) = %q, want %q", string(got), "{}")
	}
}

func TestMarshalMetadataPopulated(t *testing.T) {
	got, err := marshalMetadata(domain.Metadata{"k": "v"})
	if err != nil {
		t.Fatalf("marshalMetadata error: %v", err)
	}
	if string(got) != `{"k":"v"}` {
		t.Fatalf("marshalMetadata = %q, want %q", string(got), `{"k":"v"}`)
	}
}
