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

// --- Tests: /backlog review card flow (change 15 PR2) ---

// fakeBacklog is the backlogLister fake. It records the last filter
// the handler sent so tests can pin the workspace, page size, and
// cursor the handler passes; the canned page lets each test drive
// the rendering path with the exact BacklogItem it wants.
type fakeBacklog struct {
	page       app.BacklogPage
	err        error
	lastFilter app.BacklogFilter
}

func (b *fakeBacklog) ListHumanBacklog(_ context.Context, f app.BacklogFilter) (app.BacklogPage, error) {
	b.lastFilter = f
	if b.err != nil {
		return app.BacklogPage{}, b.err
	}
	return b.page, nil
}

// fakeFinder is the KnowledgeObjectFinder fake. Calls records
// hydration attempts so tests can assert the handler used the
// dependency exactly once per card.
type fakeFinder struct {
	obj   *domain.KnowledgeObject
	err   error
	calls int
}

func (f *fakeFinder) FindByID(_ context.Context, _ string, _ uuid.UUID) (*domain.KnowledgeObject, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.obj, nil
}

// inMemReviewStore is a focused in-memory TelegramReviewActionStore
// for handler tests. It mirrors the production inmem behavior (Save
// is an upsert keyed by token; Take is single-use with TTL). The
// fields are exposed so tests can inspect what the handler wrote.
type inMemReviewStore struct {
	mu      sync.Mutex
	data    map[string]app.TelegramReviewAction
	saveErr error
}

func newInMemReviewStore() *inMemReviewStore {
	return &inMemReviewStore{data: make(map[string]app.TelegramReviewAction)}
}

func (s *inMemReviewStore) Save(_ context.Context, a app.TelegramReviewAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.data[a.Token] = a
	return nil
}

func (s *inMemReviewStore) Take(_ context.Context, token string) (app.TelegramReviewAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data[token]
	if !ok {
		return app.TelegramReviewAction{}, app.ErrNotFound
	}
	delete(s.data, token)
	if !a.ExpiresAt.IsZero() && !time.Now().Before(a.ExpiresAt) {
		return app.TelegramReviewAction{}, app.ErrNotFound
	}
	return a, nil
}

func (s *inMemReviewStore) SweepExpired(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int64
	now := time.Now()
	for token, a := range s.data {
		if !a.ExpiresAt.IsZero() && !now.Before(a.ExpiresAt) {
			delete(s.data, token)
			removed++
		}
	}
	return removed, nil
}

// newTestHandlerWithBacklog is the test seam for the backlog flow.
// detector and rawInputs default to nil; the backlog query, finder,
// and review store are passed in. Pass nil reviewStore to exercise
// the in-memory fallback installed by the composition seam. The
// validator and debater slots are left nil: the PR2 tests do not
// exercise the rv: dispatch path, so a nil validator/debater is
// the natural state for /backlog-only test cases. PR3 tests use
// newTestHandlerWithBacklogAndReview (or call newHandlerWithBacklog
// directly) when they need to drive the dispatch.
func newTestHandlerWithBacklog(sender *fakeSender, backlog backlogLister, finder app.KnowledgeObjectFinder, review reviewActionStore) *Handler {
	uow := &fakeIngestionUOW{repos: &testRepos{source: &fakeSourceRepo{}}}
	svc := app.NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, nil)
	return newHandlerWithBacklog(svc, nil, nil, sender, nil, nil, backlog, finder, review, nil, nil)
}

// backlogUpdate returns a /backlog command update. chatID and fromID
// flow into the saved TelegramReviewAction.ActorID/ChatID, so
// handler tests can pin the actor binding the same way the PR3
// authorization check will.
func backlogUpdate(chatID, fromID int64) *models.Update {
	return &models.Update{
		Message: &models.Message{
			ID:   1,
			Text: "/backlog",
			Chat: models.Chat{ID: chatID},
			From: &models.User{ID: fromID},
		},
	}
}

// sequentialToken returns a newToken func that emits "tok-1",
// "tok-2", ... so each button on a card has a distinct, predictable
// token. Tests assert the token count and the saved action count.
func sequentialToken() func() string {
	var n int
	return func() string {
		n++
		return "tok-" + strconv.Itoa(n)
	}
}

// With no backlog wired the command answers with a friendly
// "not configured" message and never issues a button or saves a
// review action.
func TestBacklogCommandBacklogNilFriendly(t *testing.T) {
	sender := &fakeSender{}
	store := newInMemReviewStore()
	handler := newTestHandlerWithBacklog(sender, nil, nil, store)

	if err := handler.ProcessUpdate(context.Background(), backlogUpdate(100, 200)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 text message, got %d", len(sender.messages))
	}
	if len(sender.prompts) != 0 {
		t.Errorf("nil backlog must not produce a buttoned prompt, got %+v", sender.prompts)
	}
	if len(store.data) != 0 {
		t.Errorf("nil backlog must not save review actions, got %d", len(store.data))
	}
}

// A backlog error degrades to a friendly transient-error message;
// no buttons, no saved actions.
func TestBacklogCommandListErrorDegrades(t *testing.T) {
	sender := &fakeSender{}
	backlog := &fakeBacklog{err: errors.New("db unreachable")}
	store := newInMemReviewStore()
	handler := newTestHandlerWithBacklog(sender, backlog, nil, store)

	if err := handler.ProcessUpdate(context.Background(), backlogUpdate(100, 200)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 error message, got %d", len(sender.messages))
	}
	if len(sender.prompts) != 0 {
		t.Errorf("error path must not produce buttons, got %+v", sender.prompts)
	}
	if len(store.data) != 0 {
		t.Errorf("error path must not save review actions, got %d", len(store.data))
	}
}

// An empty backlog page sends a "nothing pending" message and no
// buttons. The backlog query was called with the MVP workspace and
// page size 1 so the handler never silently widens the read.
func TestBacklogCommandEmptyPage(t *testing.T) {
	sender := &fakeSender{}
	backlog := &fakeBacklog{page: app.BacklogPage{Items: nil}}
	store := newInMemReviewStore()
	handler := newTestHandlerWithBacklog(sender, backlog, nil, store)

	if err := handler.ProcessUpdate(context.Background(), backlogUpdate(100, 200)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 text message, got %d", len(sender.messages))
	}
	if len(sender.prompts) != 0 {
		t.Errorf("empty backlog must not produce a buttoned prompt, got %+v", sender.prompts)
	}
	if backlog.lastFilter.WorkspaceID != "default" {
		t.Errorf("WorkspaceID = %q, want default", backlog.lastFilter.WorkspaceID)
	}
	if backlog.lastFilter.PageSize != 1 {
		t.Errorf("PageSize = %d, want 1", backlog.lastFilter.PageSize)
	}
}

// A proposed backlog item renders the full 4-button keyboard and
// persists one review-action row per button, each carrying the
// actor/chat/workspace/expected-status/TTL the PR3 callback
// handler will need.
func TestBacklogCommandProposedItem(t *testing.T) {
	sender := &fakeSender{}
	itemID := uuid.New()
	item := app.BacklogItem{
		ID:          itemID,
		WorkspaceID: "default",
		Status:      domain.KnowledgeObjectStatusProposed,
		Title:       "Adopt Go",
		Summary:     "Backend language choice",
	}
	backlog := &fakeBacklog{page: app.BacklogPage{Items: []app.BacklogItem{item}}}
	store := newInMemReviewStore()
	handler := newTestHandlerWithBacklog(sender, backlog, nil, store)
	handler.newToken = sequentialToken()

	if err := handler.ProcessUpdate(context.Background(), backlogUpdate(100, 200)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(sender.prompts))
	}
	p := sender.prompts[0]
	if p.chatID != 100 {
		t.Errorf("chatID = %d, want 100", p.chatID)
	}
	if !strings.Contains(p.text, "Adopt Go") || !strings.Contains(p.text, "proposed") {
		t.Errorf("card missing title or status: %q", p.text)
	}

	buttons := 0
	seenTokens := map[string]bool{}
	for _, row := range p.rows {
		for _, btn := range row {
			buttons++
			if !strings.HasPrefix(btn.Data, "rv:") {
				t.Errorf("button data %q missing rv: prefix", btn.Data)
			}
			if len(btn.Data) > 64 {
				t.Errorf("button data %d bytes exceeds Telegram 64-byte limit", len(btn.Data))
			}
			tok := strings.TrimPrefix(btn.Data, "rv:")
			if seenTokens[tok] {
				t.Errorf("duplicate token on buttons: %q", tok)
			}
			seenTokens[tok] = true
		}
	}
	if buttons != 4 {
		t.Errorf("expected 4 buttons for proposed, got %d", buttons)
	}
	if len(store.data) != 4 {
		t.Fatalf("expected 4 saved actions, got %d", len(store.data))
	}
	// Every action must carry the context the PR3 handler will
	// load and verify. Pin each field so a future refactor cannot
	// silently drop one.
	gotActions := map[string]bool{}
	for _, a := range store.data {
		if a.WorkspaceID != "default" {
			t.Errorf("WorkspaceID = %q, want default", a.WorkspaceID)
		}
		if a.ActorID != 200 {
			t.Errorf("ActorID = %d, want 200", a.ActorID)
		}
		if a.ChatID != 100 {
			t.Errorf("ChatID = %d, want 100", a.ChatID)
		}
		if a.ObjectID != itemID {
			t.Errorf("ObjectID = %s, want %s", a.ObjectID, itemID)
		}
		if a.ExpectedStatus != domain.KnowledgeObjectStatusProposed {
			t.Errorf("ExpectedStatus = %q, want proposed", a.ExpectedStatus)
		}
		if a.ExpiresAt.IsZero() {
			t.Errorf("ExpiresAt must be set")
		}
		if a.Action == "" {
			t.Errorf("Action must be set")
		}
		gotActions[a.Action] = true
	}
	for _, want := range []string{
		app.TelegramReviewActionValidate,
		app.TelegramReviewActionDebate,
		app.TelegramReviewActionDeprecate,
		app.TelegramReviewActionSkip,
	} {
		if !gotActions[want] {
			t.Errorf("missing action %q in saved review actions: %+v", want, gotActions)
		}
	}
}

// A debating backlog item drops the debate button (MarkDebating is
// meaningless when the row is already debating) and the stale
// marker appears in the card body.
func TestBacklogCommandDebatingItemStale(t *testing.T) {
	sender := &fakeSender{}
	item := app.BacklogItem{
		ID:           uuid.New(),
		Status:       domain.KnowledgeObjectStatusDebating,
		Title:        "Long-running debate",
		IsStale:      true,
		StaleForDays: 5,
	}
	backlog := &fakeBacklog{page: app.BacklogPage{Items: []app.BacklogItem{item}}}
	store := newInMemReviewStore()
	handler := newTestHandlerWithBacklog(sender, backlog, nil, store)
	handler.newToken = sequentialToken()

	if err := handler.ProcessUpdate(context.Background(), backlogUpdate(100, 200)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(sender.prompts))
	}
	p := sender.prompts[0]
	if !strings.Contains(p.text, "stale 5 days") {
		t.Errorf("card missing stale marker: %q", p.text)
	}
	buttons := 0
	for _, row := range p.rows {
		buttons += len(row)
	}
	if buttons != 3 {
		t.Errorf("expected 3 buttons for debating, got %d", buttons)
	}
	if len(store.data) != 3 {
		t.Errorf("expected 3 saved actions, got %d", len(store.data))
	}
	for _, a := range store.data {
		if a.Action == app.TelegramReviewActionDebate {
			t.Errorf("debating backlog must not expose the debate action")
		}
	}
}

// A recently-deprecated backlog item exposes only the skip button
// (Validate/Deprecate/Debate are all no-ops on a deprecated row).
func TestBacklogCommandDeprecatedItemSkipOnly(t *testing.T) {
	sender := &fakeSender{}
	item := app.BacklogItem{
		ID:     uuid.New(),
		Status: domain.KnowledgeObjectStatusDeprecated,
		Title:  "Old API",
	}
	backlog := &fakeBacklog{page: app.BacklogPage{Items: []app.BacklogItem{item}}}
	store := newInMemReviewStore()
	handler := newTestHandlerWithBacklog(sender, backlog, nil, store)
	handler.newToken = sequentialToken()

	if err := handler.ProcessUpdate(context.Background(), backlogUpdate(100, 200)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(sender.prompts))
	}
	p := sender.prompts[0]
	buttons := 0
	for _, row := range p.rows {
		buttons += len(row)
	}
	if buttons != 1 {
		t.Errorf("expected 1 button for deprecated, got %d", buttons)
	}
	if len(store.data) != 1 {
		t.Fatalf("expected 1 saved action, got %d", len(store.data))
	}
	for _, a := range store.data {
		if a.Action != app.TelegramReviewActionSkip {
			t.Errorf("deprecated card action = %q, want skip", a.Action)
		}
	}
}

// When the KnowledgeObjectFinder returns content, the card includes
// it under a "Contenido:" header. The finder is called exactly once
// per card so a future regression that loops over items cannot
// double-hydrate.
func TestBacklogCommandHydratedContent(t *testing.T) {
	sender := &fakeSender{}
	itemID := uuid.New()
	item := app.BacklogItem{ID: itemID, Status: domain.KnowledgeObjectStatusProposed, Title: "T"}
	backlog := &fakeBacklog{page: app.BacklogPage{Items: []app.BacklogItem{item}}}
	finder := &fakeFinder{obj: &domain.KnowledgeObject{ID: itemID, Content: "Hydrated body."}}
	store := newInMemReviewStore()
	handler := newTestHandlerWithBacklog(sender, backlog, finder, store)
	handler.newToken = sequentialToken()

	if err := handler.ProcessUpdate(context.Background(), backlogUpdate(100, 200)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sender.prompts[0].text, "Hydrated body.") {
		t.Errorf("card missing hydrated content: %q", sender.prompts[0].text)
	}
	if finder.calls != 1 {
		t.Errorf("finder calls = %d, want 1", finder.calls)
	}
}

// A finder error falls back to a Title/Summary-only card; the user
// still gets a usable backlog card. This is the "best-effort
// hydration" contract the design pins.
func TestBacklogCommandFinderErrorDegrades(t *testing.T) {
	sender := &fakeSender{}
	item := app.BacklogItem{
		ID:      uuid.New(),
		Status:  domain.KnowledgeObjectStatusProposed,
		Title:   "T",
		Summary: "summary-kept",
	}
	backlog := &fakeBacklog{page: app.BacklogPage{Items: []app.BacklogItem{item}}}
	finder := &fakeFinder{err: errors.New("fts down")}
	store := newInMemReviewStore()
	handler := newTestHandlerWithBacklog(sender, backlog, finder, store)
	handler.newToken = sequentialToken()

	if err := handler.ProcessUpdate(context.Background(), backlogUpdate(100, 200)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(sender.prompts))
	}
	if !strings.Contains(sender.prompts[0].text, "summary-kept") {
		t.Errorf("card missing fallback summary: %q", sender.prompts[0].text)
	}
	if strings.Contains(sender.prompts[0].text, "Contenido:") {
		t.Errorf("card must not include Contenido: section when hydration failed: %q", sender.prompts[0].text)
	}
}

// The NextCursor returned by the backlog query is stored on every
// saved review action so the PR3 skip/next path can advance
// without re-issuing a /backlog command.
func TestBacklogCommandNextCursorStoredOnEveryAction(t *testing.T) {
	sender := &fakeSender{}
	item := app.BacklogItem{ID: uuid.New(), Status: domain.KnowledgeObjectStatusProposed, Title: "T"}
	backlog := &fakeBacklog{page: app.BacklogPage{
		Items:      []app.BacklogItem{item},
		NextCursor: "cursor-opaque-next",
	}}
	store := newInMemReviewStore()
	handler := newTestHandlerWithBacklog(sender, backlog, nil, store)
	handler.newToken = sequentialToken()

	if err := handler.ProcessUpdate(context.Background(), backlogUpdate(100, 200)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.data) == 0 {
		t.Fatalf("expected saved actions, got 0")
	}
	for _, a := range store.data {
		if a.NextCursor != "cursor-opaque-next" {
			t.Errorf("NextCursor = %q, want cursor-opaque-next", a.NextCursor)
		}
	}
}

// A review-action save failure aborts the card render and answers
// with a friendly error; the already-minted tokens are left in the
// store (Take is single-use, so they cannot be tapped twice).
func TestBacklogCommandSaveFailureDegrades(t *testing.T) {
	sender := &fakeSender{}
	item := app.BacklogItem{ID: uuid.New(), Status: domain.KnowledgeObjectStatusProposed, Title: "T"}
	backlog := &fakeBacklog{page: app.BacklogPage{Items: []app.BacklogItem{item}}}
	store := newInMemReviewStore()
	store.saveErr = errors.New("postgres unreachable")
	handler := newTestHandlerWithBacklog(sender, backlog, nil, store)
	handler.newToken = sequentialToken()

	if err := handler.ProcessUpdate(context.Background(), backlogUpdate(100, 200)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.prompts) != 0 {
		t.Errorf("save failure must not produce a buttoned prompt, got %+v", sender.prompts)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 error message, got %d", len(sender.messages))
	}
}

// A /backlog command update does NOT trigger ingestion: the
// collision path is reserved for plain-text messages, and the
// backlog flow is read-only with respect to the Telegram ingestion
// path. This pins the "thin adapter" contract.
func TestBacklogCommandDoesNotIngest(t *testing.T) {
	sender := &fakeSender{}
	item := app.BacklogItem{ID: uuid.New(), Status: domain.KnowledgeObjectStatusProposed, Title: "T"}
	backlog := &fakeBacklog{page: app.BacklogPage{Items: []app.BacklogItem{item}}}
	store := newInMemReviewStore()
	rawInputs := &fakeRawInputRepo{}
	handler := newTestHandlerWithBacklog(sender, backlog, nil, store)
	handler.rawInputs = rawInputs
	handler.newToken = sequentialToken()

	if err := handler.ProcessUpdate(context.Background(), backlogUpdate(100, 200)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rawInputs.creates) != 0 {
		t.Errorf("/backlog must not stage a raw_input; got %d Create calls", len(rawInputs.creates))
	}
	if len(sender.messages) != 0 {
		t.Errorf("/backlog must not send a plain text reply; got %+v", sender.messages)
	}
}
