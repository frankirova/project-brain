package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/frankirova/project-brain/internal/domain"
)

// NewEmbeddingHook returns a PostIngestHook that embeds the object's
// textual content via the Embedder and upserts the resulting vector
// through the EmbeddingRepository.
//
// It is best-effort by contract: IngestTextService logs and swallows any
// error this returns, so a provider outage (e.g. Gemini quota) degrades
// future semantic search recall but never blocks ingestion. The knowledge
// object is already durably committed by the time this runs.
func NewEmbeddingHook(embedder Embedder, repo EmbeddingRepository) PostIngestHook {
	return func(ctx context.Context, obj domain.KnowledgeObject) error {
		text := embedText(obj)
		if text == "" {
			return nil
		}
		vec, err := embedder.Embed(ctx, text)
		if err != nil {
			return fmt.Errorf("embed object %s: %w", obj.ID, err)
		}
		return repo.Upsert(ctx, domain.Embedding{
			ObjectID:    obj.ID,
			WorkspaceID: obj.WorkspaceID,
			Model:       embedder.Model(),
			Dimensions:  embedder.Dimensions(),
			Vector:      vec,
		})
	}
}

// embedText joins the object's semantic fields (title, summary, content)
// into a single string for embedding, skipping empty parts. Title and
// summary add context that improves similarity matching beyond the raw
// content alone.
func embedText(obj domain.KnowledgeObject) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{obj.Title, obj.Summary, obj.Content} {
		if s := strings.TrimSpace(p); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}
