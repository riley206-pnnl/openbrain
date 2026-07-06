package brain

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/windingriverholdings/openbrain/internal/db"
	"github.com/windingriverholdings/openbrain/internal/intent"
)

// staticEmbedder returns a fixed-length embedding for any text. It lets the
// Supersede direct path run without a live Ollama or database.
type staticEmbedder struct{}

func (staticEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func (staticEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}

func (staticEmbedder) Dimension() int { return 3 }

// fakeThought models a single row of the temporal-fact table: a thought that
// is either current (live) or retired (superseded_by set, is_current false).
type fakeThought struct {
	id           string
	content      string
	isCurrent    bool
	supersededBy string
}

// fakeStore is an in-memory model of the temporal-fact invariant from
// 006_temporal_facts.sql. It exists so the atomicity and concurrency
// contract of Supersede is testable without a live PostgreSQL, mirroring the
// existing extractFn/captureFn seam pattern in Brain.
type fakeStore struct {
	mu       sync.Mutex
	thoughts map[string]*fakeThought
	nextID   int
	// failLink, when true, models a link-step failure inside the transaction:
	// the whole operation rolls back, so no new thought is ever persisted.
	failLink bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{thoughts: map[string]*fakeThought{}}
}

// seedCurrent inserts a live thought and returns its id.
func (s *fakeStore) seedCurrent(content string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := padID(s.nextID, content)
	s.thoughts[id] = &fakeThought{id: id, content: content, isCurrent: true}
	return id
}

// supersedeCapture models db.SupersedeCapture: it locks the old row, refuses
// an already-retired target, and otherwise captures the new thought and
// retires the old one atomically. On a forced link failure it persists
// nothing (rollback).
func (s *fakeStore) supersedeCapture(ctx context.Context, params db.SupersedeParams) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, ok := s.thoughts[params.OldID]
	if !ok {
		return "", db.ErrOldThoughtNotFound
	}
	// Models SELECT ... FOR UPDATE followed by the is_current guard: a second
	// concurrent caller observes the already-retired state and does not mint a
	// duplicate live thought.
	if !old.isCurrent {
		return "", db.ErrAlreadySuperseded
	}
	if s.failLink {
		// Link step fails: the new thought is never captured, the old thought
		// stays live. Both halves roll back together.
		return "", errors.New("injected link failure")
	}
	s.nextID++
	newID := padID(s.nextID, params.Content)
	s.thoughts[newID] = &fakeThought{id: newID, content: params.Content, isCurrent: true}
	old.isCurrent = false
	old.supersededBy = newID
	return newID, nil
}

// defaultSearch returns only live thoughts, modeling include_history=false.
func (s *fakeStore) defaultSearch() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, t := range s.thoughts {
		if t.isCurrent {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *fakeStore) liveCount() int {
	return len(s.defaultSearch())
}

// padID produces a deterministic id at least 8 chars long so shortID never
// panics on it, distinct per sequence number.
func padID(n int, content string) string {
	base := "0000000" + string(rune('0'+n%10))
	return base + "-" + strings.ReplaceAll(content, " ", "_")
}

func newTestBrain(store *fakeStore) *Brain {
	b := &Brain{embedder: staticEmbedder{}}
	b.supersedeFn = store.supersedeCapture
	return b
}

// TestSupersede_DirectRepro reproduces the exact OB-031 path: old_thought_id
// supplied directly (bypassing the supersedes_query search path). After a
// successful call the old thought is excluded from default search and the new
// thought is the sole live version for the slot.
func TestSupersede_DirectRepro(t *testing.T) {
	store := newFakeStore()
	oldID := store.seedCurrent("stale sadie voice canon")
	b := newTestBrain(store)

	parsed := intent.ParsedIntent{
		Intent:       intent.Supersede,
		Text:         "canonical sadie voice canon",
		ThoughtType:  "insight",
		OldThoughtID: &oldID,
	}

	msg, err := b.Supersede(context.Background(), parsed, "test")
	require.NoError(t, err)
	assert.Contains(t, msg, "supersedes")

	// Old thought must no longer appear in default search.
	live := store.defaultSearch()
	assert.NotContains(t, live, oldID, "old thought must be excluded from default search after supersede")
	assert.Equal(t, 1, store.liveCount(), "exactly one live thought for the slot")

	// The old row is retired and points at the new thought.
	old := store.thoughts[oldID]
	assert.False(t, old.isCurrent)
	assert.NotEmpty(t, old.supersededBy)
	assert.Equal(t, old.supersededBy, live[0], "retired thought points at the new live thought")
}

// TestSupersede_InjectedLinkFailureRollsBack asserts that when the
// mark-old-superseded link cannot be applied, the new thought is NOT left
// captured and the tool returns a real, typed error, never a success-shaped
// string.
func TestSupersede_InjectedLinkFailureRollsBack(t *testing.T) {
	store := newFakeStore()
	store.failLink = true
	oldID := store.seedCurrent("stale sadie voice canon")
	b := newTestBrain(store)

	parsed := intent.ParsedIntent{
		Intent:       intent.Supersede,
		Text:         "replacement that must not survive",
		ThoughtType:  "insight",
		OldThoughtID: &oldID,
	}

	msg, err := b.Supersede(context.Background(), parsed, "test")

	require.Error(t, err, "a link failure must surface as a real error, not a success string")
	assert.Empty(t, msg, "no success-shaped confirmation on failure")
	assert.NotContains(t, err.Error(), "supersede failed",
		"must not return the old success-shaped '(supersede failed)' string")

	// Rollback: no orphan new thought, and the old thought is still live.
	assert.Equal(t, 1, len(store.thoughts), "new thought must not be captured on rollback")
	assert.True(t, store.thoughts[oldID].isCurrent, "old thought stays live on rollback")
	assert.Empty(t, store.thoughts[oldID].supersededBy)
}

// TestSupersede_ConcurrentSameTarget asserts the write path is concurrent-safe:
// two supersede calls targeting the same old thought do not both mark-and-
// capture. The second observes the already-superseded state and does not mint a
// duplicate live thought. Live count stays 1.
func TestSupersede_ConcurrentSameTarget(t *testing.T) {
	store := newFakeStore()
	oldID := store.seedCurrent("stale sadie voice canon")
	b := newTestBrain(store)

	newParsed := func(text string) intent.ParsedIntent {
		id := oldID
		return intent.ParsedIntent{
			Intent:       intent.Supersede,
			Text:         text,
			ThoughtType:  "insight",
			OldThoughtID: &id,
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var successes, alreadySuperseded int
	for i, text := range []string{"first replacement", "second replacement"} {
		wg.Add(1)
		go func(text string, _ int) {
			defer wg.Done()
			_, err := b.Supersede(context.Background(), newParsed(text), "test")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, db.ErrAlreadySuperseded):
				alreadySuperseded++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(text, i)
	}
	wg.Wait()

	assert.Equal(t, 1, successes, "exactly one supersede should win")
	assert.Equal(t, 1, alreadySuperseded, "the loser observes the already-superseded state")
	assert.Equal(t, 1, store.liveCount(), "live count must stay 1 under concurrent supersede")
	assert.Equal(t, 2, len(store.thoughts), "only the old thought plus one new thought exist, no duplicate")
}
