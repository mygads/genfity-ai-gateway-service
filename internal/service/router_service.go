package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/store"
)

type RouterService struct {
	store Store
	log   zerolog.Logger
}

func NewRouterService(store Store, logger zerolog.Logger) *RouterService {
	return &RouterService{store: store, log: logger.With().Str("component", "router_service").Logger()}
}

func (s *RouterService) UpsertInstance(ctx context.Context, instance store.RouterInstance) (store.RouterInstance, error) {
	if instance.Code == "" || instance.InternalBaseURL == "" {
		return store.RouterInstance{}, fmt.Errorf("code and internal_base_url are required")
	}
	if instance.ID == uuid.Nil {
		instance.ID = uuid.New()
	}
	if instance.Status == "" {
		instance.Status = "active"
	}
	if instance.CreatedAt.IsZero() {
		instance.CreatedAt = time.Now().UTC()
	}
	return s.store.UpsertRouterInstance(ctx, instance), nil
}

func (s *RouterService) ListInstances(ctx context.Context) []store.RouterInstance {
	return s.store.ListRouterInstances(ctx)
}

func (s *RouterService) GetInstance(ctx context.Context, code string) (*store.RouterInstance, error) {
	return s.store.GetRouterInstance(ctx, code)
}
