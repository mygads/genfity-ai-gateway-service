package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/store"
)

type ModelService struct {
	store Store
	log   zerolog.Logger
}

func NewModelService(store Store, logger zerolog.Logger) *ModelService {
	return &ModelService{store: store, log: logger.With().Str("component", "model_service").Logger()}
}

func (s *ModelService) ListModels(ctx context.Context) []store.AIModel {
	return s.store.ListModels(ctx)
}

func (s *ModelService) CreateModel(ctx context.Context, model store.AIModel) (store.AIModel, error) {
	if model.PublicModel == "" || model.DisplayName == "" {
		return store.AIModel{}, fmt.Errorf("public_model and display_name are required")
	}
	if model.ID == uuid.Nil {
		model.ID = uuid.New()
	}
	if model.Status == "" {
		model.Status = "active"
	}
	model.CreatedAt = time.Now().UTC()
	model.UpdatedAt = model.CreatedAt
	return s.store.UpsertModel(ctx, model), nil
}

func (s *ModelService) UpsertPrice(ctx context.Context, price store.AIModelPrice) (store.AIModelPrice, error) {
	if price.ModelID == uuid.Nil {
		return store.AIModelPrice{}, fmt.Errorf("model_id is required")
	}
	if price.ID == uuid.Nil {
		price.ID = uuid.New()
	}
	if price.Currency == "" {
		price.Currency = "IDR"
	}
	return s.store.UpsertPrice(ctx, price), nil
}

func (s *ModelService) ListPrices(ctx context.Context) []store.AIModelPrice {
	return s.store.ListPrices(ctx)
}

func (s *ModelService) UpsertRoute(ctx context.Context, route store.AIModelRoute) (store.AIModelRoute, error) {
	if route.ModelID == uuid.Nil {
		return store.AIModelRoute{}, fmt.Errorf("model_id is required")
	}
	if route.RouterModel == "" {
		return store.AIModelRoute{}, fmt.Errorf("router_model is required")
	}
	if route.ID == uuid.Nil {
		route.ID = uuid.New()
	}
	if route.Status == "" {
		route.Status = "active"
	}
	if route.RouterInstanceCode == "" {
		route.RouterInstanceCode = "ai-core1"
	}
	return s.store.UpsertRoute(ctx, route), nil
}

func (s *ModelService) ListRoutes(ctx context.Context) []store.AIModelRoute {
	return s.store.ListRoutes(ctx)
}

func (s *ModelService) ResolveRouteByPublicModel(ctx context.Context, publicModel string) (*store.AIModelRoute, *store.AIModel, error) {
	model, err := s.store.GetModelByPublicName(ctx, publicModel)
	if err != nil {
		return nil, nil, err
	}
	route, err := s.store.GetRouteByModelID(ctx, model.ID)
	if err != nil {
		return nil, nil, err
	}
	return route, model, nil
}
