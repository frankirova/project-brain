package telegram

import (
	"fmt"
	"strings"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
)

// TelegramReviewActionNamespace is the callback_data prefix the
// backlog review flow uses. The full payload Telegram echoes back is
// "<namespace>:<token>"; the token is a UUID the PR1
// TelegramReviewActionStore hands out. The namespace is reserved for
// the backlog flow so the existing collision validation flow keeps
// its "keep" / "discard" prefixes. The full payload stays under
// Telegram's 64-byte callback_data limit because a UUID (36 bytes)
// plus the namespace (2 bytes) plus the separator (1 byte) totals
// 39 bytes, with headroom for a future "rv:" → "rv1:" rename.
const TelegramReviewActionNamespace = "rv"

// TelegramReviewActionPayload formats the opaque callback data
// string Telegram echoes back when a user taps one of the backlog
// review buttons. The handler splits on ":" and resolves the token
// through the review-action store; no trusted context rides in the
// payload — every field the PR3 callback handler needs (workspace,
// object, action, expected status, actor, chat, next cursor, expiry)
// is loaded server-side from the store row keyed by Token.
func TelegramReviewActionPayload(token string) string {
	return TelegramReviewActionNamespace + ":" + token
}

// backlogButtonSpec describes one tappable button on a backlog card.
// The handler maps each spec.Action to a unique opaque token and
// renders one InlineButton per spec. Specs are produced in the order
// the user will see them; assembleBacklogRows folds them into the
// row-major layout Telegram expects.
type backlogButtonSpec struct {
	Action string
	Label  string
}

// backlogButtonsForStatus returns the status-aware button list for
// one backlog card, in the row-major order Telegram will render them
// in. The labels are stable strings; the PR3 callback handler
// matches on the Action constant, not the label, so renaming a
// label here does not break the wiring.
//
// Layout follows the change 15 design:
//
//   - proposed   -> Validate / Debate on row 1, Deprecate / Skip on
//     row 2 (4 buttons).
//   - debating   -> Validate / Deprecate on row 1, Skip on row 2
//     (3 buttons). MarkDebating is meaningless on a row that is
//     already debating, so the action is dropped.
//   - deprecated -> Skip only (1 button). The other lifecycle
//     transitions are no-ops on a deprecated row; the spec keeps the
//     row visible for review but disables lifecycle actions.
//   - anything else -> Skip only, defensively. A future status added
//     to change 14 should extend this switch explicitly; the default
//     keeps the handler total and avoids a panic if the backlog
//     query ever returns an unknown value.
func backlogButtonsForStatus(status string) []backlogButtonSpec {
	skip := backlogButtonSpec{Action: TelegramReviewActionSkip, Label: "⏭ Skip"}
	switch status {
	case domain.KnowledgeObjectStatusProposed:
		return []backlogButtonSpec{
			{Action: TelegramReviewActionValidate, Label: "✅ Validate"},
			{Action: TelegramReviewActionDebate, Label: "💬 Debate"},
			{Action: TelegramReviewActionDeprecate, Label: "🗑 Deprecate"},
			skip,
		}
	case domain.KnowledgeObjectStatusDebating:
		return []backlogButtonSpec{
			{Action: TelegramReviewActionValidate, Label: "✅ Validate"},
			{Action: TelegramReviewActionDeprecate, Label: "🗑 Deprecate"},
			skip,
		}
	case domain.KnowledgeObjectStatusDeprecated:
		return []backlogButtonSpec{skip}
	default:
		return []backlogButtonSpec{skip}
	}
}

// assembleBacklogRows pairs the button specs with the tokens the
// handler minted, producing the row-major InlineButton layout for
// SendMessageWithButtons. Buttons are laid out two per row to match
// the change 15 design; the last row may contain a single button
// (e.g., the deprecated card has only Skip).
//
// specs and tokens MUST be the same length; the function panics
// otherwise because a mismatch is a programming error, not a user
// error, and silent truncation would produce buttons whose Data
// field is the wrong token.
func assembleBacklogRows(specs []backlogButtonSpec, tokens []string) [][]InlineButton {
	if len(specs) != len(tokens) {
		panic("telegram: assembleBacklogRows spec/token count mismatch")
	}
	rows := make([][]InlineButton, 0, (len(specs)+1)/2)
	for i := 0; i < len(specs); i += 2 {
		end := i + 2
		if end > len(specs) {
			end = len(specs)
		}
		row := make([]InlineButton, 0, end-i)
		for j := i; j < end; j++ {
			row = append(row, InlineButton{
				Text: specs[j].Label,
				Data: TelegramReviewActionPayload(tokens[j]),
			})
		}
		rows = append(rows, row)
	}
	return rows
}

// renderBacklogCardText assembles the user-visible body of one
// backlog card. The card always shows the status, title, and
// summary. The content preview is included only when the caller
// passed a hydrated KnowledgeObject (i.e., the KnowledgeObjectFinder
// returned the row). The stale marker is appended only when the
// backlog query flagged the row as stale. The function is pure:
// it formats the text from its inputs and never touches the store
// or the sender.
func renderBacklogCardText(item app.BacklogItem, hydrated *domain.KnowledgeObject) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📋 Pendiente de revisión (%s)\n\n", item.Status)
	b.WriteString("Título: ")
	b.WriteString(item.Title)
	if item.Summary != "" {
		b.WriteString("\nResumen: ")
		b.WriteString(item.Summary)
	}
	if hydrated != nil && hydrated.Content != "" {
		b.WriteString("\n\nContenido:\n")
		b.WriteString(truncate(hydrated.Content, 400))
	}
	if item.IsStale {
		b.WriteString("\n\n")
		b.WriteString(formatStaleMarker(item.StaleForDays))
	}
	return b.String()
}

// formatStaleMarker renders the stale-days badge. The spec pins the
// exact wording to "⚠ stale N days" and uses the integer day count
// from BacklogItem.StaleForDays, which the backlog service already
// clamps to a non-negative value.
func formatStaleMarker(days int) string {
	return fmt.Sprintf("⚠ stale %d days", days)
}
