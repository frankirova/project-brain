package telegram

import (
	"context"
	"errors"
	"testing"

	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// --- Fakes for testing ---

// fakeSender records sent messages for assertion.
type fakeSender struct {
	messages []sentMessage
}

type sentMessage struct {
	chatID int64
	text   string
}

func (f *fakeSender) SendMessage(_ context.Context, chatID int64, text string) error {
	f.messages = append(f.messages, sentMessage{chatID: chatID, text: text})
	return nil
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
	uow := &fakeIngestionUOW{
		repos: &testRepos{source: sourceRepo},
	}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	return newHandlerWithSender(svc, sender, nil)
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

// --- Tests ---

// Task 3.2: Test /start command response, verify no service call
func TestStartCommand(t *testing.T) {
	sender := &fakeSender{}
	// No existing source — service would be called if /start fell through
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

// Task 3.3: Test /help command response, verify no service call
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

// Task 3.4: Test text ingestion — verify correct request fields and "Saved" response
func TestTextIngestion(t *testing.T) {
	sender := &fakeSender{}
	sourceRepo := &fakeSourceRepo{} // no existing — triggers new ingestion
	handler := newTestHandler(sender, sourceRepo)

	err := handler.ProcessUpdate(context.Background(), testUpdate("hello world"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sender.messages))
	}
	if sender.messages[0].text != "Saved" {
		t.Errorf("expected 'Saved', got %q", sender.messages[0].text)
	}
}

// Task 3.5: Test duplicate — fake service returns Duplicate=true, verify "Duplicate" response
func TestDuplicateMessage(t *testing.T) {
	sender := &fakeSender{}
	existing := &domain.IngestTextResult{
		SourceID:     uuid.New(),
		ObjectID:     uuid.New(),
		AuditEventID: uuid.New(),
		Duplicate:    true,
	}
	sourceRepo := &fakeSourceRepo{existingResult: existing}
	handler := newTestHandler(sender, sourceRepo)

	err := handler.ProcessUpdate(context.Background(), testUpdate("already seen"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sender.messages))
	}
	if sender.messages[0].text != "Duplicate" {
		t.Errorf("expected 'Duplicate', got %q", sender.messages[0].text)
	}
}

// Task 3.6: Test service error — verify bot logs error and replies generic message
func TestServiceError(t *testing.T) {
	sender := &fakeSender{}
	// Create a UoW that returns an error inside the transaction
	uow := &errorUOW{err: errors.New("database connection lost")}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	handler := newHandlerWithSender(svc, sender, nil)

	err := handler.ProcessUpdate(context.Background(), testUpdate("trigger error"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sender.messages))
	}
	if sender.messages[0].text != "Sorry, something went wrong processing your message." {
		t.Errorf("unexpected error response: %q", sender.messages[0].text)
	}
}

// errorUOW always returns the given error inside WithinIngestionTx.
type errorUOW struct {
	err error
}

func (u *errorUOW) WithinIngestionTx(_ context.Context, _ func(context.Context, app.IngestionRepositories) error) error {
	return u.err
}

// TestNilMessage verifies that a nil message doesn't cause a panic.
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
