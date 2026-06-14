package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// --- Fakes for the review-callback flow ---

// fakeValidator records every Validate call and can be configured
// to return a canned error so tests can drive the full set of
// service outcomes (success, transient, ErrNotFound,
// ErrInvalidTransition).
type fakeValidator struct {
	calls []app.ValidateObjectRequest
	err   error
}

func (v *fakeValidator) Validate(_ context.Context, req app.ValidateObjectRequest) (app.ValidateObjectResult, error) {
	v.calls = append(v.calls, req)
	if v.err != nil {
		return app.ValidateObjectResult{}, v.err
	}
	return app.ValidateObjectResult{
		ObjectID:     req.ObjectID,
		Status:       req.TargetStatus,
		AuditEventID: uuid.New(),
	}, nil
}

// fakeDebator records every MarkDebating and ResolveDebate call
// and can be configured to return canned errors per method so
// tests can drive both service paths independently.
type fakeDebator struct {
	markCalls    []app.MarkDebatingRequest
	resolveCalls []app.ResolveDebateRequest
	markErr      error
	resolveErr   error
}

func (d *fakeDebator) MarkDebating(_ context.Context, req app.MarkDebatingRequest) (app.MarkDebatingResult, error) {
	d.markCalls = append(d.markCalls, req)
	if d.markErr != nil {
		return app.MarkDebatingResult{}, d.markErr
	}
	return app.MarkDebatingResult{
		ObjectID:     req.ObjectID,
		Status:       domain.KnowledgeObjectStatusDebating,
		AuditEventID: uuid.New(),
	}, nil
}

func (d *fakeDebator) ResolveDebate(_ context.Context, req app.ResolveDebateRequest) (app.ResolveDebateResult, error) {
	d.resolveCalls = append(d.resolveCalls, req)
	if d.resolveErr != nil {
		return app.ResolveDebateResult{}, d.resolveErr
	}
	return app.ResolveDebateResult{
		ObjectID:     req.ObjectID,
		Status:       req.TargetStatus,
		AuditEventID: uuid.New(),
	}, nil
}

// --- Test helpers ---

// newTestHandlerWithReviewActions builds a Handler wired with the
// full backlog+review-callback stack: backlog query, finder,
// review store, validator, and debater. detector, rawInputs, and
// pending store default to nil/in-memory.
func newTestHandlerWithReviewActions(
	sender *fakeSender,
	backlog backlogLister,
	finder app.KnowledgeObjectFinder,
	review reviewActionStore,
	validator reviewValidator,
	debater reviewDebator,
) *Handler {
	uow := &fakeIngestionUOW{repos: &testRepos{source: &fakeSourceRepo{}}}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	return newHandlerWithBacklog(svc, nil, nil, sender, nil, nil, backlog, finder, review, validator, debater)
}

// reviewCallbackUpdate returns a callback update carrying a From
// user and a message context, the shape the rv: handler reads to
// verify actor/chat identity and to know which message to edit.
func reviewCallbackUpdate(data string, chatID int64, messageID int, fromID int64) *models.Update {
	return &models.Update{
		CallbackQuery: &models.CallbackQuery{
			ID:   "cb-rv",
			Data: data,
			From: models.User{ID: fromID},
			Message: models.MaybeInaccessibleMessage{
				Message: &models.Message{ID: messageID, Chat: models.Chat{ID: chatID}},
			},
		},
	}
}

// seedReviewAction drops a fresh action into the store so the
// rv: callback can Take it. Returns the token that was saved. The
// test helper sets a non-zero ExpiresAt by default so the action
// is reachable through the store's TTL filter.
func seedReviewAction(t *testing.T, store reviewActionStore, action app.TelegramReviewAction) string {
	t.Helper()
	if action.ExpiresAt.IsZero() {
		action.ExpiresAt = time.Now().Add(TelegramReviewActionTTL)
	}
	if err := store.Save(context.Background(), action); err != nil {
		t.Fatalf("seed review action: %v", err)
	}
	return action.Token
}

// --- Happy path: each (source, action) dispatches correctly ---

// TestReviewCallbackHappyPath covers every supported (source
// status, action) pair. Each subtest seeds an action, dispatches
// the matching callback, and asserts that exactly one
// validator/debater call happened with the right arguments and
// that the message was edited to the success text.
func TestReviewCallbackHappyPath(t *testing.T) {
	const chatID int64 = 100
	const fromID int64 = 200
	const workspace = "default"
	objectID := uuid.New()

	cases := []struct {
		name           string
		sourceStatus   string
		action         string
		wantEditedText string
		wantAnswerText string
		assertService  func(t *testing.T, v *fakeValidator, d *fakeDebator)
	}{
		{
			name:           "proposed + validate dispatches to Validate(target=validated)",
			sourceStatus:   domain.KnowledgeObjectStatusProposed,
			action:         TelegramReviewActionValidate,
			wantEditedText: "validado",
			wantAnswerText: "Validado",
			assertService: func(t *testing.T, v *fakeValidator, d *fakeDebator) {
				if len(v.calls) != 1 {
					t.Fatalf("Validate calls = %d, want 1", len(v.calls))
				}
				got := v.calls[0]
				if got.WorkspaceID != workspace || got.ObjectID != objectID {
					t.Errorf("Validate identity = %+v, want %s/%s", got, workspace, objectID)
				}
				if got.TargetStatus != domain.KnowledgeObjectStatusValidated {
					t.Errorf("Validate target = %q, want validated", got.TargetStatus)
				}
				if got.ActorID != strconv.FormatInt(fromID, 10) {
					t.Errorf("Validate actor = %q, want %d", got.ActorID, fromID)
				}
				if len(d.markCalls) != 0 || len(d.resolveCalls) != 0 {
					t.Errorf("debate service must not be called for proposed+validate: mark=%d resolve=%d", len(d.markCalls), len(d.resolveCalls))
				}
			},
		},
		{
			name:           "proposed + deprecate dispatches to Validate(target=deprecated)",
			sourceStatus:   domain.KnowledgeObjectStatusProposed,
			action:         TelegramReviewActionDeprecate,
			wantEditedText: "deprecado",
			wantAnswerText: "Deprecado",
			assertService: func(t *testing.T, v *fakeValidator, d *fakeDebator) {
				if len(v.calls) != 1 {
					t.Fatalf("Validate calls = %d, want 1", len(v.calls))
				}
				if v.calls[0].TargetStatus != domain.KnowledgeObjectStatusDeprecated {
					t.Errorf("Validate target = %q, want deprecated", v.calls[0].TargetStatus)
				}
			},
		},
		{
			name:           "proposed + debate dispatches to MarkDebating(human, no SuggestedBy)",
			sourceStatus:   domain.KnowledgeObjectStatusProposed,
			action:         TelegramReviewActionDebate,
			wantEditedText: "debate",
			wantAnswerText: "En debate",
			assertService: func(t *testing.T, v *fakeValidator, d *fakeDebator) {
				if len(d.markCalls) != 1 {
					t.Fatalf("MarkDebating calls = %d, want 1", len(d.markCalls))
				}
				got := d.markCalls[0]
				if got.TriggeredBy != domain.DebateTriggerHuman {
					t.Errorf("TriggeredBy = %q, want human", got.TriggeredBy)
				}
				if got.SuggestedBy != "" {
					t.Errorf("SuggestedBy = %q, want empty for human-explicit", got.SuggestedBy)
				}
				if got.ActorID != strconv.FormatInt(fromID, 10) {
					t.Errorf("ActorID = %q, want %d", got.ActorID, fromID)
				}
				if len(v.calls) != 0 || len(d.resolveCalls) != 0 {
					t.Errorf("validate/resolve must not be called for proposed+debate: validate=%d resolve=%d", len(v.calls), len(d.resolveCalls))
				}
			},
		},
		{
			name:           "debating + validate dispatches to ResolveDebate(target=validated)",
			sourceStatus:   domain.KnowledgeObjectStatusDebating,
			action:         TelegramReviewActionValidate,
			wantEditedText: "validado",
			wantAnswerText: "Validado",
			assertService: func(t *testing.T, v *fakeValidator, d *fakeDebator) {
				if len(d.resolveCalls) != 1 {
					t.Fatalf("ResolveDebate calls = %d, want 1", len(d.resolveCalls))
				}
				got := d.resolveCalls[0]
				if got.WorkspaceID != workspace || got.ObjectID != objectID {
					t.Errorf("ResolveDebate identity = %+v", got)
				}
				if got.TargetStatus != domain.KnowledgeObjectStatusValidated {
					t.Errorf("ResolveDebate target = %q, want validated", got.TargetStatus)
				}
				if len(v.calls) != 0 || len(d.markCalls) != 0 {
					t.Errorf("validate/mark must not be called for debating+validate: validate=%d mark=%d", len(v.calls), len(d.markCalls))
				}
			},
		},
		{
			name:           "debating + deprecate dispatches to ResolveDebate(target=deprecated)",
			sourceStatus:   domain.KnowledgeObjectStatusDebating,
			action:         TelegramReviewActionDeprecate,
			wantEditedText: "deprecado",
			wantAnswerText: "Deprecado",
			assertService: func(t *testing.T, v *fakeValidator, d *fakeDebator) {
				if len(d.resolveCalls) != 1 {
					t.Fatalf("ResolveDebate calls = %d, want 1", len(d.resolveCalls))
				}
				if d.resolveCalls[0].TargetStatus != domain.KnowledgeObjectStatusDeprecated {
					t.Errorf("ResolveDebate target = %q, want deprecated", d.resolveCalls[0].TargetStatus)
				}
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			sender := &fakeSender{}
			backlog := &fakeBacklog{}
			finder := &fakeFinder{obj: &domain.KnowledgeObject{ID: objectID, Status: tt.sourceStatus}}
			store := newInMemReviewStore()
			validator := &fakeValidator{}
			debater := &fakeDebator{}
			handler := newTestHandlerWithReviewActions(sender, backlog, finder, store, validator, debater)

			action := app.TelegramReviewAction{
				Token:          "tok-happy-" + tt.action,
				WorkspaceID:    workspace,
				ActorID:        fromID,
				ChatID:         chatID,
				ObjectID:       objectID,
				Action:         tt.action,
				ExpectedStatus: tt.sourceStatus,
			}
			seedReviewAction(t, store, action)

			data := TelegramReviewActionPayload(action.Token)
			if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, chatID, 555, fromID)); err != nil {
				t.Fatalf("callback: %v", err)
			}

			tt.assertService(t, validator, debater)

			if len(sender.edits) != 1 {
				t.Fatalf("expected 1 message edit, got %d: %+v", len(sender.edits), sender.edits)
			}
			if !strings.Contains(strings.ToLower(sender.edits[0].text), tt.wantEditedText) {
				t.Errorf("edit text = %q, want substring %q", sender.edits[0].text, tt.wantEditedText)
			}
			if sender.edits[0].messageID != 555 || sender.edits[0].chatID != chatID {
				t.Errorf("edit targeted wrong message: %+v", sender.edits[0])
			}
			if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, tt.wantAnswerText) {
				t.Fatalf("expected 1 answer with %q, got %+v", tt.wantAnswerText, sender.answers)
			}
			// Token was consumed (Take is single-use).
			if _, ok := store.data[action.Token]; ok {
				t.Errorf("token still present after successful callback")
			}
		})
	}
}

// --- Skip re-renders the next backlog card using NextCursor ---

// TestReviewCallbackSkipAdvancesToNextCard verifies that Skip is
// UI-only: the backlog query is re-issued with the stored
// NextCursor, a fresh card is sent (not the original edited),
// no lifecycle service is called, and the answer is "Saltado".
func TestReviewCallbackSkipAdvancesToNextCard(t *testing.T) {
	const chatID int64 = 100
	const fromID int64 = 200
	const workspace = "default"

	firstID := uuid.New()
	secondID := uuid.New()
	firstAction := app.TelegramReviewAction{
		Token:          "tok-skip-1",
		WorkspaceID:    workspace,
		ActorID:        fromID,
		ChatID:         chatID,
		ObjectID:       firstID,
		Action:         TelegramReviewActionSkip,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
		NextCursor:     "cursor-after-first",
	}
	nextItem := app.BacklogItem{
		ID:     secondID,
		Status: domain.KnowledgeObjectStatusDebating,
		Title:  "Next item",
	}

	sender := &fakeSender{}
	backlog := &fakeBacklog{page: app.BacklogPage{Items: []app.BacklogItem{nextItem}, NextCursor: ""}}
	finder := &fakeFinder{}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, backlog, finder, store, validator, debater)
	seedReviewAction(t, store, firstAction)

	data := TelegramReviewActionPayload(firstAction.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, chatID, 555, fromID)); err != nil {
		t.Fatalf("callback: %v", err)
	}

	if backlog.lastFilter.Cursor != "cursor-after-first" {
		t.Errorf("backlog cursor = %q, want cursor-after-first (from stored NextCursor)", backlog.lastFilter.Cursor)
	}
	if backlog.lastFilter.WorkspaceID != workspace {
		t.Errorf("backlog WorkspaceID = %q, want %q", backlog.lastFilter.WorkspaceID, workspace)
	}
	if backlog.lastFilter.PageSize != 1 {
		t.Errorf("backlog PageSize = %d, want 1", backlog.lastFilter.PageSize)
	}
	// No lifecycle mutation: validator/debater must not be called.
	if len(validator.calls) != 0 || len(debater.markCalls) != 0 || len(debater.resolveCalls) != 0 {
		t.Errorf("skip must not call any lifecycle service: validate=%d mark=%d resolve=%d",
			len(validator.calls), len(debater.markCalls), len(debater.resolveCalls))
	}
	// The original message is not edited; a new prompt is sent.
	if len(sender.edits) != 0 {
		t.Errorf("skip must not edit the original message, got %+v", sender.edits)
	}
	if len(sender.prompts) != 1 {
		t.Fatalf("expected 1 new prompt, got %d", len(sender.prompts))
	}
	if !strings.Contains(sender.prompts[0].text, "Next item") {
		t.Errorf("next card missing title: %q", sender.prompts[0].text)
	}
	// The new card must carry fresh review tokens.
	if len(sender.prompts[0].rows) == 0 {
		t.Fatalf("next card has no buttons")
	}
	buttons := 0
	for _, row := range sender.prompts[0].rows {
		buttons += len(row)
	}
	if buttons != 3 {
		t.Errorf("next card buttons = %d, want 3 (debating = validate/deprecate/skip)", buttons)
	}
	if len(store.data) != 3 {
		t.Errorf("expected 3 fresh review actions for the new card, got %d", len(store.data))
	}
	// The original token is consumed; the new card's tokens are
	// different.
	if _, ok := store.data[firstAction.Token]; ok {
		t.Errorf("original skip token must be consumed")
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "Saltado") {
		t.Fatalf("expected 'Saltado' answer, got %+v", sender.answers)
	}
}

// TestReviewCallbackSkipEmptyBacklog verifies that Skip on the
// last page of the backlog edits the original message to the
// "nothing pending" text and answers "Sin más elementos" instead
// of sending a new card.
func TestReviewCallbackSkipEmptyBacklog(t *testing.T) {
	const chatID int64 = 100
	const fromID int64 = 200

	action := app.TelegramReviewAction{
		Token:          "tok-skip-empty",
		WorkspaceID:    "default",
		ActorID:        fromID,
		ChatID:         chatID,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionSkip,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
		NextCursor:     "cursor-end",
	}

	sender := &fakeSender{}
	backlog := &fakeBacklog{page: app.BacklogPage{Items: nil, NextCursor: ""}}
	finder := &fakeFinder{}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, backlog, finder, store, validator, debater)
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, chatID, 555, fromID)); err != nil {
		t.Fatalf("callback: %v", err)
	}

	if len(sender.prompts) != 0 {
		t.Errorf("empty backlog must not send a new prompt, got %+v", sender.prompts)
	}
	if len(sender.edits) != 1 {
		t.Fatalf("expected 1 edit (to empty state), got %d", len(sender.edits))
	}
	if !strings.Contains(sender.edits[0].text, "Nada pendiente") {
		t.Errorf("edit text = %q, want empty-backlog text", sender.edits[0].text)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "Sin más elementos") {
		t.Fatalf("expected 'Sin más elementos' answer, got %+v", sender.answers)
	}
}

// --- Replay / missing / expired tokens ---

// TestReviewCallbackTokenNotFound covers the unknown-token path:
// the user typed or clicked a fabricated payload. The handler
// must answer "ya no está disponible" and never call any service.
func TestReviewCallbackTokenNotFound(t *testing.T) {
	sender := &fakeSender{}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, &fakeFinder{}, store, validator, debater)

	data := TelegramReviewActionPayload("ghost-token")
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}

	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "ya no está disponible") {
		t.Fatalf("expected 'ya no está disponible' answer, got %+v", sender.answers)
	}
	if len(sender.edits) != 0 {
		t.Errorf("unknown token must not edit the message, got %+v", sender.edits)
	}
	if len(validator.calls)+len(debater.markCalls)+len(debater.resolveCalls) != 0 {
		t.Errorf("unknown token must not call any service")
	}
}

// TestReviewCallbackReplay covers single-use Take: a second tap on
// the same token gets the "ya no está disponible" answer and does
// NOT re-run the service. The second tap's token was already
// consumed by the first call.
func TestReviewCallbackReplay(t *testing.T) {
	const chatID int64 = 100
	const fromID int64 = 200

	sender := &fakeSender{}
	backlog := &fakeBacklog{}
	finder := &fakeFinder{obj: &domain.KnowledgeObject{ID: uuid.New(), Status: domain.KnowledgeObjectStatusProposed}}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, backlog, finder, store, validator, debater)

	action := app.TelegramReviewAction{
		Token:          "tok-replay",
		WorkspaceID:    "default",
		ActorID:        fromID,
		ChatID:         chatID,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	// First tap: takes, validates, edits.
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, chatID, 555, fromID)); err != nil {
		t.Fatalf("first tap: %v", err)
	}
	if len(validator.calls) != 1 {
		t.Fatalf("first tap Validate calls = %d, want 1", len(validator.calls))
	}
	// Second tap on the same token: must report already-handled.
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, chatID, 555, fromID)); err != nil {
		t.Fatalf("second tap: %v", err)
	}
	if len(validator.calls) != 1 {
		t.Errorf("second tap must NOT re-call Validate, got %d total calls", len(validator.calls))
	}
	if len(sender.answers) != 2 {
		t.Fatalf("expected 2 answers, got %d", len(sender.answers))
	}
	if !strings.Contains(sender.answers[1].text, "ya no está disponible") {
		t.Errorf("second tap should report 'ya no está disponible', got %q", sender.answers[1].text)
	}
}

// TestReviewCallbackExpiredToken covers the TTL path: a token
// whose ExpiresAt is already in the past is reported as
// "ya no está disponible" (the store's Take filter rejects it).
// This pins the "no resurrection of stale buttons" contract.
func TestReviewCallbackExpiredToken(t *testing.T) {
	sender := &fakeSender{}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, &fakeFinder{}, store, validator, debater)

	expired := app.TelegramReviewAction{
		Token:          "tok-expired",
		WorkspaceID:    "default",
		ActorID:        200,
		ChatID:         100,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
		ExpiresAt:      time.Now().Add(-1 * time.Hour),
	}
	seedReviewAction(t, store, expired)

	data := TelegramReviewActionPayload(expired.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "ya no está disponible") {
		t.Fatalf("expected 'ya no está disponible' answer, got %+v", sender.answers)
	}
	if len(validator.calls) != 0 {
		t.Errorf("expired token must not call Validate")
	}
}

// TestReviewCallbackTakeTransientError verifies that a non
// ErrNotFound Take error (e.g., transient storage failure) is
// reported as "temporal" and does NOT edit the message.
func TestReviewCallbackTakeTransientError(t *testing.T) {
	sender := &fakeSender{}
	store := &erroringReviewStore{err: errors.New("postgres connection lost")}
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, &fakeFinder{}, store, validator, debater)

	data := TelegramReviewActionPayload("tok-anything")
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.edits) != 0 {
		t.Errorf("transient Take error must not edit the message, got %+v", sender.edits)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "temporal") {
		t.Fatalf("expected 'temporal' answer, got %+v", sender.answers)
	}
	if len(validator.calls) != 0 {
		t.Errorf("transient Take error must not call any service")
	}
}

// erroringReviewStore is a TelegramReviewActionStore fake that
// returns a canned error from every operation so tests can drive
// the transient-error branches without touching real storage.
type erroringReviewStore struct {
	err error
}

func (e *erroringReviewStore) Save(_ context.Context, _ app.TelegramReviewAction) error {
	return e.err
}
func (e *erroringReviewStore) Take(_ context.Context, _ string) (app.TelegramReviewAction, error) {
	return app.TelegramReviewAction{}, e.err
}
func (e *erroringReviewStore) SweepExpired(_ context.Context) (int64, error) {
	return 0, e.err
}

// --- Auth mismatch ---

// TestReviewCallbackAuthMismatchActor covers the case where a
// different user (not the one who generated the card) taps a
// button. The handler must answer "no es para vos" and MUST NOT
// call any lifecycle service or edit the message (the original
// user may still be in the chat and should not see a misleading
// "ya no está" edit on their card).
func TestReviewCallbackAuthMismatchActor(t *testing.T) {
	sender := &fakeSender{}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, &fakeFinder{}, store, validator, debater)

	action := app.TelegramReviewAction{
		Token:          "tok-auth-actor",
		WorkspaceID:    "default",
		ActorID:        200, // original
		ChatID:         100,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	// 999 taps it (not 200).
	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 999)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "no es para vos") {
		t.Fatalf("expected 'no es para vos' answer, got %+v", sender.answers)
	}
	if len(sender.edits) != 0 {
		t.Errorf("auth mismatch must not edit the message, got %+v", sender.edits)
	}
	if len(validator.calls)+len(debater.markCalls)+len(debater.resolveCalls) != 0 {
		t.Errorf("auth mismatch must not call any service")
	}
}

// TestReviewCallbackAuthMismatchChat covers the case where a
// different chat context (not the originating chat) taps the
// button. Same answer, same no-mutation contract.
func TestReviewCallbackAuthMismatchChat(t *testing.T) {
	sender := &fakeSender{}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, &fakeFinder{}, store, validator, debater)

	action := app.TelegramReviewAction{
		Token:          "tok-auth-chat",
		WorkspaceID:    "default",
		ActorID:        200,
		ChatID:         100, // original
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	// Chat 999 (not 100) taps it.
	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 999, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "no es para vos") {
		t.Fatalf("expected 'no es para vos' answer, got %+v", sender.answers)
	}
	if len(validator.calls)+len(debater.markCalls)+len(debater.resolveCalls) != 0 {
		t.Errorf("auth mismatch must not call any service")
	}
}

// --- Stale state ---

// TestReviewCallbackStaleStatus covers the stale-button path: the
// object's status changed after the card was rendered. The
// handler refuses to call the service and edits the message to
// point the human at /backlog.
func TestReviewCallbackStaleStatus(t *testing.T) {
	const chatID int64 = 100
	const fromID int64 = 200

	sender := &fakeSender{}
	backlog := &fakeBacklog{}
	// Finder reports a *validated* row, but the action expected
	// *proposed*. The mismatch means the button is stale.
	finder := &fakeFinder{obj: &domain.KnowledgeObject{ID: uuid.New(), Status: domain.KnowledgeObjectStatusValidated}}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, backlog, finder, store, validator, debater)

	action := app.TelegramReviewAction{
		Token:          "tok-stale",
		WorkspaceID:    "default",
		ActorID:        fromID,
		ChatID:         chatID,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, chatID, 555, fromID)); err != nil {
		t.Fatalf("callback: %v", err)
	}

	if len(validator.calls)+len(debater.markCalls)+len(debater.resolveCalls) != 0 {
		t.Errorf("stale callback must not call any service")
	}
	if len(sender.edits) != 1 || !strings.Contains(sender.edits[0].text, "ya cambió") {
		t.Fatalf("expected stale edit, got %+v", sender.edits)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "desactualizado") {
		t.Fatalf("expected 'desactualizado' answer, got %+v", sender.answers)
	}
}

// TestReviewCallbackFinderNotFound covers the case where the
// object was hard-deleted between render and tap. The handler
// edits the message to "no existe" and answers the same.
func TestReviewCallbackFinderNotFound(t *testing.T) {
	sender := &fakeSender{}
	finder := &fakeFinder{err: app.ErrNotFound}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, finder, store, validator, debater)

	action := app.TelegramReviewAction{
		Token:          "tok-gone",
		WorkspaceID:    "default",
		ActorID:        200,
		ChatID:         100,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.edits) != 1 || !strings.Contains(sender.edits[0].text, "no existe") {
		t.Fatalf("expected 'no existe' edit, got %+v", sender.edits)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "no encontrado") {
		t.Fatalf("expected 'no encontrado' answer, got %+v", sender.answers)
	}
	if len(validator.calls) != 0 {
		t.Errorf("finder NotFound must not call Validate")
	}
}

// TestReviewCallbackFinderNil verifies the defensive fallback: a
// nil finder (PR2-style /backlog-only wiring) means the handler
// cannot verify the current state, so it refuses to mutate. The
// human gets a "no se pudo verificar" message and answer.
func TestReviewCallbackFinderNil(t *testing.T) {
	sender := &fakeSender{}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	// Pass nil finder explicitly.
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, nil, store, validator, debater)

	action := app.TelegramReviewAction{
		Token:          "tok-no-finder",
		WorkspaceID:    "default",
		ActorID:        200,
		ChatID:         100,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.edits) != 1 || !strings.Contains(sender.edits[0].text, "verificar") {
		t.Fatalf("expected 'verificar' edit, got %+v", sender.edits)
	}
	if len(validator.calls) != 0 {
		t.Errorf("nil finder must not call any service")
	}
}

// --- Service-layer error mapping ---

// TestReviewCallbackServiceErrInvalidTransition covers the case
// where the app service rejects the transition (e.g., another
// caller already moved the row). The handler edits the message
// to "ya cambió" and answers the same.
func TestReviewCallbackServiceErrInvalidTransition(t *testing.T) {
	const chatID int64 = 100
	const fromID int64 = 200

	sender := &fakeSender{}
	finder := &fakeFinder{obj: &domain.KnowledgeObject{ID: uuid.New(), Status: domain.KnowledgeObjectStatusProposed}}
	store := newInMemReviewStore()
	validator := &fakeValidator{err: app.ErrInvalidTransition}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, finder, store, validator, debater)

	action := app.TelegramReviewAction{
		Token:          "tok-bad-transition",
		WorkspaceID:    "default",
		ActorID:        fromID,
		ChatID:         chatID,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, chatID, 555, fromID)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.edits) != 1 || !strings.Contains(sender.edits[0].text, "ya cambió") {
		t.Fatalf("expected 'ya cambió' edit, got %+v", sender.edits)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "desactualizado") {
		t.Fatalf("expected 'desactualizado' answer, got %+v", sender.answers)
	}
}

// TestReviewCallbackServiceErrNotFound covers the case where the
// service discovers the object is gone after the stale check
// passed (e.g., race between Take and Validate). The handler
// edits the message to "no existe".
func TestReviewCallbackServiceErrNotFound(t *testing.T) {
	sender := &fakeSender{}
	finder := &fakeFinder{obj: &domain.KnowledgeObject{ID: uuid.New(), Status: domain.KnowledgeObjectStatusProposed}}
	store := newInMemReviewStore()
	validator := &fakeValidator{err: app.ErrNotFound}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, finder, store, validator, debater)

	action := app.TelegramReviewAction{
		Token:          "tok-svc-gone",
		WorkspaceID:    "default",
		ActorID:        200,
		ChatID:         100,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.edits) != 1 || !strings.Contains(sender.edits[0].text, "no existe") {
		t.Fatalf("expected 'no existe' edit, got %+v", sender.edits)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "no encontrado") {
		t.Fatalf("expected 'no encontrado' answer, got %+v", sender.answers)
	}
}

// TestReviewCallbackServiceTransient covers a non-typed error
// from the service. The handler answers "temporal" and does NOT
// edit the message so the human can try a different button on
// the same card while the underlying issue resolves.
func TestReviewCallbackServiceTransient(t *testing.T) {
	sender := &fakeSender{}
	finder := &fakeFinder{obj: &domain.KnowledgeObject{ID: uuid.New(), Status: domain.KnowledgeObjectStatusProposed}}
	store := newInMemReviewStore()
	validator := &fakeValidator{err: errors.New("postgres connection lost")}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, finder, store, validator, debater)

	action := app.TelegramReviewAction{
		Token:          "tok-svc-transient",
		WorkspaceID:    "default",
		ActorID:        200,
		ChatID:         100,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.edits) != 0 {
		t.Errorf("transient service error must not edit the message, got %+v", sender.edits)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "temporal") {
		t.Fatalf("expected 'temporal' answer, got %+v", sender.answers)
	}
}

// --- Defensive: missing service wiring ---

// TestReviewCallbackValidatorNil verifies that a nil validator
// answer the user with "no disponible" instead of panicking.
// Mirrors the proposed+validate path.
func TestReviewCallbackValidatorNil(t *testing.T) {
	sender := &fakeSender{}
	finder := &fakeFinder{obj: &domain.KnowledgeObject{ID: uuid.New(), Status: domain.KnowledgeObjectStatusProposed}}
	store := newInMemReviewStore()
	// nil validator — must not panic.
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, finder, store, nil, &fakeDebator{})

	action := app.TelegramReviewAction{
		Token:          "tok-no-validator",
		WorkspaceID:    "default",
		ActorID:        200,
		ChatID:         100,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "no disponible") {
		t.Fatalf("expected 'no disponible' answer, got %+v", sender.answers)
	}
}

// TestReviewCallbackDebaterNil covers the proposed+debate branch
// when no debater is wired. Same defensive contract.
func TestReviewCallbackDebaterNil(t *testing.T) {
	sender := &fakeSender{}
	finder := &fakeFinder{obj: &domain.KnowledgeObject{ID: uuid.New(), Status: domain.KnowledgeObjectStatusProposed}}
	store := newInMemReviewStore()
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, finder, store, &fakeValidator{}, nil)

	action := app.TelegramReviewAction{
		Token:          "tok-no-debater",
		WorkspaceID:    "default",
		ActorID:        200,
		ChatID:         100,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionDebate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "no disponible") {
		t.Fatalf("expected 'no disponible' answer, got %+v", sender.answers)
	}
}

// TestReviewCallbackUnknownSourceAction covers the defensive
// default branch: an (ExpectedStatus, Action) pair the handler
// has not been taught about. It refuses to mutate and edits the
// message to point at /backlog.
func TestReviewCallbackUnknownSourceAction(t *testing.T) {
	sender := &fakeSender{}
	finder := &fakeFinder{obj: &domain.KnowledgeObject{ID: uuid.New(), Status: domain.KnowledgeObjectStatusDeprecated}}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, finder, store, validator, debater)

	// "deprecated" is a valid status but no (deprecated,
	// validate) branch exists in the dispatch; the handler must
	// refuse to mutate. In production the backlog render layer
	// only issues skip for deprecated rows, so this case is a
	// defensive guard against a future status that slips through.
	action := app.TelegramReviewAction{
		Token:          "tok-unknown",
		WorkspaceID:    "default",
		ActorID:        200,
		ChatID:         100,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusDeprecated,
	}
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(validator.calls)+len(debater.markCalls)+len(debater.resolveCalls) != 0 {
		t.Errorf("unknown source/action must not call any service")
	}
	if len(sender.edits) != 1 || !strings.Contains(sender.edits[0].text, "no soportada") {
		t.Fatalf("expected 'no soportada' edit, got %+v", sender.edits)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "no soportada") {
		t.Fatalf("expected 'no soportada' answer, got %+v", sender.answers)
	}
}

// TestReviewCallbackDispatchIsReadAfterWrite covers the thin
// adapter contract: when the stale check passes and the service
// succeeds, the Finder is called exactly once. The dispatch
// must not loop or re-hydrate. This is the "no policy in
// Telegram" guard for the new flow.
func TestReviewCallbackFinderCalledOnce(t *testing.T) {
	sender := &fakeSender{}
	finder := &fakeFinder{obj: &domain.KnowledgeObject{ID: uuid.New(), Status: domain.KnowledgeObjectStatusProposed}}
	store := newInMemReviewStore()
	validator := &fakeValidator{}
	debater := &fakeDebator{}
	handler := newTestHandlerWithReviewActions(sender, &fakeBacklog{}, finder, store, validator, debater)

	action := app.TelegramReviewAction{
		Token:          "tok-once",
		WorkspaceID:    "default",
		ActorID:        200,
		ChatID:         100,
		ObjectID:       uuid.New(),
		Action:         TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
	}
	seedReviewAction(t, store, action)

	data := TelegramReviewActionPayload(action.Token)
	if err := handler.ProcessUpdate(context.Background(), reviewCallbackUpdate(data, 100, 555, 200)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if finder.calls != 1 {
		t.Errorf("finder calls = %d, want 1 (no policy loop)", finder.calls)
	}
	if len(validator.calls) != 1 {
		t.Errorf("validator calls = %d, want 1", len(validator.calls))
	}
}
