package domain

import (
	"testing"
)

// TestSddOrderedSectionsHasFourKeys verifies that SddOrderedSections contains
// exactly the four canonical keys in the declared order.
func TestSddOrderedSectionsHasFourKeys(t *testing.T) {
	want := []SddSectionKey{
		SddSectionContext,
		SddSectionDecisions,
		SddSectionConstraints,
		SddSectionOpenQuestions,
	}
	if len(SddOrderedSections) != len(want) {
		t.Fatalf("len(SddOrderedSections) = %d, want %d", len(SddOrderedSections), len(want))
	}
	for i, k := range want {
		if SddOrderedSections[i] != k {
			t.Errorf("SddOrderedSections[%d] = %q, want %q", i, SddOrderedSections[i], k)
		}
	}
}

// TestSddSectionConstantsAreDistinct asserts that all four section key
// constants have different string values.
func TestSddSectionConstantsAreDistinct(t *testing.T) {
	keys := []SddSectionKey{
		SddSectionContext,
		SddSectionDecisions,
		SddSectionConstraints,
		SddSectionOpenQuestions,
	}
	seen := make(map[SddSectionKey]bool, len(keys))
	for _, k := range keys {
		if seen[k] {
			t.Errorf("duplicate section key: %q", k)
		}
		seen[k] = true
	}
}
