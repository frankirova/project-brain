package app

import (
	"strings"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// parseTime is a test helper that parses an RFC 3339 string and panics on failure.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRenderSddDocumentMarkdown_AllEmpty(t *testing.T) {
	doc := domain.SddDocument{
		WorkspaceID: "ws-test",
		Sections: map[domain.SddSectionKey][]domain.SddEntry{
			domain.SddSectionContext:       {},
			domain.SddSectionDecisions:     {},
			domain.SddSectionConstraints:   {},
			domain.SddSectionOpenQuestions: {},
		},
	}

	got := RenderSddDocumentMarkdown(doc)

	// H1 title present.
	if !strings.Contains(got, "# SDD Document — ws-test") {
		t.Errorf("missing H1 title; got:\n%s", got)
	}

	// All four H2 headings present.
	for _, heading := range []string{"## Context", "## Decisions", "## Constraints", "## Open Questions"} {
		if !strings.Contains(got, heading) {
			t.Errorf("missing heading %q; got:\n%s", heading, got)
		}
	}

	// Each empty section renders _(none)_.
	count := strings.Count(got, "_(none)_")
	if count != 4 {
		t.Errorf("want 4 _(none)_ markers, got %d; output:\n%s", count, got)
	}
}

func TestRenderSddDocumentMarkdown_WithEntries(t *testing.T) {
	older := parseTime("2026-01-01T10:00:00Z")
	newer := parseTime("2026-06-01T10:00:00Z")

	doc := domain.SddDocument{
		WorkspaceID: "ws-acme",
		Sections: map[domain.SddSectionKey][]domain.SddEntry{
			domain.SddSectionContext: {
				{
					ObjectID:  uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
					Title:     "Background",
					Summary:   "Context summary",
					UpdatedAt: older,
				},
			},
			domain.SddSectionDecisions: {
				// Already ordered DESC by writer: newer first.
				{
					ObjectID:  uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
					Title:     "Use PostgreSQL",
					Summary:   "We chose postgres for reliability.",
					UpdatedAt: newer,
				},
				{
					ObjectID:  uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc"),
					Title:     "No ORM",
					Summary:   "Hand-rolled SQL for clarity.",
					UpdatedAt: older,
				},
			},
			domain.SddSectionConstraints:   {},
			domain.SddSectionOpenQuestions: {},
		},
	}

	got := RenderSddDocumentMarkdown(doc)

	// H1 present.
	if !strings.Contains(got, "# SDD Document — ws-acme") {
		t.Errorf("missing H1; got:\n%s", got)
	}

	// Context section has one entry.
	if !strings.Contains(got, "### Background") {
		t.Errorf("missing Context entry title; got:\n%s", got)
	}
	if !strings.Contains(got, "Context summary") {
		t.Errorf("missing Context entry summary; got:\n%s", got)
	}

	// Decisions section has two entries.
	if !strings.Contains(got, "### Use PostgreSQL") {
		t.Errorf("missing Decisions entry 1; got:\n%s", got)
	}
	if !strings.Contains(got, "### No ORM") {
		t.Errorf("missing Decisions entry 2; got:\n%s", got)
	}

	// Newer entry appears before older entry in the output.
	posPostgres := strings.Index(got, "### Use PostgreSQL")
	posNoORM := strings.Index(got, "### No ORM")
	if posPostgres >= posNoORM {
		t.Errorf("want newer entry (Use PostgreSQL) before older (No ORM); positions %d vs %d", posPostgres, posNoORM)
	}

	// Empty sections still render _(none)_.
	count := strings.Count(got, "_(none)_")
	if count != 2 {
		t.Errorf("want 2 _(none)_ markers (Constraints + Open Questions), got %d; output:\n%s", count, got)
	}
}

func TestRenderSddDocumentMarkdown_SectionOrder(t *testing.T) {
	// Verify that sections appear in canonical order: Context → Decisions →
	// Constraints → Open Questions regardless of map iteration order.
	doc := domain.SddDocument{
		WorkspaceID: "ws-order",
		Sections: map[domain.SddSectionKey][]domain.SddEntry{
			domain.SddSectionContext:       {},
			domain.SddSectionDecisions:     {},
			domain.SddSectionConstraints:   {},
			domain.SddSectionOpenQuestions: {},
		},
	}
	got := RenderSddDocumentMarkdown(doc)

	positions := map[string]int{
		"## Context":        strings.Index(got, "## Context"),
		"## Decisions":      strings.Index(got, "## Decisions"),
		"## Constraints":    strings.Index(got, "## Constraints"),
		"## Open Questions": strings.Index(got, "## Open Questions"),
	}
	for k, v := range positions {
		if v == -1 {
			t.Errorf("heading %q not found", k)
		}
	}
	if !(positions["## Context"] < positions["## Decisions"] &&
		positions["## Decisions"] < positions["## Constraints"] &&
		positions["## Constraints"] < positions["## Open Questions"]) {
		t.Errorf("sections out of canonical order; positions: %v", positions)
	}
}

func TestRenderSddDocumentMarkdown_EntryFormat(t *testing.T) {
	// Verify that each entry renders as "### {title}\n\n{summary}".
	doc := domain.SddDocument{
		WorkspaceID: "ws-fmt",
		Sections: map[domain.SddSectionKey][]domain.SddEntry{
			domain.SddSectionContext: {
				{
					ObjectID:  uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd"),
					Title:     "My Title",
					Summary:   "My Summary",
					UpdatedAt: parseTime("2026-01-01T00:00:00Z"),
				},
			},
			domain.SddSectionDecisions:     {},
			domain.SddSectionConstraints:   {},
			domain.SddSectionOpenQuestions: {},
		},
	}
	got := RenderSddDocumentMarkdown(doc)

	// The entry must have title as H3 followed by summary on a subsequent line.
	if !strings.Contains(got, "### My Title") {
		t.Errorf("entry H3 not found; got:\n%s", got)
	}
	if !strings.Contains(got, "My Summary") {
		t.Errorf("entry summary not found; got:\n%s", got)
	}
}
