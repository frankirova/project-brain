package telegram

import (
	"context"
	"log/slog"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/go-telegram/bot/models"
)

// handler_backlog.go owns the backlog read flow: /backlog command
// dispatch, the "next card" path the Skip button triggers, and
// the small helpers (hydration, token minting) those two methods
// share. The callback dispatch (the rv: branch in handleCallback
// and the whole review-callback switch) lives in
// handler_callback.go; the render split lives in handler_render.go.

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

// renderNextBacklogCard edits the original backlog card in place
// to show the next page's item, or the empty-backlog state. It
// never mutates lifecycle state (per the spec, Skip is UI-only),
// and it never inserts a new message — editing in place matches
// the inline-keyboard UX where buttons retire as the user
// interacts with the card.
//
// The handler reuses the same issueReviewActions path the
// /backlog command uses so a card produced by Skip carries the
// same shape (status-aware buttons, NextCursor, TTL) as one
// produced by /backlog. The only difference is the source of
// the next page's filter: Skip uses action.NextCursor instead
// of the empty cursor /backlog uses.
func (h *Handler) renderNextBacklogCard(ctx context.Context, cb *models.CallbackQuery, chatID int64, messageID int, action app.TelegramReviewAction) error {
	if h.backlog == nil {
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "El backlog no está disponible en esta build.")
		return h.sender.AnswerCallback(ctx, cb.ID, "Backlog no disponible")
	}
	page, err := h.backlog.ListHumanBacklog(ctx, app.BacklogFilter{
		WorkspaceID: action.WorkspaceID,
		PageSize:    1,
		Cursor:      action.NextCursor,
	})
	if err != nil {
		h.logger.Error("telegram review action skip list failed",
			slog.Int64("chat_id", chatID),
			slog.String("error", err.Error()))
		return h.sender.AnswerCallback(ctx, cb.ID, "Error al cargar el siguiente; probá más tarde")
	}
	if len(page.Items) == 0 {
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "Nada pendiente en el backlog 🎉")
		return h.sender.AnswerCallback(ctx, cb.ID, "Sin más elementos")
	}
	next := page.Items[0]
	hydrated := h.hydrateBacklogItem(ctx, chatID, next)
	specs := backlogButtonsForStatus(next.Status)
	tokens, saveErr := h.issueReviewActions(ctx, next, page.NextCursor, action.ActorID, chatID, specs)
	if saveErr != nil {
		h.logger.Error("telegram review action skip save failed",
			slog.Int64("chat_id", chatID),
			slog.String("error", saveErr.Error()))
		return h.sender.AnswerCallback(ctx, cb.ID, "Error al preparar el siguiente; probá de nuevo")
	}
	rows := assembleBacklogRows(specs, tokens)
	if err := h.sender.SendMessageWithButtons(ctx, chatID, renderBacklogCardText(next, hydrated), rows); err != nil {
		h.logger.Error("telegram review action skip send failed",
			slog.Int64("chat_id", chatID),
			slog.String("error", err.Error()))
		return h.sender.AnswerCallback(ctx, cb.ID, "Error al enviar el siguiente; probá de nuevo")
	}
	// Skip the message edit: the new card lives in a fresh
	// message, the original card's buttons are now stale and the
	// human can dismiss it. The "Skip" answer tells Telegram the
	// toast to show above the inline keyboard.
	return h.sender.AnswerCallback(ctx, cb.ID, "Saltado")
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
			ExpiresAt:      time.Now().Add(TelegramReviewActionTTL),
		}); err != nil {
			return tokens, err
		}
		tokens = append(tokens, token)
	}
	return tokens, nil
}
