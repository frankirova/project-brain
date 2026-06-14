package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// handler.go owns the Handler struct, the cross-cutting types
// (InlineButton, Sender, per-feature interfaces) the whole package
// shares, and the entry-point methods that route an update into the
// appropriate feature. The backlog read flow lives in
// handler_backlog.go, the callback dispatch flow in
// handler_callback.go, the render split in handler_render.go and
// handler_render_telegram.go.

// InlineButton is one tappable inline-keyboard button: a visible
// label and the opaque callback data Telegram echoes back when it
// is tapped. The handler-internal view of a button — handler_render.go
// produces the rows the Sender consumes, and handler_render_telegram.go
// converts those rows into models.InlineKeyboardButton when a real
// Sender is wired.
type InlineButton struct {
	Text string
	Data string
}

// Sender abstracts Telegram message operations for testability: the
// collision-validation flow needs more than plain text (inline-keyboard
// prompts, callback acknowledgements, in-place message edits to retire
// the buttons once the human has decided). It is the test seam the
// change-18 spec pins (#1736): the production composition root leaves
// Config.Sender nil and New builds a real *telegramSender from
// Config.Bot, while tests inject a fakeSender directly.
type Sender interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
	SendMessageWithButtons(ctx context.Context, chatID int64, text string, rows [][]InlineButton) error
	AnswerCallback(ctx context.Context, callbackID, text string) error
	EditMessageText(ctx context.Context, chatID int64, messageID int, text string) error
}

// collisionChecker is the slice of *app.CollisionDetector the handler
// needs: "what existing knowledge would this candidate text clash with?"
type collisionChecker interface {
	Detect(ctx context.Context, workspaceID, text string, excludeID *uuid.UUID) ([]app.Collision, error)
}

// pendingValidation is a candidate input awaiting a human decision after
// a collision was detected. It is keyed by a short token carried in the
// inline buttons' callback data (Telegram caps callback data at 64 bytes,
// so the full text cannot ride along - it waits here). The on-disk
// shape lives in app.PendingValidation; this local alias keeps the
// handler code free of repeated type names.
type pendingValidation = app.PendingValidation

// pendingStore is the storage boundary for in-flight validations. It
// is an alias of app.PendingValidationStore so the handler depends on
// the same interface the Postgres and in-memory implementations
// satisfy.
type pendingStore = app.PendingValidationStore

// backlogLister is the slice of *app.ObjectDebateService the handler
// needs: "what is the next backlog item I should render?". The MVP
// uses the workspace "default" and asks for one row at a time so the
// user gets a single focused card per /backlog. The service handles
// workspace normalization, page-size clamping, and cursor decoding;
// the handler is a thin consumer.
//
// Nil backlog disables the /backlog command (the handler answers
// with a friendly "not configured" message). This matches the
// existing pattern where detector==nil falls back to direct ingest.
type backlogLister interface {
	ListHumanBacklog(ctx context.Context, filter app.BacklogFilter) (app.BacklogPage, error)
}

// reviewActionStore is the storage boundary for opaque Telegram
// backlog review tokens. Aliased to the app-level interface so the
// handler depends on the same surface the PR1 Postgres and
// in-memory implementations satisfy.
type reviewActionStore = app.TelegramReviewActionStore

// reviewValidator is the slice of *app.ValidateObjectService the
// review callback handler needs. The MVP uses direct validation
// (no MarkDebating first) for proposed rows, and the service owns
// the proposed-source guard plus the target whitelist. Nil disables
// rv: validate/deprecate buttons with a "no disponible" answer;
// the backlog render path is unaffected.
type reviewValidator interface {
	Validate(ctx context.Context, req app.ValidateObjectRequest) (app.ValidateObjectResult, error)
}

// reviewDebator is the slice of *app.ObjectDebateService the
// review callback handler needs: MarkDebating for proposed+debate
// transitions, ResolveDebate for debating+terminal resolutions.
// Nil disables rv: debate/resolve buttons with a "no disponible"
// answer; the backlog render path is unaffected. Skip/Next is
// UI-only and never calls this interface.
type reviewDebator interface {
	MarkDebating(ctx context.Context, req app.MarkDebatingRequest) (app.MarkDebatingResult, error)
	ResolveDebate(ctx context.Context, req app.ResolveDebateRequest) (app.ResolveDebateResult, error)
}

// telegramWorkspaceDefault is the MVP workspace every backlog card
// is sourced from. Kept as a named constant so the pin-constraint
// "MVP workspace default" is visible at the call site and the
// future multi-workspace extension has one place to extend.
const telegramWorkspaceDefault = "default"

// Handler processes Telegram updates and routes them to the ingestion
// service, gated by collision detection + human validation.
type Handler struct {
	service     *app.IngestTextService
	detector    collisionChecker       // nil => legacy direct-ingest, no validation
	rawInputs   app.RawInputRepository // nil => raw_input staging disabled (no postgres)
	sender      Sender
	store       pendingStore // nil => in-memory fallback (local dev)
	backlog     backlogLister
	finder      app.KnowledgeObjectFinder
	reviewStore reviewActionStore
	validator   reviewValidator // nil => rv: validate/deprecate answers "no disponible"
	debater     reviewDebator   // nil => rv: debate/resolve answers "no disponible"
	logger      *slog.Logger
	newToken    func() string
}

// ProcessUpdate handles a single Telegram update. The dispatch is
// intentionally trivial: a callback update goes to the callback
// branch, a command is recognised by exact text match, and
// everything else falls through to handleMessage (which is where
// the collision / ingest decision lives).
func (h *Handler) ProcessUpdate(ctx context.Context, update *models.Update) error {
	if update == nil {
		return nil
	}
	if update.CallbackQuery != nil {
		return h.handleCallback(ctx, update.CallbackQuery)
	}
	if update.Message == nil {
		return nil
	}

	text := strings.TrimSpace(update.Message.Text)
	chatID := update.Message.Chat.ID

	if text == "/start" {
		return h.handleStart(ctx, chatID)
	}
	if text == "/help" {
		return h.handleHelp(ctx, chatID)
	}
	if text == "/backlog" {
		return h.handleBacklog(ctx, update)
	}

	return h.handleMessage(ctx, update)
}

func (h *Handler) handleStart(ctx context.Context, chatID int64) error {
	return h.sender.SendMessage(ctx, chatID, "Welcome! Send me any text and I'll save it to Knowledge Core.")
}

func (h *Handler) handleHelp(ctx context.Context, chatID int64) error {
	return h.sender.SendMessage(ctx, chatID, "Send any text message and I'll ingest it into Knowledge Core. Use /start for a welcome message.")
}

// handleMessage is the ingest entry point. It stages the raw_input
// row, runs collision detection, and either asks the human for a
// decision (collision) or commits the message directly. The
// callback branch lives in handleCallback (handler_callback.go);
// the backlog read flow lives in handleBacklog (handler_backlog.go).
func (h *Handler) handleMessage(ctx context.Context, update *models.Update) error {
	msg := update.Message
	chatID := msg.Chat.ID
	req := buildIngestRequest(msg)

	// TASK-07: Write-first raw_input staging (REQ-05).
	// Create the raw_input row before any collision check or ingest so
	// every message that enters the system is captured. Failure here is
	// logged at ERROR and the handler degrades to normal behavior —
	// collision detection and ingest still run.
	rawInputID := uuid.New()
	if h.rawInputs != nil {
		actorID := strconv.FormatInt(msg.From.ID, 10)
		ri := domain.RawInput{
			ID:          rawInputID,
			WorkspaceID: req.WorkspaceID,
			Channel:     domain.RawInputChannelTelegram,
			Content:     msg.Text,
			ActorID:     actorID,
			ExternalRef: domain.Metadata{
				"chat_id":    msg.Chat.ID,
				"message_id": strconv.Itoa(msg.ID),
			},
			Status: domain.RawInputStatusPending,
		}
		if err := h.rawInputs.Create(ctx, ri); err != nil {
			h.logger.Error("raw_input create failed, continuing without staging",
				slog.Int64("chat_id", chatID),
				slog.String("error", err.Error()))
			// Reset so promote/discard guards see a zero UUID and skip.
			rawInputID = uuid.Nil
		}
	}

	// No detector configured (no embeddings): keep the original behaviour.
	if h.detector == nil {
		return h.ingestAndReplyWithRawInput(ctx, chatID, req, rawInputID)
	}

	collisions, err := h.detector.Detect(ctx, req.WorkspaceID, req.Content, nil)
	if err != nil {
		// Validation is best-effort. A collision-check failure must never
		// block ingestion — degrade to a direct save.
		h.logger.Warn("collision check failed, ingesting without validation",
			slog.Int64("chat_id", chatID),
			slog.String("error", err.Error()))
		return h.ingestAndReplyWithRawInput(ctx, chatID, req, rawInputID)
	}
	if len(collisions) == 0 {
		return h.ingestAndReplyWithRawInput(ctx, chatID, req, rawInputID)
	}

	// Collision detected: update collision_summary before sending the
	// keyboard (REQ-06). Best-effort: failure does not block the prompt.
	top := collisions[0]
	if h.rawInputs != nil && rawInputID != uuid.Nil {
		summary := domain.Metadata{
			"verdict":         top.Verdict,
			"similarity":      top.Similarity,
			"object_id":       top.Object.ID.String(),
			"content_preview": truncate(top.Object.Content, 200),
		}
		if err := h.rawInputs.SetCollisionSummary(ctx, rawInputID, summary); err != nil {
			h.logger.Warn("raw_input set_collision_summary failed",
				slog.String("raw_input_id", rawInputID.String()),
				slog.String("error", err.Error()))
		}
	}

	// Ask the human before this becomes canonical.
	token := h.newToken()
	if err := h.store.Save(ctx, pendingValidation{
		Token:      token,
		ChatID:     chatID,
		Request:    req,
		Collision:  top,
		RawInputID: rawInputID,
		// Stamp the TTL on every prompt. Take and SweepExpired
		// both honour this cutoff; without it the row would sit
		// in the table until the human taps the button, which is
		// the exact "abandoned prompt" case the TTL is meant to
		// police.
		ExpiresAt: time.Now().Add(app.PendingValidationTTL),
	}); err != nil {
		// Durability is best-effort. A save failure must never block
		// ingestion — degrade to a direct save and let the human retry.
		h.logger.Error("telegram pending validation save failed",
			slog.Int64("chat_id", chatID),
			slog.String("error", err.Error()))
		return h.ingestAndReplyWithRawInput(ctx, chatID, req, rawInputID)
	}

	rows := [][]InlineButton{{
		{Text: "✅ Guardar igual", Data: "keep:" + token},
		{Text: "❌ Descartar", Data: "discard:" + token},
	}}
	return h.sender.SendMessageWithButtons(ctx, chatID, formatCollisionPrompt(req.Content, top), rows)
}

// buildIngestRequest maps a Telegram message to an ingestion request.
func buildIngestRequest(msg *models.Message) domain.IngestTextRequest {
	messageID := strconv.Itoa(msg.ID)
	userID := strconv.FormatInt(msg.From.ID, 10)
	return domain.IngestTextRequest{
		WorkspaceID: "default",
		Content:     msg.Text,
		Source: domain.SourceInput{
			Type:           "telegram",
			ExternalID:     messageID,
			IdempotencyKey: messageID,
			Metadata: domain.Metadata{
				"chat_id": msg.Chat.ID,
				"user_id": userID,
			},
		},
		Object: domain.ObjectInput{
			Type:      "document",
			CreatedBy: userID,
		},
	}
}

// formatCollisionPrompt renders the human-facing collision warning.
func formatCollisionPrompt(candidate string, c app.Collision) string {
	return fmt.Sprintf(
		"⚠️ Esto puede chocar con conocimiento existente (%s, %.0f%%).\n\n"+
			"Tu mensaje:\n%s\n\n"+
			"Choca con:\n%s\n\n"+
			"¿Qué hacés?",
		c.Verdict, c.Similarity*100,
		truncate(candidate, 200),
		truncate(c.Object.Content, 200),
	)
}

// splitCallbackData parses "<action>:<token>" callback data.
func splitCallbackData(data string) (action, token string, ok bool) {
	action, token, ok = strings.Cut(data, ":")
	if !ok || action == "" || token == "" {
		return "", "", false
	}
	return action, token, true
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// DefaultHandler returns a bot.HandlerFunc suitable for bot.New /
// WithDefaultHandler. It lazily wires the real bot into the sender
// the first time an update arrives (New is called with a nil bot
// in main.go because the bot cannot be created without its
// handler, and the bot library patches the handler into the bot
// in the same call).
func (h *Handler) DefaultHandler() bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if ts, ok := h.sender.(*telegramSender); ok && ts.b == nil {
			ts.b = b
		}
		if err := h.ProcessUpdate(ctx, update); err != nil {
			h.logger.Error("telegram unhandled error", slog.String("error", err.Error()))
		}
	}
}
