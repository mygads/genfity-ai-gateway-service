package http

import (
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/config"
	"genfity-ai-gateway-service/internal/handler"
	mw "genfity-ai-gateway-service/internal/http/middleware"
	"genfity-ai-gateway-service/internal/router"
	"genfity-ai-gateway-service/internal/service"
)

type Server struct {
	Router *chi.Mux
	cfg    config.Config
	log    zerolog.Logger
}

func New(cfg config.Config, redisClient *redis.Client, store service.Store, logger zerolog.Logger) *Server {
	apiKeys := service.NewAPIKeyService(store, cfg.APIKeyPepper, logger)
	models := service.NewModelService(store, logger)
	routers := service.NewRouterService(store, logger)
	combos := service.NewComboService(store, logger)
	entitlements := service.NewEntitlementService(store, logger)
	usage := service.NewUsageService(store, logger)
	syncService := service.NewSyncService(store, entitlements, models, usage, logger)

	var rateLimit *service.RateLimitService
	if redisClient != nil {
		rateLimit = service.NewRateLimitService(redisClient, cfg.RedisPrefix, logger)
	}

	cliProxyClient := router.NewCLIProxyClient(cfg.AIRouterCore2InternalURL, cfg.AIRouterCore2APIKey, time.Duration(cfg.RequestTimeoutSeconds)*time.Second)

	gatewayHandler := handler.NewGatewayHandler(models, entitlements, usage, rateLimit, combos, routers, cliProxyClient, cfg.AIRouterCore2APIKey, time.Duration(cfg.RequestTimeoutSeconds)*time.Second)
	customerHandler := handler.NewCustomerHandler(apiKeys, models, usage, entitlements)
	adminHandler := handler.NewAdminHandler(models, routers, usage)
	routerProxyHandler := handler.NewRouterProxyHandler(cliProxyClient, routers, cfg.AIRouterCore2APIKey, time.Duration(cfg.RequestTimeoutSeconds)*time.Second)
	comboHandler := handler.NewComboHandler(combos)
	syncHandler := handler.NewSyncHandler(syncService)
	healthHandler := handler.NewHealthHandler(syncService)

	authMiddleware := mw.NewAuthMiddleware(&cfg)
	apiKeyMiddleware := mw.NewAPIKeyMiddleware(&cfg, apiKeys)
	internalMiddleware := mw.NewInternalMiddleware(&cfg)
	globalRateLimitMiddleware := mw.NewGlobalRateLimitMiddleware(redisClient, cfg.RedisPrefix, cfg.GlobalRateLimitEnabled, cfg.GlobalRateLimitRPM, cfg.GlobalRateLimitBurst)

	r := chi.NewRouter()
	r.Use(mw.RequestID)
	r.Use(mw.Logging(logger))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://genfity.com", "https://www.genfity.com", "http://localhost:3000"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token", "X-Internal-Secret", "X-Request-ID"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/health", healthHandler.Check)

	r.Route("/v1", func(r chi.Router) {
		r.With(globalRateLimitMiddleware.Limit, apiKeyMiddleware.RequireAPIKey).Get("/models", gatewayHandler.ListModels)
		r.With(globalRateLimitMiddleware.Limit, apiKeyMiddleware.RequireAPIKey).Post("/chat/completions", gatewayHandler.ChatCompletions)
		r.With(globalRateLimitMiddleware.Limit, apiKeyMiddleware.RequireAPIKey).Post("/messages", gatewayHandler.Messages)
		r.With(globalRateLimitMiddleware.Limit, apiKeyMiddleware.RequireAPIKey).Post("/messages/count_tokens", gatewayHandler.CountMessageTokens)
		r.With(globalRateLimitMiddleware.Limit, apiKeyMiddleware.RequireAPIKey).Post("/embeddings", gatewayHandler.Embeddings)
	})

	r.Route("/customer", func(r chi.Router) {
		r.Use(authMiddleware.RequireRoles(cfg.AllowedCustomerRoles()...))
		r.Get("/overview", customerHandler.Overview)
		r.Get("/api-keys", customerHandler.ListAPIKeys)
		r.Post("/api-keys", customerHandler.CreateAPIKey)
		r.Delete("/api-keys/{id}", customerHandler.RevokeAPIKey)
		r.Patch("/api-keys/{id}", customerHandler.UpdateAPIKeyStatus)
		r.Get("/models", customerHandler.ListModels)
		r.Get("/usage", customerHandler.Usage)
		r.Get("/usage/summary", customerHandler.UsageSummary)
		r.Get("/quota", customerHandler.Quota)
		r.Get("/subscription", customerHandler.Subscription)
	})

	r.Route("/admin", func(r chi.Router) {
		r.Use(authMiddleware.RequireRoles(cfg.AllowedAdminRoles()...))
		r.Get("/models", adminHandler.ListModels)
		r.Post("/models", adminHandler.CreateModel)
		r.Patch("/models/{id}", adminHandler.UpdateModel)
		r.Delete("/models/{id}", adminHandler.DeleteModel)
		r.Get("/model-prices", adminHandler.ListModelPrices)
		r.Post("/model-prices", adminHandler.UpsertModelPrice)
		r.Patch("/model-prices/{id}", adminHandler.UpdateModelPrice)
		r.Delete("/model-prices/{id}", adminHandler.DeleteModelPrice)
		r.Get("/model-routes", adminHandler.ListModelRoutes)
		r.Post("/model-routes", adminHandler.UpsertModelRoute)
		r.Patch("/model-routes/{id}", adminHandler.UpdateModelRoute)
		r.Delete("/model-routes/{id}", adminHandler.DeleteModelRoute)
		r.Get("/router-instances", adminHandler.ListRouterInstances)
		r.Post("/router-instances", adminHandler.UpsertRouterInstance)
		r.Patch("/router-instances/{id}", adminHandler.UpdateRouterInstance)
		r.Delete("/router-instances/{id}", adminHandler.DeleteRouterInstance)
		r.Get("/usage", adminHandler.ListAllUsage)

		r.Get("/routers/{code}/health", routerProxyHandler.Health)
		r.Get("/routers/{code}/models", routerProxyHandler.Models)
		r.Get("/routers/{code}/providers", routerProxyHandler.Providers)
		r.Get("/routers/{code}/providers/{providerID}/models", routerProxyHandler.ProviderModels)
		r.Get("/routers/{code}/combos", comboHandler.ListCombos)
		r.Post("/routers/{code}/combos", comboHandler.CreateCombo)
		r.Put("/routers/{code}/combos/{comboID}", comboHandler.UpdateCombo)
		r.Patch("/routers/{code}/combos/{comboID}", comboHandler.UpdateCombo)
		r.Delete("/routers/{code}/combos/{comboID}", comboHandler.DeleteCombo)

		r.Get("/combos", comboHandler.ListCombos)
		r.Post("/combos", comboHandler.CreateCombo)
		r.Put("/combos/{comboID}", comboHandler.UpdateCombo)
		r.Patch("/combos/{comboID}", comboHandler.UpdateCombo)
		r.Delete("/combos/{comboID}", comboHandler.DeleteCombo)
	})

	r.Route("/internal", func(r chi.Router) {
		r.Use(internalMiddleware.RequireInternalSecret)
		r.Post("/sync/subscription-plans", syncHandler.SyncSubscriptionPlans)
		r.Post("/sync/customer-entitlements", syncHandler.SyncCustomerEntitlements)
		r.Post("/sync/customer-balance", syncHandler.SyncCustomerBalance)
		r.Get("/export/plans", syncHandler.ExportPlans)
		r.Get("/export/models", syncHandler.ExportModels)
		r.Get("/export/model-prices", syncHandler.ExportModelPrices)
		r.Get("/export/usage-summary", syncHandler.ExportUsageSummary)
	})

	return &Server{
		Router: r,
		cfg:    cfg,
		log:    logger,
	}
}
