package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/frankirova/project-brain/internal/app"
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
type pendingValidation struct {
	req       domain.IngestTextRequest
	collision app.Collision
}

// Handler processes Telegram updates and routes them to the ingestion
// service, gated by collision detection + human validation.
type Handler struct {
	service  *app.IngestTextService
	detector collisionChecker // nil => legacy direct-ingest, no validation
	sender   Sender
	logger   *slog.Logger

	// pending holds candidate inputs awaiting a button press. In-memory
	// and single-instance: entries are lost on restart, which only means
	// a stale button reports "expired" — the source message is untouched.
	mu       sync.Mutex
	pending  map[string]pendingValidation
	newToken func() string
}

// NewHandler creates a Handler that sends responses via the given bot.
// detector may be nil (disables collision validation; falls back to
// direct ingestion). b may be nil — the bot is wired lazily by
// DefaultHandler when the first update arrives. logger falls back to
// slog.Default() when nil.
func NewHandler(svc *app.IngestTextService, detector collisionChecker, b *bot.Bot, logger *slog.Logger) *Handler {
	return newHandlerWithSender(svc, detector, &telegramSender{b: b}, logger)
}

// newHandlerWithSender creates a Handler with an injected Sender (for testing).
func newHandlerWithSender(svc *app.IngestTextService, detector collisionChecker, sender Sender, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		service:  svc,
		detector: detector,
		sender:   sender,
		logger:   logger,
		pending:  make(map[string]pendingValidation),
		newToken: func() string { return uuid.NewString() },
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

	// No detector configured (no embeddings): keep the original behaviour.
	if h.detector == nil {
		return h.ingestAndReply(ctx, chatID, req)
	}

	collisions, err := h.detector.Detect(ctx, req.WorkspaceID, req.Content, nil)
	if err != nil {
		// Validation is best-effort. A collision-check failure must never
		// block ingestion — degrade to a direct save.
		h.logger.Warn("collision check failed, ingesting without validation",
			slog.Int64("chat_id", chatID),
			slog.String("error", err.Error()))
		return h.ingestAndReply(ctx, chatID, req)
	}
	if len(collisions) == 0 {
		return h.ingestAndReply(ctx, chatID, req)
	}

	// Collision detected: ask the human before this becomes canonical.
	top := collisions[0]
	token := h.newToken()
	h.savePending(token, pendingValidation{req: req, collision: top})

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

	pending, found := h.takePending(token)
	if !found {
		// Restart, double-tap, or expired entry. The source is untouched.
		return h.sender.AnswerCallback(ctx, cb.ID, "Esta validación ya no está disponible")
	}

	switch action {
	case "keep":
		result, err := h.service.Ingest(ctx, pending.req)
		if err != nil {
			h.logger.Error("telegram validated ingest failed",
				slog.Int64("chat_id", chatID),
				slog.String("error", err.Error()))
			_ = h.sender.EditMessageText(ctx, chatID, messageID, "⚠️ No se pudo guardar. Probá de nuevo.")
			return h.sender.AnswerCallback(ctx, cb.ID, "Error al guardar")
		}
		verb := "Guardado"
		if result.Duplicate {
			verb = "Ya estaba guardado"
		}
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "✅ "+verb+" igual, a pesar de la colisión.")
		return h.sender.AnswerCallback(ctx, cb.ID, verb)

	case "discard":
		_ = h.sender.EditMessageText(ctx, chatID, messageID,
			"❌ Descartado. Ya estaba cubierto por:\n"+truncate(pending.collision.Object.Content, 120))
		return h.sender.AnswerCallback(ctx, cb.ID, "Descartado")

	default:
		return h.sender.AnswerCallback(ctx, cb.ID, "")
	}
}

// ingestAndReply runs an unvalidated ingest and reports the outcome.
func (h *Handler) ingestAndReply(ctx context.Context, chatID int64, req domain.IngestTextRequest) error {
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

	if result.Duplicate {
		return h.sender.SendMessage(ctx, chatID, "Duplicate")
	}
	return h.sender.SendMessage(ctx, chatID, "Saved")
}

func (h *Handler) savePending(token string, p pendingValidation) {
	h.mu.Lock()
	h.pending[token] = p
	h.mu.Unlock()
}

// takePending atomically loads and removes a pending entry so a button
// can only be acted on once.
func (h *Handler) takePending(token string) (pendingValidation, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.pending[token]
	if ok {
		delete(h.pending, token)
	}
	return p, ok
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
