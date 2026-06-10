package domain

import (
	"time"

	"github.com/google/uuid"
)

// Embedding stores a fixed-size vector representation of a
// knowledge object's content. One row per object; the FK cascade
// removes the embedding when the object is deleted.
type Embedding struct {
	ObjectID    uuid.UUID
	WorkspaceID string
	Model       string
	Dimensions  int
	Vector      []float32
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
