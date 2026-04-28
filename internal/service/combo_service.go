package service

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/store"
)

type ComboService struct {
	store Store
	log   zerolog.Logger
}

func NewComboService(store Store, logger zerolog.Logger) *ComboService {
	return &ComboService{store: store, log: logger.With().Str("component", "combo_service").Logger()}
}

func (s *ComboService) ListCombos(ctx context.Context) []store.VirtualCombo {
	return s.store.ListVirtualCombos(ctx)
}

func (s *ComboService) UpsertCombo(ctx context.Context, payload map[string]any) (store.VirtualCombo, error) {
	var combo store.VirtualCombo
	b, err := json.Marshal(payload)
	if err != nil {
		return combo, err
	}
	if err := json.Unmarshal(b, &combo); err != nil {
		return combo, err
	}
	if combo.ID == uuid.Nil {
		combo.ID = uuid.New()
	}
	if combo.Status == "" {
		combo.Status = "active"
	}
	for i := range combo.Entries {
		if combo.Entries[i].ID == uuid.Nil {
			combo.Entries[i].ID = uuid.New()
		}
		combo.Entries[i].ComboID = combo.ID
	}
		saved := s.store.UpsertVirtualCombo(ctx, combo)
	if reloaded, err := s.store.GetVirtualComboByID(ctx, saved.ID); err == nil {
		return *reloaded, nil
	}
	return saved, nil
}

func (s *ComboService) DeleteCombo(ctx context.Context, id uuid.UUID) error {
	return s.store.DeleteVirtualCombo(ctx, id)
}

func (s *ComboService) GetComboForModel(ctx context.Context, modelID uuid.UUID) (*store.VirtualCombo, error) {
	return s.store.GetVirtualComboByModelID(ctx, modelID)
}

