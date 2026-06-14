// Package embeddings is the home for adapter-shaped data types
// related to the embedding generation and persistence flow. The
// Embedder and EmbeddingRepository ports live in internal/app
// (retrieval.go) because app services (CollisionDetector,
// NewEmbeddingHook, EmbeddingRetryService) depend on them; per the
// spec, any adapter-specific request/response shapes for the
// embedding flow MUST land here rather than in internal/app.
//
// This file is a forward-compat placeholder: the current
// Embedder.Embed signature is (ctx, text) -> ([]float32, error),
// which has no body worth wrapping in a DTO. Future embedding
// DTOs (e.g. a batch EmbedRequest with per-text metadata, or a
// provider-specific retry envelope) have a designated home here so
// the app layer can stay clear of them.
package embeddings
