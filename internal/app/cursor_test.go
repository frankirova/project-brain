package app

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBacklogCursorRoundTripPreservesTimestampAndID(t *testing.T) {
	now := time.Date(2026, 6, 12, 15, 0, 0, 123456789, time.UTC)
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")

	token := EncodeBacklogCursor(now, id)
	if token == "" {
		t.Fatalf("EncodeBacklogCursor returned empty token")
	}

	gotTime, gotID, err := DecodeBacklogCursor(token)
	if err != nil {
		t.Fatalf("DecodeBacklogCursor returned error: %v", err)
	}
	if !gotTime.Equal(now) {
		t.Fatalf("decoded time = %v, want %v", gotTime, now)
	}
	if gotID != id {
		t.Fatalf("decoded id = %v, want %v", gotID, id)
	}
}

func TestBacklogCursorEncodedTokenIsURLSafeAndUnpadded(t *testing.T) {
	now := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	token := EncodeBacklogCursor(now, id)
	// RFC 4648 §5 (URL-safe) uses '-' and '_' instead of '+' and '/'.
	// RawURLEncoding emits no '=' padding, so a stray '=' is also a
	// violation of the contract.
	if strings.ContainsAny(token, "+/=") {
		t.Fatalf("token %q contains non-URL-safe or padded base64 chars", token)
	}
}

func TestBacklogCursorDecodeRejectsMalformedInput(t *testing.T) {
	// Sanity: a freshly encoded token MUST decode without error,
	// otherwise the negative cases below would be vacuously true.
	now := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	id := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	validToken := EncodeBacklogCursor(now, id)
	if _, _, err := DecodeBacklogCursor(validToken); err != nil {
		t.Fatalf("sanity: DecodeBacklogCursor(validToken) = %v, want nil", err)
	}

	cases := []struct {
		name  string
		token string
	}{
		{name: "empty string", token: ""},
		{name: "non-base64 garbage", token: "!!!not base64!!!"},
		{name: "base64 of non-JSON", token: base64.RawURLEncoding.EncodeToString([]byte("hello world"))},
		{name: "JSON missing updated_at", token: base64.RawURLEncoding.EncodeToString([]byte(`{"id":"00000000-0000-0000-0000-000000000003"}`))},
		{name: "JSON missing id", token: base64.RawURLEncoding.EncodeToString([]byte(`{"updated_at":"2026-06-12T00:00:00Z"}`))},
		{name: "JSON with zero UUID", token: base64.RawURLEncoding.EncodeToString([]byte(`{"updated_at":"2026-06-12T00:00:00Z","id":"00000000-0000-0000-0000-000000000000"}`))},
		{name: "JSON with bad timestamp", token: base64.RawURLEncoding.EncodeToString([]byte(`{"updated_at":"not-a-timestamp","id":"00000000-0000-0000-0000-000000000004"}`))},
		{name: "JSON with bad UUID", token: base64.RawURLEncoding.EncodeToString([]byte(`{"updated_at":"2026-06-12T00:00:00Z","id":"not-a-uuid"}`))},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotTime, gotID, err := DecodeBacklogCursor(tt.token)
			if err == nil {
				t.Fatalf("DecodeBacklogCursor(%q) = (%v, %v, nil), want ErrInvalidCursor", tt.token, gotTime, gotID)
			}
			if err != ErrInvalidCursor {
				t.Fatalf("DecodeBacklogCursor(%q) error = %v, want ErrInvalidCursor", tt.token, err)
			}
			if !gotTime.IsZero() || gotID != uuid.Nil {
				t.Fatalf("DecodeBacklogCursor(%q) returned (%v, %v), want zero values on error", tt.token, gotTime, gotID)
			}
		})
	}
}
