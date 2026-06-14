package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// --- Fakes for testing ---

// fakeRawInputRepo records calls to app.RawInputRepository for assertion.
type fakeRawInputRepo struct {
	creates          []domain.RawInput
	promoted         []struct{ id, objectID uuid.UUID }
	discarded        []uuid.UUID
	collisionSummary []struct {
		id      uuid.UUID
		summary domain.Metadata
	}
	createErr         error
	setPromotedErr    error
	setDiscardedErr   error
	setCollSummaryErr error
}

func (r *fakeRawInputRepo) Create(_ context.Context, ri domain.RawInput) error {
	r.creates = append(r.creates, ri)
	return r.createErr
}

func (r *fakeRawInputRepo) SetPromoted(_ context.Context, id, objectID uuid.UUID) error {
	r.promoted = append(r.promoted, struct{ id, objectID uuid.UUID }{id, objectID})
	return r.setPromotedErr
}

func (r *fakeRawInputRepo) SetDiscarded(_ context.Context, id uuid.UUID) error {
	r.discarded = append(r.discarded, id)
	return r.setDiscardedErr
}

func (r *fakeRawInputRepo) SetCollisionSummary(_ context.Context, id uuid.UUID, summary domain.Metadata) error {
	r.collisionSummary = append(r.collisionSummary, struct {
		id      uuid.UUID
		summary domain.Metadata
	}{id, summary})
	return r.setCollSummaryErr
}

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
	return newHandlerWithStore(Config{
		Service:  svc,
		Detector: detector,
		Sender:   sender,
	})
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
	handler := newHandlerWithStore(Config{
		Service: svc,
		Sender:  sender,
	})

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

// fakePendingStore records Save/Take calls so we can assert the
// handler uses the store abstraction instead of a hidden map.
type fakePendingStore struct {
	mu      sync.Mutex
	saves   []app.PendingValidation
	takes   []string
	entries map[string]app.PendingValidation
	// optional overrides
	takeErr error
	saveErr error
}

func newFakePendingStore() *fakePendingStore {
	return &fakePendingStore{entries: make(map[string]app.PendingValidation)}
}

func (f *fakePendingStore) Save(_ context.Context, entry app.PendingValidation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves = append(f.saves, entry)
	if f.saveErr != nil {
		return f.saveErr
	}
	f.entries[entry.Token] = entry
	return nil
}

func (f *fakePendingStore) Take(_ context.Context, token string) (app.PendingValidation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takes = append(f.takes, token)
	if f.takeErr != nil {
		return app.PendingValidation{}, f.takeErr
	}
	entry, ok := f.entries[token]
	if !ok {
		return app.PendingValidation{}, app.ErrNotFound
	}
	delete(f.entries, token)
	// Match the real stores' TTL filter so the fake exercises the
	// same expiry path the production stores will.
	if !entry.ExpiresAt.IsZero() && !time.Now().Before(entry.ExpiresAt) {
		return app.PendingValidation{}, app.ErrNotFound
	}
	return entry, nil
}

// The handler must persist the candidate through the store on
// collision and consume it through the store on callback. This is the
// "uses the abstraction, not a hidden map" contract.
func TestHandlerUsesStoreAbstraction(t *testing.T) {
	sender := &fakeSender{}
	collision := sampleCollision()
	det := &fakeDetector{collisions: []app.Collision{collision}}
	store := newFakePendingStore()
	uow := &fakeIngestionUOW{repos: &testRepos{source: &fakeSourceRepo{}}}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	handler := newHandlerWithStore(Config{
		Service:  svc,
		Detector: det,
		Sender:   sender,
		Pending:  store,
	})
	handler.newToken = func() string { return "tok123" }

	if err := handler.ProcessUpdate(context.Background(), testUpdate("propongo Python")); err != nil {
		t.Fatalf("prompt step: %v", err)
	}
	if len(store.saves) != 1 {
		t.Fatalf("store saves = %d, want 1 (handler must persist on collision)", len(store.saves))
	}
	if store.saves[0].Token != "tok123" {
		t.Errorf("saved token = %q, want tok123", store.saves[0].Token)
	}
	if store.saves[0].Collision.Object.ID != collision.Object.ID {
		t.Errorf("saved collision = %+v, want detector output %+v", store.saves[0].Collision, collision)
	}

	if err := handler.ProcessUpdate(context.Background(), callbackUpdate("keep:tok123", 100, 555)); err != nil {
		t.Fatalf("keep step: %v", err)
	}
	if len(store.takes) != 1 || store.takes[0] != "tok123" {
		t.Errorf("store takes = %+v, want [tok123]", store.takes)
	}
	// The entry must be gone after Take so a retry would 404.
	if _, ok := store.entries["tok123"]; ok {
		t.Errorf("entry still present after Take: load-and-delete contract broken")
	}
}

// A storage save failure must not block ingestion — degrade to a
// direct save and never put a button on the wire.
func TestHandlerDegradesOnStoreSaveError(t *testing.T) {
	sender := &fakeSender{}
	det := &fakeDetector{collisions: []app.Collision{sampleCollision()}}
	store := newFakePendingStore()
	store.saveErr = errors.New("postgres unreachable")
	uow := &fakeIngestionUOW{repos: &testRepos{source: &fakeSourceRepo{}}}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	handler := newHandlerWithStore(Config{
		Service:  svc,
		Detector: det,
		Sender:   sender,
		Pending:  store,
	})

	if err := handler.ProcessUpdate(context.Background(), testUpdate("anything")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.prompts) != 0 {
		t.Fatalf("storage failure must not prompt, got %d prompts", len(sender.prompts))
	}
	if len(sender.messages) != 1 || sender.messages[0].text != "Saved" {
		t.Fatalf("expected degraded 'Saved', got %+v", sender.messages)
	}
	if len(store.saves) != 1 {
		t.Fatalf("store should have been called once, got %d", len(store.saves))
	}
}

// A storage Take error that is NOT ErrNotFound must not edit the
// message (the row may still be in flight) and must answer the
// callback with a transient error so the human can retry.
func TestHandlerHandlesStoreTakeError(t *testing.T) {
	sender := &fakeSender{}
	det := &fakeDetector{collisions: []app.Collision{sampleCollision()}}
	store := newFakePendingStore()
	// pre-seed so Save succeeds and Take gets a chance to fail.
	if err := store.Save(context.Background(), app.PendingValidation{Token: "tok123", ChatID: 100, Collision: sampleCollision()}); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	store.takeErr = errors.New("postgres connection lost")
	uow := &fakeIngestionUOW{repos: &testRepos{source: &fakeSourceRepo{}}}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	handler := newHandlerWithStore(Config{
		Service:  svc,
		Detector: det,
		Sender:   sender,
		Pending:  store,
	})

	if err := handler.ProcessUpdate(context.Background(), callbackUpdate("keep:tok123", 100, 555)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.edits) != 0 {
		t.Fatalf("transient store error must not edit the message, got %+v", sender.edits)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "temporal") {
		t.Fatalf("expected transient-error answer, got %+v", sender.answers)
	}
}

// An entry whose ExpiresAt is already in the past must be reported as
// "no longer available", the same way a never-saved token is. The
// store's TTL filter makes the two cases indistinguishable to the
// handler; this test pins that contract so a future regression in
// either layer (handler or store) cannot quietly resurrect a stale
// prompt.
func TestHandlerExpiredEntryReportsUnavailable(t *testing.T) {
	sender := &fakeSender{}
	det := &fakeDetector{collisions: []app.Collision{sampleCollision()}}
	store := newFakePendingStore()
	// Seed an entry with a TTL already in the past; the handler will
	// never see this token via its own Save path.
	expired := app.PendingValidation{
		Token:     "tok-stale",
		ChatID:    100,
		Collision: sampleCollision(),
		ExpiresAt: time.Now().Add(-1 * time.Hour),
	}
	if err := store.Save(context.Background(), expired); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	uow := &fakeIngestionUOW{repos: &testRepos{source: &fakeSourceRepo{}}}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	handler := newHandlerWithStore(Config{
		Service:  svc,
		Detector: det,
		Sender:   sender,
		Pending:  store,
	})

	if err := handler.ProcessUpdate(context.Background(), callbackUpdate("keep:tok-stale", 100, 555)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if len(sender.edits) != 0 {
		t.Fatalf("expired entry must not edit the prompt, got %+v", sender.edits)
	}
	if len(sender.answers) != 1 || !strings.Contains(sender.answers[0].text, "disponible") {
		t.Fatalf("expected 'no disponible' answer, got %+v", sender.answers)
	}
}

// newTestHandlerWithRawInputs builds a handler with a fake rawInputRepo
// and optional collision detector. The pending store defaults to
// in-memory (nil → installed by Config.applyDefaults).
func newTestHandlerWithRawInputs(sender *fakeSender, sourceRepo *fakeSourceRepo, detector collisionChecker, rawInputs app.RawInputRepository) *Handler {
	uow := &fakeIngestionUOW{repos: &testRepos{source: sourceRepo}}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	return newHandlerWithStore(Config{
		Service:   svc,
		Detector:  detector,
		RawInputs: rawInputs,
		Sender:    sender,
	})
}

// TestHandleMessageCreatesRawInput verifies that handleMessage calls
// Create on the raw_input repo with the expected fields (REQ-05).
func TestHandleMessageCreatesRawInput(t *testing.T) {
	sender := &fakeSender{}
	rawInputs := &fakeRawInputRepo{}
	handler := newTestHandlerWithRawInputs(sender, &fakeSourceRepo{}, nil, rawInputs)

	update := testUpdate("usamos Redis")
	if err := handler.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rawInputs.creates) != 1 {
		t.Fatalf("Create called %d times, want 1", len(rawInputs.creates))
	}
	got := rawInputs.creates[0]
	if got.Channel != domain.RawInputChannelTelegram {
		t.Errorf("Channel = %q, want %q", got.Channel, domain.RawInputChannelTelegram)
	}
	if got.Content != "usamos Redis" {
		t.Errorf("Content = %q, want %q", got.Content, "usamos Redis")
	}
	if got.ActorID != strconv.FormatInt(200, 10) {
		t.Errorf("ActorID = %q, want %q", got.ActorID, strconv.FormatInt(200, 10))
	}
	if got.WorkspaceID != "default" {
		t.Errorf("WorkspaceID = %q, want %q", got.WorkspaceID, "default")
	}
	if got.Status != domain.RawInputStatusPending {
		t.Errorf("Status = %q, want %q", got.Status, domain.RawInputStatusPending)
	}
	if got.ExternalRef["chat_id"] != int64(100) {
		t.Errorf("ExternalRef[chat_id] = %v, want %d", got.ExternalRef["chat_id"], 100)
	}
}

// TestHandleMessageNoCollisionCallsSetPromoted verifies that after a
// successful direct ingest with no collision, SetPromoted is called
// with the raw_input ID (REQ-07).
func TestHandleMessageNoCollisionCallsSetPromoted(t *testing.T) {
	sender := &fakeSender{}
	rawInputs := &fakeRawInputRepo{}
	det := &fakeDetector{collisions: nil}
	handler := newTestHandlerWithRawInputs(sender, &fakeSourceRepo{}, det, rawInputs)

	if err := handler.ProcessUpdate(context.Background(), testUpdate("algo nuevo")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rawInputs.promoted) != 1 {
		t.Fatalf("SetPromoted called %d times, want 1", len(rawInputs.promoted))
	}
	if rawInputs.promoted[0].id != rawInputs.creates[0].ID {
		t.Errorf("SetPromoted id = %s, want %s", rawInputs.promoted[0].id, rawInputs.creates[0].ID)
	}
}

// TestHandleMessageRawInputCreateErrorDegrades verifies that a Create
// error from the raw_input repo does not abort the ingest flow (S-05).
func TestHandleMessageRawInputCreateErrorDegrades(t *testing.T) {
	sender := &fakeSender{}
	rawInputs := &fakeRawInputRepo{createErr: errors.New("db unreachable")}
	handler := newTestHandlerWithRawInputs(sender, &fakeSourceRepo{}, nil, rawInputs)

	if err := handler.ProcessUpdate(context.Background(), testUpdate("trigger error path")); err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	// Ingest should still run — user sees "Saved"
	if len(sender.messages) != 1 || sender.messages[0].text != "Saved" {
		t.Fatalf("expected 'Saved' after Create error, got %+v", sender.messages)
	}
	// SetPromoted must NOT be called when Create failed (rawInputID is zero)
	if len(rawInputs.promoted) != 0 {
		t.Errorf("SetPromoted called %d times after Create error, want 0", len(rawInputs.promoted))
	}
}

// TestCallbackKeepCallsSetPromoted verifies that "keep" callback calls
// SetPromoted with the pv.RawInputID (REQ-08).
func TestCallbackKeepCallsSetPromoted(t *testing.T) {
	sender := &fakeSender{}
	rawInputs := &fakeRawInputRepo{}
	det := &fakeDetector{collisions: []app.Collision{sampleCollision()}}
	store := newFakePendingStore()
	uow := &fakeIngestionUOW{repos: &testRepos{source: &fakeSourceRepo{}}}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	handler := newHandlerWithStore(Config{
		Service:   svc,
		Detector:  det,
		RawInputs: rawInputs,
		Sender:    sender,
		Pending:   store,
	})
	handler.newToken = func() string { return "tok-keep" }

	// Trigger the collision prompt (creates raw_input and saves PendingValidation).
	if err := handler.ProcessUpdate(context.Background(), testUpdate("propongo algo")); err != nil {
		t.Fatalf("prompt step: %v", err)
	}
	if len(rawInputs.creates) != 1 {
		t.Fatalf("Create not called during prompt step")
	}
	rawID := rawInputs.creates[0].ID

	// User taps "keep".
	if err := handler.ProcessUpdate(context.Background(), callbackUpdate("keep:tok-keep", 100, 555)); err != nil {
		t.Fatalf("keep step: %v", err)
	}

	if len(rawInputs.promoted) != 1 {
		t.Fatalf("SetPromoted called %d times on keep, want 1", len(rawInputs.promoted))
	}
	if rawInputs.promoted[0].id != rawID {
		t.Errorf("SetPromoted id = %s, want %s", rawInputs.promoted[0].id, rawID)
	}
}

// TestCallbackDiscardCallsSetDiscarded verifies that "discard" callback
// calls SetDiscarded with the pv.RawInputID (REQ-09).
func TestCallbackDiscardCallsSetDiscarded(t *testing.T) {
	sender := &fakeSender{}
	rawInputs := &fakeRawInputRepo{}
	det := &fakeDetector{collisions: []app.Collision{sampleCollision()}}
	store := newFakePendingStore()
	uow := &fakeIngestionUOW{repos: &testRepos{source: &fakeSourceRepo{}}}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	handler := newHandlerWithStore(Config{
		Service:   svc,
		Detector:  det,
		RawInputs: rawInputs,
		Sender:    sender,
		Pending:   store,
	})
	handler.newToken = func() string { return "tok-discard" }

	_ = handler.ProcessUpdate(context.Background(), testUpdate("propongo algo"))
	if len(rawInputs.creates) != 1 {
		t.Fatalf("Create not called during prompt step")
	}
	rawID := rawInputs.creates[0].ID

	if err := handler.ProcessUpdate(context.Background(), callbackUpdate("discard:tok-discard", 100, 555)); err != nil {
		t.Fatalf("discard step: %v", err)
	}

	if len(rawInputs.discarded) != 1 {
		t.Fatalf("SetDiscarded called %d times on discard, want 1", len(rawInputs.discarded))
	}
	if rawInputs.discarded[0] != rawID {
		t.Errorf("SetDiscarded id = %s, want %s", rawInputs.discarded[0], rawID)
	}
}

// TestCallbackDiscardZeroRawInputIDSkipsSetDiscarded verifies that
// when PendingValidation.RawInputID is the zero UUID, SetDiscarded is
// NOT called (S-08: forward-compat for pre-migration records).
func TestCallbackDiscardZeroRawInputIDSkipsSetDiscarded(t *testing.T) {
	sender := &fakeSender{}
	rawInputs := &fakeRawInputRepo{}
	det := &fakeDetector{}
	store := newFakePendingStore()
	// Seed a pending validation with zero RawInputID (simulates pre-migration row).
	if err := store.Save(context.Background(), app.PendingValidation{
		Token:      "tok-legacy",
		ChatID:     100,
		Request:    domain.IngestTextRequest{WorkspaceID: "default", Content: "old"},
		Collision:  sampleCollision(),
		RawInputID: uuid.Nil, // zero = pre-migration
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	uow := &fakeIngestionUOW{repos: &testRepos{source: &fakeSourceRepo{}}}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	handler := newHandlerWithStore(Config{
		Service:   svc,
		Detector:  det,
		RawInputs: rawInputs,
		Sender:    sender,
		Pending:   store,
	})

	if err := handler.ProcessUpdate(context.Background(), callbackUpdate("discard:tok-legacy", 100, 555)); err != nil {
		t.Fatalf("discard step: %v", err)
	}

	if len(rawInputs.discarded) != 0 {
		t.Errorf("SetDiscarded called %d times with zero RawInputID, want 0", len(rawInputs.discarded))
	}
}

// TestNilRawInputsRepoPreservesExistingBehavior verifies that passing
// nil as the rawInputs repo leaves all existing flows intact.
func TestNilRawInputsRepoPreservesExistingBehavior(t *testing.T) {
	sender := &fakeSender{}
	// nil rawInputs — no raw_input staging
	handler := newTestHandlerWithRawInputs(sender, &fakeSourceRepo{}, nil, nil)

	if err := handler.ProcessUpdate(context.Background(), testUpdate("hello")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.messages) != 1 || sender.messages[0].text != "Saved" {
		t.Fatalf("expected 'Saved' with nil rawInputs, got %+v", sender.messages)
	}
}
