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
	logger      *slog.Logger
	newToken    func() string
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

// NewHandlerWithBacklog is the composition seam for the backlog
// review flow. It accepts the backlog query, the knowledge-object
// finder, and the review-action store alongside the existing
// dependencies. Pass nil backlog to disable /backlog (the command
// answers with a friendly "not configured" message); nil finder
// falls back to Title/Summary-only cards; nil reviewStore installs
// the in-memory fallback (same as pendingStore).
//
// The existing NewHandler and NewHandlerWithStore constructors stay
// available so the composition root in cmd/api/main.go is unchanged
// in this PR; PR4 (wiring) will switch main.go to this constructor
// when the production Postgres-backed implementations of backlog,
// finder, and reviewStore are ready.
func NewHandlerWithBacklog(
	svc *app.IngestTextService,
	detector collisionChecker,
	rawInputs app.RawInputRepository,
	b *bot.Bot,
	logger *slog.Logger,
	pending pendingStore,
	backlog backlogLister,
	finder app.KnowledgeObjectFinder,
	reviewStore reviewActionStore,
) *Handler {
	return newHandlerWithBacklog(
		svc, detector, rawInputs, &telegramSender{b: b},
		pending, logger, backlog, finder, reviewStore,
	)
}

// newHandlerWithBacklog is the test seam for backlog-aware
// handlers. pending==nil installs the in-memory fallback; so does
// reviewStore==nil. backlog==nil and finder==nil are propagated so
// the handler can answer with their "not configured" / "summary
// only" fallbacks in tests.
func newHandlerWithBacklog(
	svc *app.IngestTextService,
	detector collisionChecker,
	rawInputs app.RawInputRepository,
	sender Sender,
	pending pendingStore,
	logger *slog.Logger,
	backlog backlogLister,
	finder app.KnowledgeObjectFinder,
	reviewStore reviewActionStore,
) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if pending == nil {
		pending = inmem.NewPendingValidationStore()
	}
	if reviewStore == nil {
		reviewStore = inmem.NewTelegramReviewActionStore()
	}
	return &Handler{
		service:     svc,
		detector:    detector,
		rawInputs:   rawInputs,
		sender:      sender,
		store:       pending,
		backlog:     backlog,
		finder:      finder,
		reviewStore: reviewStore,
		logger:      logger,
		newToken:    func() string { return uuid.NewString() },
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

// handleBacklog renders the next backlog card for the MVP workspace
// "default" and issues one opaque review-action token per button.
// It does NOT execute any lifecycle mutation; PR3 takes over when
// the user taps a button.
//
// Flow:
//
//  1. Query the backlog service for one row. An empty page sends a
//     "nothing pending" text message with no buttons. A query error
//     sends a transient-error message and logs at ERROR.
//  2. Hydrate the content preview through the KnowledgeObjectFinder
//     when one is wired. A nil finder or a hydration failure falls
//     back to Title/Summary-only cards (the Find is best-effort;
//     stale cards without content preview are still actionable).
//  3. Build the status-aware button list, mint one token per button,
//     and persist each one through the PR1 review-action store with
//     the actor, chat, object, action, expected status, next cursor
//     (for skip advance), and TTL. A save failure aborts and sends
//     a friendly error reply — the tokens minted before the failure
//     are left in the store and will expire on their own.
//  4. Send the card with the inline keyboard.
func (h *Handler) handleBacklog(ctx context.Context, update *models.Update) error {
	chatID := update.Message.Chat.ID
	var actorID int64
	if update.Message.From != nil {
		actorID = update.Message.From.ID
	}

	if h.backlog == nil {
		return h.sender.SendMessage(ctx, chatID, "El backlog no está disponible en esta build.")
	}

	page, err := h.backlog.ListHumanBacklog(ctx, app.BacklogFilter{
		WorkspaceID: telegramWorkspaceDefault,
		PageSize:    1,
		Cursor:      "",
	})
	if err != nil {
		h.logger.Error("telegram backlog list failed",
			slog.Int64("chat_id", chatID),
			slog.String("error", err.Error()))
		return h.sender.SendMessage(ctx, chatID, "Error al obtener el backlog; probá más tarde.")
	}
	if len(page.Items) == 0 {
		return h.sender.SendMessage(ctx, chatID, "Nada pendiente en el backlog 🎉")
	}

	item := page.Items[0]
	hydrated := h.hydrateBacklogItem(ctx, chatID, item)

	specs := backlogButtonsForStatus(item.Status)
	tokens, saveErr := h.issueReviewActions(ctx, item, page.NextCursor, actorID, chatID, specs)
	if saveErr != nil {
		h.logger.Error("telegram review action save failed",
			slog.Int64("chat_id", chatID),
			slog.String("error", saveErr.Error()))
		return h.sender.SendMessage(ctx, chatID, "Error al preparar las acciones; probá de nuevo.")
	}

	rows := assembleBacklogRows(specs, tokens)
	return h.sender.SendMessageWithButtons(ctx, chatID, renderBacklogCardText(item, hydrated), rows)
}

// hydrateBacklogItem looks up the KnowledgeObject behind a backlog
// row so the card can show a content preview. The lookup is
// best-effort: a nil finder or a lookup error yields a nil hydrated
// pointer, which renderBacklogCardText handles by falling back to
// the Title/Summary the BacklogItem already carries.
func (h *Handler) hydrateBacklogItem(ctx context.Context, chatID int64, item app.BacklogItem) *domain.KnowledgeObject {
	if h.finder == nil {
		return nil
	}
	obj, err := h.finder.FindByID(ctx, telegramWorkspaceDefault, item.ID)
	if err != nil {
		h.logger.Warn("telegram backlog hydrate failed, falling back to summary",
			slog.Int64("chat_id", chatID),
			slog.String("object_id", item.ID.String()),
			slog.String("error", err.Error()))
		return nil
	}
	return obj
}

// issueReviewActions mints one opaque token per button and persists
// it in the review-action store. The stored row carries everything
// the PR3 callback handler will need (workspace, object, action,
// expected status, actor, chat, next cursor, expiry) so the
// callback payload stays under Telegram's 64-byte limit. On a save
// error the function returns the error along with the tokens minted
// so far; the handler turns this into a friendly Telegram reply and
// leaves the saved tokens in place — they will expire on their own
// and the PR3 Take is single-use, so leaving them around cannot
// resurrect a button.
func (h *Handler) issueReviewActions(
	ctx context.Context,
	item app.BacklogItem,
	nextCursor string,
	actorID, chatID int64,
	specs []backlogButtonSpec,
) ([]string, error) {
	tokens := make([]string, 0, len(specs))
	for _, spec := range specs {
		token := h.newToken()
		if err := h.reviewStore.Save(ctx, app.TelegramReviewAction{
			Token:          token,
			WorkspaceID:    telegramWorkspaceDefault,
			ActorID:        actorID,
			ChatID:         chatID,
			ObjectID:       item.ID,
			Action:         spec.Action,
			ExpectedStatus: item.Status,
			NextCursor:     nextCursor,
			ExpiresAt:      time.Now().Add(app.TelegramReviewActionTTL),
		}); err != nil {
			return tokens, err
		}
		tokens = append(tokens, token)
	}
	return tokens, nil
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
