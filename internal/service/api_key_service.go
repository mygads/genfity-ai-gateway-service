package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/store"
)

type APIKeyService struct {
	store  Store
	pepper string
	log    zerolog.Logger
}

type CreateAPIKeyInput struct {
	UserID        string
	TenantID      *string
	Name          string
	Status        string
	BillingSource string
	ExpiresAt     *time.Time
}

type CreatedAPIKey struct {
	Record store.APIKey
	RawKey string
}

func NewAPIKeyService(store Store, pepper string, logger zerolog.Logger) *APIKeyService {
	return &APIKeyService{store: store, pepper: pepper, log: logger.With().Str("component", "api_key_service").Logger()}
}

func (s *APIKeyService) Create(ctx context.Context, input CreateAPIKeyInput) (*CreatedAPIKey, error) {
	raw, prefix, err := generateRawAPIKey()
	if err != nil {
		return nil, fmt.Errorf("generate api key: %w", err)
	}

	hash := s.hash(raw)
	now := time.Now().UTC()
	record := store.APIKey{
		ID:              uuid.New(),
		GenfityUserID:   input.UserID,
		GenfityTenantID: input.TenantID,
		Name:            strings.TrimSpace(input.Name),
		KeyPrefix:       prefix,
		KeyHash:         hash,
		Status:          defaultStatus(input.Status),
		BillingSource:   defaultBillingSource(input.BillingSource),
		ExpiresAt:       input.ExpiresAt,
		CreatedAt:       now,
	}
	record, err = s.store.UpsertAPIKey(ctx, record)
	if err != nil {
		return nil, err
	}

	s.log.Info().Str("user_id", input.UserID).Str("api_key_id", record.ID.String()).Str("billing_source", record.BillingSource).Msg("created api key")

	return &CreatedAPIKey{Record: record, RawKey: raw}, nil
}

func (s *APIKeyService) Validate(ctx context.Context, rawKey string) (*store.APIKey, error) {
	prefix := extractPrefix(rawKey)
	if prefix == "" {
		return nil, ErrNotFound
	}

	record, err := s.store.FindAPIKeyByPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}

	if record.Status != "active" {
		return nil, ErrNotFound
	}
	if record.ExpiresAt != nil && record.ExpiresAt.Before(time.Now().UTC()) {
		return nil, ErrNotFound
	}

	candidateHash := s.hash(rawKey)
	if subtle.ConstantTimeCompare([]byte(candidateHash), []byte(record.KeyHash)) != 1 {
		return nil, ErrNotFound
	}

	now := time.Now().UTC()
	if err := s.store.UpdateAPIKeyLastUsedAt(ctx, record.ID, now); err != nil {
		return nil, err
	}
	record.LastUsedAt = &now
	return record, nil
}

func (s *APIKeyService) Revoke(ctx context.Context, id uuid.UUID) error {
	return s.store.RevokeAPIKey(ctx, id, time.Now().UTC())
}

// Regenerate replaces an existing key with a new raw secret while
// preserving its name, status, billing_source, and expiry. The old
// hash is overwritten so the previous raw key stops working
// immediately. The returned CreatedAPIKey carries the new raw key
// which the caller should display once and never persist server-side.
func (s *APIKeyService) Regenerate(ctx context.Context, id uuid.UUID, userID string) (*CreatedAPIKey, error) {
	keys := s.store.ListAPIKeysByUser(ctx, userID)
	var existing *store.APIKey
	for i := range keys {
		if keys[i].ID == id {
			existing = &keys[i]
			break
		}
	}
	if existing == nil {
		return nil, ErrNotFound
	}

	raw, prefix, err := generateRawAPIKey()
	if err != nil {
		return nil, fmt.Errorf("generate api key: %w", err)
	}
	hash := s.hash(raw)
	now := time.Now().UTC()
	updated := store.APIKey{
		ID:              existing.ID,
		GenfityUserID:   existing.GenfityUserID,
		GenfityTenantID: existing.GenfityTenantID,
		Name:            existing.Name,
		KeyPrefix:       prefix,
		KeyHash:         hash,
		Status:          existing.Status,
		BillingSource:   existing.BillingSource,
		ExpiresAt:       existing.ExpiresAt,
		CreatedAt:       existing.CreatedAt,
		RegeneratedAt:   &now,
	}
	saved, err := s.store.UpsertAPIKey(ctx, updated)
	if err != nil {
		return nil, err
	}
	s.log.Info().Str("user_id", userID).Str("api_key_id", id.String()).Msg("regenerated api key")
	return &CreatedAPIKey{Record: saved, RawKey: raw}, nil
}

func (s *APIKeyService) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "active" && status != "inactive" && status != "revoked" {
		return fmt.Errorf("invalid_status")
	}
	return s.store.UpdateAPIKeyStatus(ctx, id, status)
}

func (s *APIKeyService) UpdateBillingSource(ctx context.Context, id uuid.UUID, source string) error {
	source = strings.ToLower(strings.TrimSpace(source))
	if !validBillingSource(source) {
		return fmt.Errorf("invalid_billing_source")
	}
	return s.store.UpdateAPIKeyBillingSource(ctx, id, source)
}

func (s *APIKeyService) ListByUser(ctx context.Context, userID string) []store.APIKey {
	return s.store.ListAPIKeysByUser(ctx, userID)
}

func (s *APIKeyService) hash(raw string) string {
	h := hmac.New(sha256.New, []byte(s.pepper))
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

// API key format: "genfity_" + 40 hex chars (20 random bytes).
// Prefix exposed to admins is the first 16 chars (genfity_ + 8 hex).
const apiKeyPrefixLen = 16

func generateRawAPIKey() (raw string, prefix string, err error) {
	buf := make([]byte, 20)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	payload := hex.EncodeToString(buf)
	raw = fmt.Sprintf("genfity_%s", payload)
	prefix = raw[:apiKeyPrefixLen]
	return raw, prefix, nil
}

func defaultStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "inactive" || status == "revoked" {
		return status
	}
	return "active"
}

func defaultBillingSource(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	if validBillingSource(source) {
		return source
	}
	return "auto"
}

func validBillingSource(source string) bool {
	switch source {
	case "auto", "subscription", "credit", "payg":
		return true
	}
	return false
}

func extractPrefix(raw string) string {
	// New format: genfity_ + hex. Old format kept for backwards
	// compatibility with keys created before 2026-05.
	if strings.HasPrefix(raw, "genfity_") && len(raw) > apiKeyPrefixLen {
		return raw[:apiKeyPrefixLen]
	}
	if strings.HasPrefix(raw, "sk_genfity_live_") && len(raw) > 20 {
		return raw[:20]
	}
	return ""
}
