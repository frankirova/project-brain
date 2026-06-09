package telegram

import (
	"context"
	"fmt"
	"log"
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
}

// NewHandler creates a Handler that sends responses via the given bot.
func NewHandler(svc *app.IngestTextService, b *bot.Bot) *Handler {
	return &Handler{service: svc, sender: &telegramSender{b: b}}
}

// newHandlerWithSender creates a Handler with an injected Sender (for testing).
func newHandlerWithSender(svc *app.IngestTextService, sender Sender) *Handler {
	return &Handler{service: svc, sender: sender}
}

// SetBot configures the handler to send messages via the given bot.
// Call this after bot.New when the bot instance becomes available.
func (h *Handler) SetBot(b *bot.Bot) {
	h.sender = &telegramSender{b: b}
}

// ProcessUpdate handles a single Telegram update.
// Returns nil for commands (/start, /help) or on successful ingestion.
// Returns error only for unexpected failures (logged, not sent to user).
func (h *Handler) ProcessUpdate(ctx context.Context, update *models.Update) error {
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
		log.Printf("telegram: ingest error chat_id=%d message_id=%s: %v", chatID, messageID, err)
		return h.sender.SendMessage(ctx, chatID, "Sorry, something went wrong processing your message.")
	}

	if result.Duplicate {
		return h.sender.SendMessage(ctx, chatID, "Duplicate")
	}

	return h.sender.SendMessage(ctx, chatID, "Saved")
}

// DefaultHandler returns a bot.HandlerFunc suitable for bot.New / WithDefaultHandler.
// The returned function delegates to this Handler's ProcessUpdate.
func (h *Handler) DefaultHandler() bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if err := h.ProcessUpdate(ctx, update); err != nil {
			fmt.Fprintf(log.Writer(), "telegram: unhandled error: %v\n", err)
		}
	}
}
