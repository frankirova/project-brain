package telegram

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// handler_render_telegram.go owns the adapter-side render boundary:
// the implementation of Sender that talks to the real Telegram
// bot SDK, including the conversion from the handler's UI-agnostic
// InlineButton rows to models.InlineKeyboardMarkup.
//
// The spec for change-18 (telegram-bot-adapter §"render split")
// requires this file to be the ONLY place in the package that
// imports models.InlineKeyboardMarkup / models.InlineKeyboardButton.
// The viewmodel assembly in handler_render.go deals in
// []BacklogViewItem / []BacklogAction (from dto.go) and never
// reaches across into the SDK types. handler.go's business logic
// likewise stays out of the SDK render — it calls into the
// Sender interface, which the type below satisfies.

// telegramSender is the production Sender. It wraps a *bot.Bot
// the DefaultHandler captures lazily on the first update
// (the bot cannot be created without a handler, so the
// composition root constructs the Handler with a nil bot and
// the bot library patches it in on the first dispatch).
type telegramSender struct {
	b *bot.Bot
}

// SendMessage posts a plain text reply with no inline keyboard.
// Used for /start, /help, the "duplicate" and "saved" replies,
// and every transient error message the handler emits.
func (s *telegramSender) SendMessage(ctx context.Context, chatID int64, text string) error {
	_, err := s.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	return err
}

// SendMessageWithButtons posts a message that carries an inline
// keyboard. The rows argument is the handler's UI-agnostic
// representation: each button is a (Text, Data) pair where
// Data is the opaque callback payload Telegram echoes back
// when the user taps it. This function is the sole place in
// the package that crosses the boundary into the SDK's
// models.InlineKeyboardMarkup / models.InlineKeyboardButton
// types — every other file stays on the app-facing viewmodel
// or the Sender interface.
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

// AnswerCallback dismisses the "loading" badge Telegram shows
// on top of the inline keyboard after a tap, and shows the
// caller-supplied text as a toast. Used by every callback
// branch (keep, discard, rv: validate, rv: debate, …) to
// acknowledge the tap.
func (s *telegramSender) AnswerCallback(ctx context.Context, callbackID, text string) error {
	_, err := s.b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
		Text:            text,
	})
	return err
}

// EditMessageText replaces the text of a message Telegram
// already delivered. The handler uses it to retire the
// inline-keyboard prompt once the human has decided
// (keep / discard / rv: validate / …), so the buttons
// disappear the moment they no longer apply.
func (s *telegramSender) EditMessageText(ctx context.Context, chatID int64, messageID int, text string) error {
	_, err := s.b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
	})
	return err
}
