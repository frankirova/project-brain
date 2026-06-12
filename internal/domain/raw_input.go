package domain

import (
	"time"

	"github.com/google/uuid"
)

// RawInput is a staged message captured before ingestion decisions are
// made. It transitions from pending -> promoted (ingested) or
// pending -> discarded (user rejected after collision detection).
type RawInput struct {
	ID               uuid.UUID
	WorkspaceID      string
	Channel          string
	Content          string
	ActorID          string
	ExternalRef      Metadata   // JSONB; always non-nil on write (defaults to {})
	Status           string
	PromotedObjectID *uuid.UUID // non-nil iff status == "promoted"
	CollisionSummary Metadata   // nullable; nil => SQL NULL
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// RawInput status constants mirror the CHECK constraint on
// raw_inputs.status in migration 0011_raw_inputs.sql.
const (
	RawInputStatusPending   = "pending"
	RawInputStatusPromoted  = "promoted"
	RawInputStatusDiscarded = "discarded"
)

// RawInputChannelTelegram is the channel value written by the Telegram
// handler.
const RawInputChannelTelegram = "telegram"
