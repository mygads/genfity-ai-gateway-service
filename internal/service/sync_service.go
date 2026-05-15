package service

import (
	"context"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/store"
)

type SyncService struct {
	store        Store
	entitlements *EntitlementService
	models       *ModelService
	usage        *UsageService
	log          zerolog.Logger
}

func NewSyncService(store Store, entitlements *EntitlementService, models *ModelService, usage *UsageService, logger zerolog.Logger) *SyncService {
	return &SyncService{
		store:        store,
		entitlements: entitlements,
		models:       models,
		usage:        usage,
		log:          logger.With().Str("component", "sync_service").Logger(),
	}
}

func (s *SyncService) SyncSubscriptionPlans(ctx context.Context, payload []store.SubscriptionPlanSnapshot) (int, error) {
	count := 0
	for _, item := range payload {
		if item.ID == uuid.Nil {
			item.ID = uuid.New()
		}
		if _, err := s.store.UpsertPlan(ctx, item); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *SyncService) SyncCustomerEntitlements(ctx context.Context, payload []store.CustomerEntitlement) (int, error) {
	count := 0
	for _, item := range payload {
		if item.ID == uuid.Nil {
			item.ID = uuid.New()
		}
		if _, err := s.entitlements.Upsert(ctx, item); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *SyncService) SyncCustomerBalance(ctx context.Context, userID string, balance string, paygBalance *string, creditExpiresAt *time.Time) error {
	_, err := s.store.UpsertBalanceSnapshot(ctx, userID, balance, paygBalance, creditExpiresAt)
	return err
}

type ReplayUsageDebitsInput struct {
	UserID string
	Since  time.Time
	Limit  int
	DryRun bool
}

type ReplayUsageDebitError struct {
	RequestID string `json:"request_id"`
	UserID    string `json:"user_id"`
	Error     string `json:"error"`
}

type ReplayUsageDebitsResult struct {
	Scanned  int                     `json:"scanned"`
	Replayed int                     `json:"replayed"`
	Skipped  int                     `json:"skipped"`
	Failed   int                     `json:"failed"`
	DryRun   bool                    `json:"dry_run"`
	Errors   []ReplayUsageDebitError `json:"errors,omitempty"`
}

func (s *SyncService) ReplayUsageDebits(ctx context.Context, callback *GenfityCallback, input ReplayUsageDebitsInput) (ReplayUsageDebitsResult, error) {
	result := ReplayUsageDebitsResult{DryRun: input.DryRun}
	if callback == nil || !callback.Enabled() {
		return result, errors.New("genfity_callback_disabled")
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}

	entries := s.replayUsageEntries(ctx, input.UserID, input.Since, limit)
	for _, entry := range entries {
		if result.Scanned >= limit {
			break
		}
		if !input.Since.IsZero() && entry.StartedAt.Before(input.Since) {
			continue
		}
		result.Scanned++

		payload, ok := replayPayloadFromUsage(entry)
		if !ok {
			result.Skipped++
			continue
		}
		if input.DryRun {
			result.Replayed++
			continue
		}

		if err := callback.PostUsageDebit(ctx, payload); err != nil {
			result.Failed++
			result.Errors = append(result.Errors, ReplayUsageDebitError{RequestID: entry.RequestID, UserID: entry.GenfityUserID, Error: err.Error()})
			continue
		}
		result.Replayed++
	}

	return result, nil
}

func (s *SyncService) replayUsageEntries(ctx context.Context, userID string, since time.Time, limit int) []store.UsageLedgerEntry {
	if userID != "" && !since.IsZero() {
		return s.store.ListUsageByUserSince(ctx, userID, since)
	}
	if userID != "" {
		return s.store.ListUsageByUser(ctx, userID)
	}
	return s.store.ListAllUsage(ctx, limit)
}

func replayPayloadFromUsage(entry store.UsageLedgerEntry) (UsageDebitPayload, bool) {
	if entry.Status != "success" || entry.BillingMode == nil || entry.RequestID == "" || entry.GenfityUserID == "" {
		return UsageDebitPayload{}, false
	}

	switch *entry.BillingMode {
	case "credit_package":
		amount, ok := parseUsageAmount(entry.AmountCredits)
		if !ok || amount <= 0 {
			return UsageDebitPayload{}, false
		}
		return UsageDebitPayload{
			UserID:        entry.GenfityUserID,
			RequestID:     entry.RequestID,
			BillingMode:   "credit_package",
			AmountCredits: amount,
			Model:         entry.PublicModel,
			Notes:         "gateway debit replay",
		}, true
	case "payg_topup":
		amount, ok := parseUsageAmount(&entry.TotalCost)
		if !ok || amount <= 0 {
			return UsageDebitPayload{}, false
		}
		return UsageDebitPayload{
			UserID:      entry.GenfityUserID,
			RequestID:   entry.RequestID,
			BillingMode: "payg_topup",
			AmountUSD:   amount,
			Model:       entry.PublicModel,
			Notes:       "gateway debit replay",
		}, true
	default:
		return UsageDebitPayload{}, false
	}
}

func parseUsageAmount(value *string) (float64, bool) {
	if value == nil || *value == "" {
		return 0, false
	}
	amount, err := strconv.ParseFloat(*value, 64)
	if err != nil {
		return 0, false
	}
	if amount < 0 {
		return 0, false
	}
	if amount > 1_000_000 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, false
	}
	return amount, true
}

// SyncModelCreditCosts upserts each incoming row by FullModelID. Missing
// rows on the incoming side are left untouched — the source of truth
// (genfity-app) emits the full catalog every sync.
func (s *SyncService) SyncModelCreditCosts(ctx context.Context, payload []store.ModelCreditCost) (int, error) {
	count := 0
	for _, item := range payload {
		if item.FullModelID == "" {
			return count, errors.New("full_model_id required")
		}
		if _, err := s.store.UpsertModelCreditCost(ctx, item); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// SyncPaygTopupRates upserts each incoming row by Code. Same contract
// as SyncModelCreditCosts — caller sends the full catalog.
func (s *SyncService) SyncPaygTopupRates(ctx context.Context, payload []store.PaygTopupRate) (int, error) {
	count := 0
	for _, item := range payload {
		if item.Code == "" {
			return count, errors.New("code required")
		}
		if _, err := s.store.UpsertPaygTopupRate(ctx, item); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// SyncModels upserts models (and optionally their default route) so the
// gateway's ai_models / ai_model_routes tables stay in sync with
// genfity-app's AiGatewayModelCache catalog. Without this sync, models
// admins publish via the catalog page would be missing from the gateway
// store and the request path would reject them as "model_not_allowed".
//
// The payload is a list of models, each carrying enough metadata to
// upsert both the model row and (optionally) a single active route.
// When RouterInstanceCode/RouterModel are non-empty we also upsert the
// route; otherwise we leave any existing route untouched so manual
// admin route configuration is not clobbered.
type ModelSyncItem struct {
	PublicModel        string `json:"public_model"`
	DisplayName        string `json:"display_name"`
	Description        string `json:"description,omitempty"`
	Status             string `json:"status,omitempty"` // "active" | "draft"
	ContextWindow      *int32 `json:"context_window,omitempty"`
	SupportsStreaming  bool   `json:"supports_streaming"`
	SupportsTools      bool   `json:"supports_tools"`
	SupportsVision     bool   `json:"supports_vision"`
	RouterInstanceCode string `json:"router_instance_code,omitempty"`
	RouterModel        string `json:"router_model,omitempty"`
}

func (s *SyncService) SyncModels(ctx context.Context, payload []ModelSyncItem) (int, error) {
	count := 0
	for _, item := range payload {
		if item.PublicModel == "" {
			return count, errors.New("public_model required")
		}

		desc := item.Description

		status := item.Status
		if status == "" {
			status = "active"
		}

		// Upsert ai_models. We look up the existing row by public_model
		// so we keep the same UUID across syncs (routes/usage join on
		// model_id).
		var modelID uuid.UUID
		if existing, err := s.store.GetModelByPublicName(ctx, item.PublicModel); err == nil && existing != nil {
			modelID = existing.ID
		} else {
			modelID = uuid.New()
		}

		model, err := s.store.UpsertModel(ctx, store.AIModel{
			ID:                modelID,
			PublicModel:       item.PublicModel,
			DisplayName:       item.DisplayName,
			Description:       desc,
			Status:            status,
			ContextWindow:     item.ContextWindow,
			SupportsStreaming: item.SupportsStreaming,
			SupportsTools:     item.SupportsTools,
			SupportsVision:    item.SupportsVision,
		})
		if err != nil {
			return count, err
		}

		// Optionally upsert route. If neither router code nor model is
		// supplied we skip — admin may have configured the route manually
		// and we don't want to overwrite it with empty values.
		if item.RouterInstanceCode != "" || item.RouterModel != "" {
			var routeID uuid.UUID
			if existing, err := s.store.GetRouteByModelID(ctx, model.ID); err == nil && existing != nil {
				routeID = existing.ID
			} else {
				routeID = uuid.New()
			}
			if _, err := s.store.UpsertRoute(ctx, store.AIModelRoute{
				ID:                 routeID,
				ModelID:            model.ID,
				RouterInstanceCode: item.RouterInstanceCode,
				RouterModel:        item.RouterModel,
				Status:             "active",
			}); err != nil {
				return count, err
			}
		}

		count++
	}
	return count, nil
}

func (s *SyncService) ExportModels(ctx context.Context) []store.AIModel {
	return s.models.ListModels(ctx)
}

func (s *SyncService) ExportPlans(ctx context.Context) []store.SubscriptionPlanSnapshot {
	return s.store.ListPlans(ctx)
}

func (s *SyncService) ExportModelPrices(ctx context.Context) []store.AIModelPrice {
	return s.models.ListPrices(ctx)
}

func (s *SyncService) ExportUsageSummary(ctx context.Context, userID string) map[string]any {
	return s.usage.SummaryByUser(ctx, userID)
}
