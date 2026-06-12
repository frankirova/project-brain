package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"

	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// --- Fakes for testing ---

// fakeSender records every Telegram operation for assertion.
type fakeSender struct {
	messages []sentMessage
	prompts  []sentPrompt
	edits    []editedMessage
	answers  []answeredCallback
}

type sentMessage struct {
	chatID int64
	text   string
}

type sentPrompt struct {
	chatID int64
	text   string
	rows   [][]InlineButton
}

type editedMessage struct {
	chatID    int64
	messageID int
	text      string
}

type answeredCallback struct {
	callbackID string
	text       string
}

func (f *fakeSender) SendMessage(_ context.Context, chatID int64, text string) error {
	f.messages = append(f.messages, sentMessage{chatID: chatID, text: text})
	return nil
}

func (f *fakeSender) SendMessageWithButtons(_ context.Context, chatID int64, text string, rows [][]InlineButton) error {
	f.prompts = append(f.prompts, sentPrompt{chatID: chatID, text: text, rows: rows})
	return nil
}

func (f *fakeSender) EditMessageText(_ context.Context, chatID int64, messageID int, text string) error {
	f.edits = append(f.edits, editedMessage{chatID: chatID, messageID: messageID, text: text})
	return nil
}

func (f *fakeSender) AnswerCallback(_ context.Context, callbackID, text string) error {
	f.answers = append(f.answers, answeredCallback{callbackID: callbackID, text: text})
	return nil
}

// fakeDetector returns canned collisions for the validation flow.
type fakeDetector struct {
	collisions []app.Collision
	err        error
	gotWS      string
	gotText    string
}

func (d *fakeDetector) Detect(_ context.Context, ws, text string, _ *uuid.UUID) ([]app.Collision, error) {
	d.gotWS, d.gotText = ws, text
	return d.collisions, d.err
}

// fakeIngestionUOW provides controlled behavior for IngestTextService.
type fakeIngestionUOW struct {
	repos app.IngestionRepositories
}

func (u *fakeIngestionUOW) WithinIngestionTx(ctx context.Context, fn func(context.Context, app.IngestionRepositories) error) error {
	return fn(ctx, u.repos)
}

// --- Fake Repositories ---

type fakeSourceRepo struct {
	existingResult *domain.IngestTextResult // nil = not found, non-nil = found
}

func (r *fakeSourceRepo) FindIngestionResultByIdentityKey(_ context.Context, _ string, _ string) (domain.IngestTextResult, error) {
	if r.existingResult != nil {
		return *r.existingResult, nil
	}
	return domain.IngestTextResult{}, app.ErrNotFound
}

func (r *fakeSourceRepo) Create(_ context.Context, _ domain.Source) error { return nil }

type fakeObjectRepo struct{}

func (r *fakeObjectRepo) Create(_ context.Context, _ domain.KnowledgeObject) error { return nil }
func (r *fakeObjectRepo) UpdateStatus(_ context.Context, _ string, _ uuid.UUID, _ string) error {
	return nil
}

type fakeLinkRepo struct{}

func (r *fakeLinkRepo) Create(_ context.Context, _ domain.ObjectSource) error { return nil }

type fakeAuditRepo struct{}

func (r *fakeAuditRepo) Create(_ context.Context, _ domain.AuditEvent) error { return nil }

// --- Test helpers ---

func newTestHandler(sender *fakeSender, sourceRepo *fakeSourceRepo) *Handler {
	return newTestHandlerWithDetector(sender, sourceRepo, nil)
}

func newTestHandlerWithDetector(sender *fakeSender, sourceRepo *fakeSourceRepo, detector collisionChecker) *Handler {
	uow := &fakeIngestionUOW{
		repos: &testRepos{source: sourceRepo},
	}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	return newHandlerWithSender(svc, detector, sender, nil)
}

type testRepos struct {
	source *fakeSourceRepo
}

func (r *testRepos) Sources() app.SourceRepository                   { return r.source }
func (r *testRepos) KnowledgeObjects() app.KnowledgeObjectRepository { return &fakeObjectRepo{} }
func (r *testRepos) ObjectSources() app.ObjectSourceRepository       { return &fakeLinkRepo{} }
func (r *testRepos) AuditEvents() app.AuditEventRepository           { return &fakeAuditRepo{} }

func testUpdate(text string) *models.Update {
	return &models.Update{
		Message: &models.Message{
			ID:   123,
			Text: text,
			Chat: models.Chat{ID: 100},
			From: &models.User{ID: 200},
		},
	}
}

func callbackUpdate(data string, chatID int64, messageID int) *models.Update {
	return &models.Update{
		CallbackQuery: &models.CallbackQuery{
			ID:   "cb-1",
			Data: data,
			Message: models.MaybeInaccessibleMessage{
				Message: &models.Message{ID: messageID, Chat: models.Chat{ID: chatID}},
			},
		},
	}
}

func sampleCollision() app.Collision {
	return app.Collision{
		Object:     domain.KnowledgeObject{ID: uuid.New(), Content: "El equipo decidió adoptar Go como backend"},
		Similarity: 0.80,
		Verdict:    app.CollisionStrongOverlap,
	}
}

// --- Tests: existing command/ingest behaviour ---

func TestStartCommand(t *testing.T) {
	sender := &fakeSender{}
	handler := newTestHandler(sender, &fakeSourceRepo{})

	err := handler.ProcessUpdate(context.Background(), testUpdate("/start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sender.messages))
	}
	if sender.messages[0].chatID != 100 {
		t.Errorf("expected chat_id 100, got %d", sender.messages[0].chatID)
	}
	if sender.messages[0].text != "Welcome! Send me any text and I'll save it to Knowledge Core." {
		t.Errorf("unexpected welcome text: %q", sender.messages[0].text)
	}
}

func TestHelpCommand(t *testing.T) {
	sender := &fakeSender{}
	handler := newTestHandler(sender, &fakeSourceRepo{})

	err := handler.ProcessUpdate(context.Background(), testUpdate("/help"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sender.messages))
	}
	if sender.messages[0].text != "Send any text message and I'll ingest it into Knowledge Core. Use /start for a welcome message." {
		t.Errorf("unexpected help text: %q", sender.messages[0].text)
	}
}

// With no detector, a plain message ingests directly (legacy behaviour).
func TestTextIngestionNoDetector(t *testing.T) {
	sender := &fakeSender{}
	handler := newTestHandler(sender, &fakeSourceRepo{})

	err := handler.ProcessUpdate(context.Background(), testUpdate("hello world"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.messages) != 1 || sender.messages[0].text != "Saved" {
		t.Fatalf("expected 'Saved', got %+v", sender.messages)
	}
}

func TestDuplicateMessage(t *testing.T) {
	sender := &fakeSender{}
	existing := &domain.IngestTextResult{
		SourceID:     uuid.New(),
		ObjectID:     uuid.New(),
		AuditEventID: uuid.New(),
		Duplicate:    true,
	}
	handler := newTestHandler(sender, &fakeSourceRepo{existingResult: existing})

	err := handler.ProcessUpdate(context.Background(), testUpdate("already seen"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.messages) != 1 || sender.messages[0].text != "Duplicate" {
		t.Fatalf("expected 'Duplicate', got %+v", sender.messages)
	}
}

func TestServiceError(t *testing.T) {
	sender := &fakeSender{}
	uow := &errorUOW{err: errors.New("database connection lost")}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	handler := newHandlerWithSender(svc, nil, sender, nil)

	err := handler.ProcessUpdate(context.Background(), testUpdate("trigger error"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.messages) != 1 || sender.messages[0].text != "Sorry, something went wrong processing your message." {
		t.Fatalf("unexpected error response: %+v", sender.messages)
	}
}

type errorUOW struct {
	err error
}

func (u *errorUOW) WithinIngestionTx(_ context.Context, _ func(context.Context, app.IngestionRepositories) error) error {
	return u.err
}

func TestNilMessage(t *testing.T) {
	sender := &fakeSender{}
	handler := newTestHandler(sender, &fakeSourceRepo{})

	err := handler.ProcessUpdate(context.Background(), &models.Update{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.messages) != 0 {
		t.Errorf("expected no messages for nil update, got %d", len(sender.messages))
	}
}

// --- Tests: collision validation flow ---

// No collision => direct ingest, no buttons.
func TestNoCollisionIngestsDirectly(t *testing.T) {
	sender := &fakeSender{}
	det := &fakeDetector{collisions: nil}
	handler := newTestHandlerWithDetector(sender, &fakeSourceRepo{}, det)

	if err := handler.ProcessUpdate(context.Background(), testUpdate("usamos Redis")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if det.gotText != "usamos Redis" {
		t.Errorf("detector got text %q", det.gotText)
	}
	if len(sender.prompts) != 0 {
		t.Fatalf("expected no validation prompt, got %d", len(sender.prompts))
	}
	if len(sender.messages) != 1 || sender.messages[0].text != "Saved" {
		t.Fatalf("expected direct 'Saved', got %+v", sender.messages)
	}
}

// Collision => prompt with two buttons, NOTHING ingested yet.
func TestCollisionPromptsForValidation(t *testing.T) {
	sender := &fakeSender{}
	det := &fakeDetector{collisions: []app.Collision{sampleCollision()}}
	handler := newTestHandlerWithDetector(sender, &fakeSourceRepo{}, det)
	handler.newToken = func() string { return "tok123" }

	if err := handler.ProcessUpdate(context.Background(), testUpdate("propongo Python en vez de Go")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.messages) != 0 {
		t.Fatalf("nothing should be ingested yet, got messages %+v", sender.messages)
	}
	if len(sender.prompts) != 1 {
		t.Fatalf("expected 1 validation prompt, got %d", len(sender.prompts))
	}
	p := sender.prompts[0]
	if !strings.Contains(p.text, "strong_overlap") {
		t.Errorf("prompt missing verdict: %q", p.text)
	}
	if len(p.rows) != 1 || len(p.rows[0]) != 2 {
		t.Fatalf("expected one row of two buttons, got %+v", p.rows)
	}
	if p.rows[0][0].Data != "keep:tok123" || p.rows[0][1].Data != "discard:tok123" {
		t.Errorf("unexpected button data: %+v", p.rows[0])
	}
}

// Pressing "Guardar igual" ingests the pending input and retires buttons.
func TestCallbackKeepIngests(t *testing.T) {
	sender := &fakeSender{}
	det := &fakeDetector{collisions: []app.Collision{sampleCollision()}}
	handler := newTestHandlerWithDetector(sender, &fakeSourceRepo{}, det)
	handler.newToken = func() string { return "tok123" }

	// First the message triggers the prompt.
	if err := handler.ProcessUpdate(context.Background(), testUpdate("propongo Python")); err != nil {
		t.Fatalf("prompt step: %v", err)
	}
	// Then the user taps Guardar igual.
	if err := handler.ProcessUpdate(context.Background(), callbackUpdate("keep:tok123", 100, 555)); err != nil {
		t.Fatalf("keep step: %v", err)
	}

	if len(sender.edits) != 1 || !strings.Contains(sender.edits[0].text, "Guardado") {
		t.Fatalf("expected an edit confirming save, got %+v", sender.edits)
	}
	if sender.edits[0].messageID != 555 || sender.edits[0].chatID != 100 {
		t.Errorf("edit targeted wrong message: %+v", sender.edits[0])
	}
	if len(sender.answers) != 1 {
		t.Fatalf("expected one callback answer, got %d", len(sender.answers))
	}
}

// Pressing "Descartar" ingests NOTHING and retires buttons.
func TestCallbackDiscardSkipsIngest(t *testing.T) {
	sender := &fakeSender{}
	det := &fakeDetector{collisions: []app.Collision{sampleCollision()}}
	handler := newTestHandlerWithDetector(sender, &fakeSourceRepo{}, det)
	handler.newToken = func() string { return "tok123" }

	_ = handler.ProcessUpdate(context.Background(), testUpdate("propongo Python"))
	if err := handler.ProcessUpdate(context.Background(), callbackUpdate("discard:tok123", 100, 555)); err != nil {
		t.Fatalf("discard step: %v", err)
	}

	if len(sender.edits) != 1 || !strings.Contains(sender.edits[0].text, "Descartado") {
		t.Fatalf("expected an edit confirming discard, got %+v", sender.edits)
	}
	if len(sender.answers) != 1 || sender.answers[0].text != "Descartado" {
		t.Fatalf("expected 'Descartado' answer, got %+v", sender.answers)
	}
}

// A token with no pending entry (restart / double-tap) is reported gracefully.
func TestCallbackExpiredToken(t *testing.T) {
	sender := &fakeSender{}
	handler := newTestHandlerWithDetector(sender, &fakeSourceRepo{}, &fakeDetector{})

	if err := handler.ProcessUpdate(context.Background(), callbackUpdate("keep:ghost", 100, 555)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.edits) != 0 {
		t.Fatalf("expired token must not edit anything, got %+v", sender.edits)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "disponible") {
		t.Fatalf("expected 'no disponible' answer, got %+v", sender.answers)
	}
}

// The same token cannot be acted on twice (load-and-delete).
func TestCallbackTokenSingleUse(t *testing.T) {
	sender := &fakeSender{}
	det := &fakeDetector{collisions: []app.Collision{sampleCollision()}}
	handler := newTestHandlerWithDetector(sender, &fakeSourceRepo{}, det)
	handler.newToken = func() string { return "tok123" }

	_ = handler.ProcessUpdate(context.Background(), testUpdate("propongo Python"))
	_ = handler.ProcessUpdate(context.Background(), callbackUpdate("keep:tok123", 100, 555))
	// Second tap on the same button.
	_ = handler.ProcessUpdate(context.Background(), callbackUpdate("keep:tok123", 100, 555))

	if len(sender.answers) != 2 {
		t.Fatalf("expected two answers, got %d", len(sender.answers))
	}
	if !strings.Contains(sender.answers[1].text, "disponible") {
		t.Errorf("second tap should report unavailable, got %q", sender.answers[1].text)
	}
}

// A detector error must never block ingestion — degrade to direct save.
func TestDetectorErrorDegradesToIngest(t *testing.T) {
	sender := &fakeSender{}
	det := &fakeDetector{err: errors.New("gemini quota")}
	handler := newTestHandlerWithDetector(sender, &fakeSourceRepo{}, det)

	if err := handler.ProcessUpdate(context.Background(), testUpdate("anything")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.prompts) != 0 {
		t.Fatalf("error path must not prompt, got %d", len(sender.prompts))
	}
	if len(sender.messages) != 1 || sender.messages[0].text != "Saved" {
		t.Fatalf("expected degraded 'Saved', got %+v", sender.messages)
	}
}
