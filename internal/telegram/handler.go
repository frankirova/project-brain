package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/app/inmem"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// InlineButton is one tappable inline-keyboard button: a visible label
// and the opaque callback data Telegram echoes back when it is tapped.
type InlineButton struct {
	Text string
	Data string
}

// Sender abstracts Telegram message operations for testability. The
// collision-validation flow needs more than plain text: inline-keyboard
// prompts, callback acknowledgements, and in-place message edits to
// retire the buttons once the human has decided.
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

// telegramSender implements Sender using a real Telegram bot.
type telegramSender struct {
	b *bot.Bot
}

func (s *telegramSender) SendMessage(ctx context.Context, chatID int64, text string) error {
	_, err := s.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	return err
}

func (s *telegramSender) SendMessageWithButtons(ctx context.Context, chatID int64, text string, rows [][]InlineButton) error {
	kb := make([][]models.InlineKeyboardButton, 0, len(rows))
	for _, row := range rows {
		buttons := make([]models.InlineKeyboardButton, 0, len(row))
		for _, b := range row {
			buttons = append(buttons, models.InlineKeyboardButton{Text: b.Text, CallbackData: b.Data})
		}
		kb = append(kb, buttons)
	}
	_, err := s.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: kb},
	})
	return err
}

func (s *telegramSender) AnswerCallback(ctx context.Context, callbackID, text string) error {
	_, err := s.b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
		Text:            text,
	})
	return err
}

func (s *telegramSender) EditMessageText(ctx context.Context, chatID int64, messageID int, text string) error {
	_, err := s.b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
	})
	return err
}

// pendingValidation is a candidate input awaiting a human decision after
// a collision was detected. It is keyed by a short token carried in the
// inline buttons' callback data (Telegram caps callback data at 64 bytes,
// so the full text cannot ride along — it waits here).
//
// The on-disk shape lives in app.PendingValidation; this local alias
// keeps the handler code free of repeated type names. The name stays
// lower-case (unexported) to mark the alias as a handler-local
// convenience; the underlying type is the same struct the store
// receives and round-trips.
type pendingValidation = app.PendingValidation

// pendingStore is the storage boundary for in-flight validations. It
// is an alias of app.PendingValidationStore so the handler depends on
// the same interface the Postgres and in-memory implementations
// satisfy.
type pendingStore = app.PendingValidationStore

// Handler processes Telegram updates and routes them to the ingestion
// service, gated by collision detection + human validation.
type Handler struct {
	service    *app.IngestTextService
	detector   collisionChecker       // nil => legacy direct-ingest, no validation
	rawInputs  app.RawInputRepository // nil => raw_input staging disabled (no postgres)
	sender     Sender
	store      pendingStore // nil => in-memory fallback (local dev)
	logger     *slog.Logger
	newToken   func() string
}

// NewHandler creates a Handler that sends responses via the given bot
// and stores pending validations in memory. Use NewHandlerWithStore to
// plug in a durable (e.g. PostgreSQL) store. detector may be nil
// (disables collision validation; falls back to direct ingestion).
// rawInputs may be nil (disables raw_input staging; local dev / no postgres).
// b may be nil — the bot is wired lazily by DefaultHandler when the
// first update arrives. logger falls back to slog.Default() when nil.
func NewHandler(svc *app.IngestTextService, detector collisionChecker, rawInputs app.RawInputRepository, b *bot.Bot, logger *slog.Logger) *Handler {
	return newHandlerWithStore(svc, detector, rawInputs, &telegramSender{b: b}, nil, logger)
}

// NewHandlerWithStore is like NewHandler but lets the caller wire a
// durable PendingValidationStore. Pass nil store to fall back to the
// in-memory store (same as NewHandler). The composition root in
// cmd/api/main.go passes the Postgres-backed store when the database
// is available.
func NewHandlerWithStore(svc *app.IngestTextService, detector collisionChecker, rawInputs app.RawInputRepository, b *bot.Bot, logger *slog.Logger, store pendingStore) *Handler {
	return newHandlerWithStore(svc, detector, rawInputs, &telegramSender{b: b}, store, logger)
}

// newHandlerWithSender is the test seam: inject a Sender and a nil
// store so existing tests run against the in-memory fallback.
func newHandlerWithSender(svc *app.IngestTextService, detector collisionChecker, sender Sender, logger *slog.Logger) *Handler {
	return newHandlerWithStore(svc, detector, nil, sender, nil, logger)
}

// newHandlerWithStore is the single composition seam. store==nil
// installs the in-memory fallback so local dev and the existing test
// suite keep working without a database. rawInputs==nil disables
// raw_input staging (used in local dev and unit tests).
func newHandlerWithStore(svc *app.IngestTextService, detector collisionChecker, rawInputs app.RawInputRepository, sender Sender, store pendingStore, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if store == nil {
		store = inmem.NewPendingValidationStore()
	}
	return &Handler{
		service:   svc,
		detector:  detector,
		rawInputs: rawInputs,
		sender:    sender,
		store:     store,
		logger:    logger,
		newToken:  func() string { return uuid.NewString() },
	}
}

// ProcessUpdate handles a single Telegram update.
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

	return h.handleMessage(ctx, update)
}

func (h *Handler) handleStart(ctx context.Context, chatID int64) error {
	return h.sender.SendMessage(ctx, chatID, "Welcome! Send me any text and I'll save it to Knowledge Core.")
}

func (h *Handler) handleHelp(ctx context.Context, chatID int64) error {
	return h.sender.SendMessage(ctx, chatID, "Send any text message and I'll ingest it into Knowledge Core. Use /start for a welcome message.")
}

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

// handleCallback resolves a button press: ingest the pending input, or
// discard it, then retire the buttons by editing the prompt message.
func (h *Handler) handleCallback(ctx context.Context, cb *models.CallbackQuery) error {
	action, token, ok := splitCallbackData(cb.Data)
	if !ok {
		return h.sender.AnswerCallback(ctx, cb.ID, "")
	}

	var chatID int64
	var messageID int
	if cb.Message.Message != nil {
		chatID = cb.Message.Message.Chat.ID
		messageID = cb.Message.Message.ID
	}

	pending, err := h.store.Take(ctx, token)
	if err != nil {
		if errors.Is(err, app.ErrNotFound) {
			// Restart, double-tap, or expired entry. The source is untouched.
			return h.sender.AnswerCallback(ctx, cb.ID, "Esta validación ya no está disponible")
		}
		// Storage layer is broken: answer gracefully and let the human retry
		// rather than eating the callback silently. The store may or may not
		// have committed the row, so we DO NOT edit the message: a retry
		// with the same token could still succeed once the DB recovers.
		h.logger.Error("telegram pending validation take failed",
			slog.Int64("chat_id", chatID),
			slog.String("token", token),
			slog.String("error", err.Error()))
		return h.sender.AnswerCallback(ctx, cb.ID, "Error temporal; probá de nuevo")
	}

	switch action {
	case "keep":
		result, err := h.service.Ingest(ctx, pending.Request)
		if err != nil {
			h.logger.Error("telegram validated ingest failed",
				slog.Int64("chat_id", chatID),
				slog.String("error", err.Error()))
			_ = h.sender.EditMessageText(ctx, chatID, messageID, "⚠️ No se pudo guardar. Probá de nuevo.")
			return h.sender.AnswerCallback(ctx, cb.ID, "Error al guardar")
		}
		// Best-effort promotion of the raw_input (REQ-08).
		if h.rawInputs != nil && pending.RawInputID != uuid.Nil {
			if err := h.rawInputs.SetPromoted(ctx, pending.RawInputID, result.ObjectID); err != nil {
				h.logger.Warn("raw_input set_promoted failed (keep callback)",
					slog.String("raw_input_id", pending.RawInputID.String()),
					slog.String("error", err.Error()))
			}
		}
		verb := "Guardado"
		if result.Duplicate {
			verb = "Ya estaba guardado"
		}
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "✅ "+verb+" igual, a pesar de la colisión.")
		return h.sender.AnswerCallback(ctx, cb.ID, verb)

	case "discard":
		// Best-effort discard of the raw_input (REQ-09).
		// If RawInputID is the zero UUID (pre-migration record), skip silently (S-08).
		if h.rawInputs != nil && pending.RawInputID != uuid.Nil {
			if err := h.rawInputs.SetDiscarded(ctx, pending.RawInputID); err != nil {
				h.logger.Warn("raw_input set_discarded failed",
					slog.String("raw_input_id", pending.RawInputID.String()),
					slog.String("error", err.Error()))
			}
		}
		_ = h.sender.EditMessageText(ctx, chatID, messageID,
			"❌ Descartado. Ya estaba cubierto por:\n"+truncate(pending.Collision.Object.Content, 120))
		return h.sender.AnswerCallback(ctx, cb.ID, "Descartado")

	default:
		return h.sender.AnswerCallback(ctx, cb.ID, "")
	}
}

// ingestAndReplyWithRawInput runs an unvalidated ingest and reports the
// outcome. If rawInputID is non-zero and rawInputs is set, it promotes
// the raw_input row after a successful ingest (REQ-07). Best-effort:
// a promotion failure is logged at WARN and never surfaces to the user.
func (h *Handler) ingestAndReplyWithRawInput(ctx context.Context, chatID int64, req domain.IngestTextRequest, rawInputID uuid.UUID) error {
	result, err := h.service.Ingest(ctx, req)
	if err != nil {
		h.logger.Error("telegram ingest error",
			slog.Int64("chat_id", chatID),
			slog.String("error", err.Error()))
		return h.sender.SendMessage(ctx, chatID, "Sorry, something went wrong processing your message.")
	}
	h.logger.Info("telegram message ingested",
		slog.Int64("chat_id", chatID),
		slog.Bool("duplicate", result.Duplicate))

	// Best-effort promotion (REQ-07).
	if h.rawInputs != nil && rawInputID != uuid.Nil {
		if err := h.rawInputs.SetPromoted(ctx, rawInputID, result.ObjectID); err != nil {
			h.logger.Warn("raw_input set_promoted failed (direct ingest)",
				slog.String("raw_input_id", rawInputID.String()),
				slog.String("error", err.Error()))
		}
	}

	if result.Duplicate {
		return h.sender.SendMessage(ctx, chatID, "Duplicate")
	}
	return h.sender.SendMessage(ctx, chatID, "Saved")
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
// WithDefaultHandler. It lazily wires the real bot into the sender the
// first time an update arrives (NewHandler is called with a nil bot in
// main.go because the bot cannot be created without its handler).
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
