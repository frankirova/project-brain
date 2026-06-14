package telegram

import (
	"strings"
	"testing"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// Telegram pins callback_data at 64 bytes; the payload test pins
// that contract so a future token-size regression (e.g., switching
// to a 64-char secret) cannot silently break the inline keyboard.
func TestTelegramReviewActionPayload(t *testing.T) {
	got := TelegramReviewActionPayload("abc-123")
	if got != "rv:abc-123" {
		t.Fatalf("payload = %q, want rv:abc-123", got)
	}
	if len(got) > 64 {
		t.Errorf("payload %d bytes exceeds Telegram 64-byte limit", len(got))
	}
}

// A real UUID payload is the worst case under our token scheme
// (39 bytes total: 2 namespace + 1 separator + 36 UUID).
func TestTelegramReviewActionPayloadFitsUUID(t *testing.T) {
	p := TelegramReviewActionPayload(uuid.NewString())
	if got, want := len(p), 39; got != want {
		t.Errorf("UUID payload length = %d, want %d", got, want)
	}
	if len(p) > 64 {
		t.Errorf("payload %d bytes exceeds Telegram 64-byte limit", len(p))
	}
}

func TestBacklogButtonsForStatus(t *testing.T) {
	tests := []struct {
		name        string
		status      string
		wantActions []string
	}{
		{
			name:   "proposed exposes validate, debate, deprecate, skip",
			status: domain.KnowledgeObjectStatusProposed,
			wantActions: []string{
				TelegramReviewActionValidate,
				TelegramReviewActionDebate,
				TelegramReviewActionDeprecate,
				TelegramReviewActionSkip,
			},
		},
		{
			name:   "debating drops debate, keeps terminal + skip",
			status: domain.KnowledgeObjectStatusDebating,
			wantActions: []string{
				TelegramReviewActionValidate,
				TelegramReviewActionDeprecate,
				TelegramReviewActionSkip,
			},
		},
		{
			name:        "deprecated shows only skip",
			status:      domain.KnowledgeObjectStatusDeprecated,
			wantActions: []string{TelegramReviewActionSkip},
		},
		{
			name:        "unknown status falls back to skip only",
			status:      domain.KnowledgeObjectStatusActive,
			wantActions: []string{TelegramReviewActionSkip},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specs := backlogButtonsForStatus(tt.status)
			if len(specs) != len(tt.wantActions) {
				t.Fatalf("got %d buttons, want %d", len(specs), len(tt.wantActions))
			}
			for i, s := range specs {
				if s.Action != tt.wantActions[i] {
					t.Errorf("button %d action = %q, want %q", i, s.Action, tt.wantActions[i])
				}
				if s.Label == "" {
					t.Errorf("button %d has empty label", i)
				}
			}
		})
	}
}

func TestAssembleBacklogRowsPairsSpecsWithTokens(t *testing.T) {
	specs := []backlogButtonSpec{
		{Action: TelegramReviewActionValidate, Label: "✅ Validate"},
		{Action: TelegramReviewActionDebate, Label: "💬 Debate"},
		{Action: TelegramReviewActionDeprecate, Label: "🗑 Deprecate"},
		{Action: TelegramReviewActionSkip, Label: "⏭ Skip"},
	}
	tokens := []string{"t1", "t2", "t3", "t4"}
	rows := assembleBacklogRows(specs, tokens)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (two per row)", len(rows))
	}
	want := [][]string{
		{"rv:t1", "rv:t2"},
		{"rv:t3", "rv:t4"},
	}
	for r, row := range rows {
		if len(row) != 2 {
			t.Fatalf("row %d has %d buttons, want 2", r, len(row))
		}
		for c, btn := range row {
			if btn.Data != want[r][c] {
				t.Errorf("row %d col %d data = %q, want %q", r, c, btn.Data, want[r][c])
			}
			if !strings.HasPrefix(btn.Data, "rv:") {
				t.Errorf("row %d col %d missing rv: prefix: %q", r, c, btn.Data)
			}
		}
	}
}

func TestAssembleBacklogRowsSingleButtonOnLastRow(t *testing.T) {
	specs := []backlogButtonSpec{
		{Action: TelegramReviewActionSkip, Label: "⏭ Skip"},
	}
	rows := assembleBacklogRows(specs, []string{"t1"})
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("expected 1 row with 1 button, got %+v", rows)
	}
	if rows[0][0].Data != "rv:t1" {
		t.Errorf("data = %q, want rv:t1", rows[0][0].Data)
	}
}

func TestAssembleBacklogRowsPanicsOnCountMismatch(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on spec/token count mismatch")
		}
	}()
	assembleBacklogRows(
		[]backlogButtonSpec{{Action: TelegramReviewActionValidate}},
		[]string{"t1", "t2"},
	)
}

func TestRenderBacklogCardTextShowsStatusTitleSummary(t *testing.T) {
	item := app.BacklogItem{
		Status:  domain.KnowledgeObjectStatusProposed,
		Title:   "Adopt Go for the new service",
		Summary: "Discussion in #eng about backend language.",
	}
	got := renderBacklogCardText(item, nil)
	for _, want := range []string{"proposed", "Adopt Go", "Discussion in #eng"} {
		if !strings.Contains(got, want) {
			t.Errorf("text missing %q: %q", want, got)
		}
	}
}

func TestRenderBacklogCardTextWithHydratedContent(t *testing.T) {
	item := app.BacklogItem{Status: domain.KnowledgeObjectStatusProposed, Title: "T"}
	hydrated := &domain.KnowledgeObject{Content: "Hydrated body."}
	got := renderBacklogCardText(item, hydrated)
	if !strings.Contains(got, "Hydrated body.") {
		t.Errorf("missing hydrated content: %q", got)
	}
}

func TestRenderBacklogCardTextOmitsContentWhenFinderAbsent(t *testing.T) {
	item := app.BacklogItem{Status: domain.KnowledgeObjectStatusProposed, Title: "T"}
	if got := renderBacklogCardText(item, nil); strings.Contains(got, "Contenido:") {
		t.Errorf("expected no Contenido section without a hydrated object: %q", got)
	}
}

func TestRenderBacklogCardTextStaleMarker(t *testing.T) {
	item := app.BacklogItem{
		Status:       domain.KnowledgeObjectStatusDebating,
		Title:        "X",
		IsStale:      true,
		StaleForDays: 7,
	}
	got := renderBacklogCardText(item, nil)
	if !strings.Contains(got, "⚠ stale 7 days") {
		t.Errorf("missing stale marker: %q", got)
	}
}

func TestFormatStaleMarker(t *testing.T) {
	if got, want := formatStaleMarker(3), "⚠ stale 3 days"; got != want {
		t.Errorf("formatStaleMarker(3) = %q, want %q", got, want)
	}
}
