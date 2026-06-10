package telegram

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Sender abstracts Telegram message sending for testability.
type Sender interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

// telegramSender implements Sender using a real Telegram bot.
type telegramSender struct {
	b *bot.Bot
}

// SendMessage sends a text message to the specified chat.
func (s *telegramSender) SendMessage(ctx context.Context, chatID int64, text string) error {
	_, err := s.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	return err
}

// Handler processes Telegram updates and routes them to the ingestion service.
type Handler struct {
	service *app.IngestTextService
	sender  Sender
	logger  *slog.Logger
}

// NewHandler creates a Handler that sends responses via the given bot.
// b may be nil — the bot will be wired lazily by DefaultHandler when
// the first update arrives. The logger falls back to slog.Default()
// when nil.
func NewHandler(svc *app.IngestTextService, b *bot.Bot, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{service: svc, sender: &telegramSender{b: b}, logger: logger}
}

// newHandlerWithSender creates a Handler with an injected Sender (for testing).
func newHandlerWithSender(svc *app.IngestTextService, sender Sender, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{service: svc, sender: sender, logger: logger}
}

// ProcessUpdate handles a single Telegram update.
// Returns nil for commands (/start, /help) or on successful ingestion.
// Returns error only for unexpected failures (logged, not sent to user).
func (h *Handler) ProcessUpdate(ctx context.Context, update *models.Update) error {
	if update == nil {
		return nil
	}

	// Inline Keyboard callbacks (Fase 3 prep). Today this is a stub that
	// logs the callback and acks; the real handler ships with the
	// validation workflow change.
	if update.CallbackQuery != nil {
		cb := update.CallbackQuery
		h.logger.Info("telegram callback received",
			slog.String("callback_id", cb.ID),
			slog.String("data", cb.Data),
		)
		if cb.Message.Message != nil {
			return h.sender.SendMessage(ctx, cb.Message.Message.Chat.ID, "OK")
		}
		return nil
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
	messageID := strconv.Itoa(msg.ID)
	userID := strconv.FormatInt(msg.From.ID, 10)

	req := domain.IngestTextRequest{
		WorkspaceID: "default",
		Content:     msg.Text,
		Source: domain.SourceInput{
			Type:           "telegram",
			ExternalID:     messageID,
			IdempotencyKey: messageID,
			Metadata: domain.Metadata{
				"chat_id": chatID,
				"user_id": userID,
			},
		},
		Object: domain.ObjectInput{
			Type:      "document",
			CreatedBy: userID,
		},
	}

	result, err := h.service.Ingest(ctx, req)
	if err != nil {
		h.logger.Error("telegram ingest error",
			slog.Int64("chat_id", chatID),
			slog.String("message_id", messageID),
			slog.String("error", err.Error()))
		return h.sender.SendMessage(ctx, chatID, "Sorry, something went wrong processing your message.")
	}

	h.logger.Info("telegram message ingested",
		slog.Int64("chat_id", chatID),
		slog.String("message_id", messageID),
		slog.Bool("duplicate", result.Duplicate))

	if result.Duplicate {
		return h.sender.SendMessage(ctx, chatID, "Duplicate")
	}

	return h.sender.SendMessage(ctx, chatID, "Saved")
}

// DefaultHandler returns a bot.HandlerFunc suitable for bot.New / WithDefaultHandler.
// The returned function delegates to this Handler's ProcessUpdate.
//
// The bot argument is passed by the Telegram library at callback time.
// We use it to lazily wire the sender if NewHandler was called with
// a nil bot (which happens in main.go because the bot cannot be
// created without a handler that uses WithDefaultHandler).
func (h *Handler) DefaultHandler() bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		// Lazy init: if sender has a nil bot, install the real one now.
		if ts, ok := h.sender.(*telegramSender); ok && ts.b == nil {
			ts.b = b
		}
		if err := h.ProcessUpdate(ctx, update); err != nil {
			h.logger.Error("telegram unhandled error", slog.String("error", err.Error()))
		}
	}
}
