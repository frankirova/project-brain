package app

import (
	"fmt"
	"strings"

	"github.com/frankirova/project-brain/internal/domain"
)

// sectionHeadings maps each SddSectionKey to its human-readable Markdown heading.
var sectionHeadings = map[domain.SddSectionKey]string{
	domain.SddSectionContext:       "Context",
	domain.SddSectionDecisions:     "Decisions",
	domain.SddSectionConstraints:   "Constraints",
	domain.SddSectionOpenQuestions: "Open Questions",
}

// RenderSddDocumentMarkdown renders doc as a structured Markdown string. The
// output always contains a top-level H1 heading and four H2 section headings,
// regardless of whether any entries exist. Empty sections render as "_(none)_".
// Entries within each section are ordered by UpdatedAt DESC (maintained by the
// writer; the renderer does not re-sort).
func RenderSddDocumentMarkdown(doc domain.SddDocument) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# SDD Document — %s\n", doc.WorkspaceID)

	for _, key := range domain.SddOrderedSections {
		heading, ok := sectionHeadings[key]
		if !ok {
			heading = string(key)
		}
		fmt.Fprintf(&b, "\n## %s\n", heading)

		entries := doc.Sections[key]
		if len(entries) == 0 {
			b.WriteString("\n_(none)_\n")
			continue
		}

		for _, e := range entries {
			fmt.Fprintf(&b, "\n### %s\n\n%s\n", e.Title, e.Summary)
		}
	}

	return b.String()
}
