package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// handler_callback.go owns the callback dispatch flow: the
// keep/discard branch for the legacy collision-validation flow
// (handleCallback) and the rv: branch for the backlog review
// flow (handleReviewCallback). The backlog read flow lives in
// handler_backlog.go; the render split lives in handler_render.go.

// handleCallback resolves a button press: ingest the pending input, or
// discard it, then retire the buttons by editing the prompt message.
func (h *Handler) handleCallback(ctx context.Context, cb *models.CallbackQuery) error {
	action, token, ok := splitCallbackData(cb.Data)
	if !ok {
		return h.sender.AnswerCallback(ctx, cb.ID, "")
	}

	// PR3: route the rv: namespace to the backlog review flow.
	// The existing keep: / discard: branches below stay untouched
	// and continue to handle the collision-validation flow.
	if action == TelegramReviewActionNamespace {
		return h.handleReviewCallback(ctx, cb, token)
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

// handleReviewCallback dispatches an "rv:<token>" Telegram review
// button press to the matching app service. The flow is:
//
//  1. Take the token (single-use) from the PR1 review-action
//     store. A missing/expired/consumed token is reported as
//     "ya no está disponible"; a transient storage error is
//     reported as "temporal, probá de nuevo" and the message is
//     NOT edited (the human can still wait for the store to
//     recover on a future tap, even though the token is
//     single-use — Take either succeeded or it didn't, and the
//     handler does not invent a retry path that does not exist).
//  2. Verify the tapping actor and chat match the stored action.
//     A mismatch (e.g., a different user in a group chat tapped
//     the button) gets "no es para vos" and the message is NOT
//     edited. The token is already consumed by Take, so the
//     intended user cannot tap it; the message text is preserved
//     for transparency.
//  3. Skip is UI-only: re-render the next backlog card using the
//     stored NextCursor. No lifecycle service is called. If the
//     next page is empty, the card is edited to the "nothing
//     pending" message. No service is wired for skip because
//     the action must not mutate lifecycle state per the spec.
//  4. For lifecycle actions, fetch the current object via the
//     KnowledgeObjectFinder and compare its status to the stored
//     ExpectedStatus. A mismatch (stale button: someone else
//     mutated the row) gets "ya cambió" and the message is
//     edited to point the human to /backlog. No service is
//     called. A missing object is the same path with a "no
//     existe" message.
//  5. Dispatch to the matching app service by (source status,
//     action). See the switch below. Each call carries the
//     stored WorkspaceID/ObjectID, the tapping actor as
//     ActorID, and a Reason that names the Telegram channel so
//     the audit trail is auditable from a single read.
//  6. Map typed app errors (ErrInvalidTransition, ErrNotFound)
//     to user-friendly replies; transient errors are answered
//     with "temporal" and do NOT edit the message so other
//     buttons on the same card remain actionable.
//
// The thin-adapter contract is preserved: this method never
// infers lifecycle state. It loads the trusted context from the
// store, performs an identity check, an expected-status check,
// and a single app-service call; the rest is the existing
// service's policy.
func (h *Handler) handleReviewCallback(ctx context.Context, cb *models.CallbackQuery, token string) error {
	chatID := int64(0)
	messageID := 0
	if cb.Message.Message != nil {
		chatID = cb.Message.Message.Chat.ID
		messageID = cb.Message.Message.ID
	}
	actorID := cb.From.ID

	// 1. Take the token. Single-use: the row is gone after Take
	//    regardless of which branch we take below.
	action, err := h.reviewStore.Take(ctx, token)
	if err != nil {
		if errors.Is(err, app.ErrNotFound) {
			return h.sender.AnswerCallback(ctx, cb.ID, "Esta acción ya no está disponible")
		}
		h.logger.Error("telegram review action take failed",
			slog.Int64("chat_id", chatID),
			slog.String("token", token),
			slog.String("error", err.Error()))
		return h.sender.AnswerCallback(ctx, cb.ID, "Error temporal; probá de nuevo")
	}

	// 2. Auth check: actor and chat must match the saved action.
	if actorID != action.ActorID || chatID != action.ChatID {
		return h.sender.AnswerCallback(ctx, cb.ID, "Este botón no es para vos")
	}

	// 3. Skip is UI-only: re-render the next backlog card.
	if action.Action == TelegramReviewActionSkip {
		return h.renderNextBacklogCard(ctx, cb, chatID, messageID, action)
	}

	// 4. Stale check: fetch the current object and compare to
	//    ExpectedStatus. A nil finder means we cannot perform the
	//    check; treat it as a stale button to keep the
	//    "no-mutation" contract: refuse to call a service that
	//    would operate on unverified state.
	if h.finder == nil {
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "⚠️ No se pudo verificar el estado actual. Usá /backlog para ver el estado real.")
		return h.sender.AnswerCallback(ctx, cb.ID, "Estado no verificable")
	}
	obj, err := h.finder.FindByID(ctx, action.WorkspaceID, action.ObjectID)
	if err != nil {
		if errors.Is(err, app.ErrNotFound) {
			_ = h.sender.EditMessageText(ctx, chatID, messageID, "⚠️ Este objeto ya no existe.")
			return h.sender.AnswerCallback(ctx, cb.ID, "Objeto no encontrado")
		}
		h.logger.Warn("telegram review action finder failed",
			slog.Int64("chat_id", chatID),
			slog.String("object_id", action.ObjectID.String()),
			slog.String("error", err.Error()))
		return h.sender.AnswerCallback(ctx, cb.ID, "Error al leer el objeto; probá de nuevo")
	}
	if obj.Status != action.ExpectedStatus {
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "⚠️ Este elemento ya cambió de estado. Usá /backlog para ver el actual.")
		return h.sender.AnswerCallback(ctx, cb.ID, "Estado desactualizado")
	}

	// 5. Dispatch by (source status, action). Each branch owns
	//    the AppServiceError -> user-friendly reply mapping via
	//    the respondReviewServiceError helper so a single helper
	//    can edit the message, answer the callback, and log
	//    transient failures uniformly.
	actor := strconv.FormatInt(actorID, 10)
	switch {
	case action.ExpectedStatus == domain.KnowledgeObjectStatusProposed &&
		action.Action == TelegramReviewActionValidate:
		if h.validator == nil {
			return h.sender.AnswerCallback(ctx, cb.ID, "Validación no disponible")
		}
		_, err := h.validator.Validate(ctx, app.ValidateObjectRequest{
			WorkspaceID:  action.WorkspaceID,
			ObjectID:     action.ObjectID,
			TargetStatus: domain.KnowledgeObjectStatusValidated,
			ActorID:      actor,
			Reason:       "telegram validation action",
		})
		if err != nil {
			return h.respondReviewServiceError(ctx, cb, chatID, messageID, err, "validar")
		}
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "✅ Marcado como validado.")
		return h.sender.AnswerCallback(ctx, cb.ID, "Validado")

	case action.ExpectedStatus == domain.KnowledgeObjectStatusProposed &&
		action.Action == TelegramReviewActionDeprecate:
		if h.validator == nil {
			return h.sender.AnswerCallback(ctx, cb.ID, "Validación no disponible")
		}
		_, err := h.validator.Validate(ctx, app.ValidateObjectRequest{
			WorkspaceID:  action.WorkspaceID,
			ObjectID:     action.ObjectID,
			TargetStatus: domain.KnowledgeObjectStatusDeprecated,
			ActorID:      actor,
			Reason:       "telegram deprecation action",
		})
		if err != nil {
			return h.respondReviewServiceError(ctx, cb, chatID, messageID, err, "deprecar")
		}
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "🗑 Marcado como deprecado.")
		return h.sender.AnswerCallback(ctx, cb.ID, "Deprecado")

	case action.ExpectedStatus == domain.KnowledgeObjectStatusProposed &&
		action.Action == TelegramReviewActionDebate:
		if h.debater == nil {
			return h.sender.AnswerCallback(ctx, cb.ID, "Debate no disponible")
		}
		_, err := h.debater.MarkDebating(ctx, app.MarkDebatingRequest{
			WorkspaceID: action.WorkspaceID,
			ObjectID:    action.ObjectID,
			TriggeredBy: domain.DebateTriggerHuman,
			SuggestedBy: "",
			ActorID:     actor,
			Reason:      "telegram debate action",
		})
		if err != nil {
			return h.respondReviewServiceError(ctx, cb, chatID, messageID, err, "abrir debate")
		}
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "💬 Marcado como en debate.")
		return h.sender.AnswerCallback(ctx, cb.ID, "En debate")

	case action.ExpectedStatus == domain.KnowledgeObjectStatusDebating &&
		action.Action == TelegramReviewActionValidate:
		if h.debater == nil {
			return h.sender.AnswerCallback(ctx, cb.ID, "Debate no disponible")
		}
		_, err := h.debater.ResolveDebate(ctx, app.ResolveDebateRequest{
			WorkspaceID:  action.WorkspaceID,
			ObjectID:     action.ObjectID,
			TargetStatus: domain.KnowledgeObjectStatusValidated,
			ActorID:      actor,
			Reason:       "telegram validation action",
		})
		if err != nil {
			return h.respondReviewServiceError(ctx, cb, chatID, messageID, err, "resolver debate (validar)")
		}
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "✅ Debate resuelto: validado.")
		return h.sender.AnswerCallback(ctx, cb.ID, "Validado")

	case action.ExpectedStatus == domain.KnowledgeObjectStatusDebating &&
		action.Action == TelegramReviewActionDeprecate:
		if h.debater == nil {
			return h.sender.AnswerCallback(ctx, cb.ID, "Debate no disponible")
		}
		_, err := h.debater.ResolveDebate(ctx, app.ResolveDebateRequest{
			WorkspaceID:  action.WorkspaceID,
			ObjectID:     action.ObjectID,
			TargetStatus: domain.KnowledgeObjectStatusDeprecated,
			ActorID:      actor,
			Reason:       "telegram deprecation action",
		})
		if err != nil {
			return h.respondReviewServiceError(ctx, cb, chatID, messageID, err, "resolver debate (deprecar)")
		}
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "🗑 Debate resuelto: deprecado.")
		return h.sender.AnswerCallback(ctx, cb.ID, "Deprecado")

	default:
		// Unknown (source, action) pair: a future backlog status or
		// action that the handler has not been taught about. Refuse
		// to mutate; the human gets a clear "not supported" answer
		// and the message is edited to point at /backlog.
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "⚠️ Acción no soportada para el estado actual. Usá /backlog para ver el backlog.")
		return h.sender.AnswerCallback(ctx, cb.ID, "Acción no soportada")
	}
}

// respondReviewServiceError maps typed app errors and transient
// service errors to user-friendly Telegram replies. The pattern
// mirrors the existing handleCallback branch: ErrInvalidTransition
// and ErrNotFound retire the message (the human cannot recover
// without re-checking /backlog), and everything else answers with
// "temporal" without editing (the other buttons on the same card
// remain actionable while the underlying issue resolves).
//
// The verb argument is included in the ERROR log so a future
// operator can correlate a stack of identical "temporal" answers
// with the action that produced them.
func (h *Handler) respondReviewServiceError(ctx context.Context, cb *models.CallbackQuery, chatID int64, messageID int, err error, verb string) error {
	switch {
	case errors.Is(err, app.ErrInvalidTransition):
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "⚠️ Este elemento ya cambió de estado. Usá /backlog para ver el actual.")
		return h.sender.AnswerCallback(ctx, cb.ID, "Estado desactualizado")
	case errors.Is(err, app.ErrNotFound):
		_ = h.sender.EditMessageText(ctx, chatID, messageID, "⚠️ Este objeto ya no existe.")
		return h.sender.AnswerCallback(ctx, cb.ID, "Objeto no encontrado")
	default:
		h.logger.Error("telegram review action service error",
			slog.Int64("chat_id", chatID),
			slog.String("verb", verb),
			slog.String("error", err.Error()))
		return h.sender.AnswerCallback(ctx, cb.ID, "Error temporal; probá de nuevo")
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
