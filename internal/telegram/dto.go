// dto.go owns the adapter-shaped data types for the Telegram bot
// (callback payloads, viewmodel items, action identifiers). Per the
// telegram-bot-adapter spec ("DTOs live in adapter packages") and
// Engram observation #1737, these types do NOT live in internal/app.
// The port-contract types referenced by interface signatures there
// (TelegramReviewAction, PendingValidation, BacklogItem, …) stay in
// app/ports.go — moving them would create a forbidden app→telegram
// import. The viewmodel types here are the NEW boundary PR5 hands
// to the render layer.
package telegram

import (
	"time"

	"github.com/google/uuid"
)

// TelegramReviewActionTTL bounds how long a rendered review button
// stays actionable. Mirrors PendingValidationTTL: 24 hours.
const TelegramReviewActionTTL = 24 * time.Hour

// Telegram review action identifiers. Postgres stores these
// verbatim; the callback handler matches on the constants, not the
// visible label.
const (
	TelegramReviewActionValidate  = "validate"
	TelegramReviewActionDeprecate = "deprecate"
	TelegramReviewActionDebate    = "debate"
	TelegramReviewActionSkip      = "skip"
)

// BacklogViewItem is the UI-agnostic viewmodel one backlog card
// renders. It is a projection of app.BacklogItem with a status-aware
// Actions slice the render layer (PR5) converts to
// models.InlineKeyboardMarkup. The app layer MUST NOT import
// models.InlineKeyboardMarkup.
type BacklogViewItem struct {
	ID      uuid.UUID
	Title   string
	Summary string
	Status  string
	Actions []BacklogAction
}

// BacklogAction is one tappable button. Token is the opaque
// server-side handle embedded in callback_data; Label is what
// Telegram renders. The handler mints a fresh Token per button so a
// token is single-use.
type BacklogAction struct {
	Label string
	Token string
}
