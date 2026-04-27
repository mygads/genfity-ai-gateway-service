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
	UserID    string
	TenantID  *string
	Name      string
	Status    string
	ExpiresAt *time.Time
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
		ExpiresAt:       input.ExpiresAt,
		CreatedAt:       now,
	}
	s.store.UpsertAPIKey(ctx, record)

	s.log.Info().Str("user_id", input.UserID).Str("api_key_id", record.ID.String()).Msg("created api key")

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
	record.LastUsedAt = &now
	s.store.UpsertAPIKey(ctx, *record)
	return record, nil
}

func (s *APIKeyService) Revoke(ctx context.Context, id uuid.UUID) error {
	return s.store.RevokeAPIKey(ctx, id, time.Now().UTC())
}

func (s *APIKeyService) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "active" && status != "inactive" && status != "revoked" {
		return fmt.Errorf("invalid_status")
	}
	return s.store.UpdateAPIKeyStatus(ctx, id, status)
}

func (s *APIKeyService) ListByUser(ctx context.Context, userID string) []store.APIKey {
	return s.store.ListAPIKeysByUser(ctx, userID)
}

func (s *APIKeyService) hash(raw string) string {
	h := hmac.New(sha256.New, []byte(s.pepper))
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

func generateRawAPIKey() (raw string, prefix string, err error) {
	buf := make([]byte, 20)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	payload := hex.EncodeToString(buf)
	raw = fmt.Sprintf("sk_genfity_live_%s", payload)
	prefix = raw[:20]
	return raw, prefix, nil
}

func defaultStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "inactive" || status == "revoked" {
		return status
	}
	return "active"
}

func extractPrefix(raw string) string {
	if len(raw) < 20 || !strings.HasPrefix(raw, "sk_genfity_live_") {
		return ""
	}
	return raw[:20]
}
