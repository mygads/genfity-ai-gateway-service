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
	PaygExposed        bool   `json:"payg_exposed"`
	// IsFree marks the model as free-tier; when true the gateway enforces
	// per-(user,model) limits below in addition to plan-level limits.
	IsFree             bool   `json:"is_free"`
	FreeLimitRPD       *int32 `json:"free_limit_rpd,omitempty"`
	FreeLimitRPM       *int32 `json:"free_limit_rpm,omitempty"`
	FreeLimitTPD       *int64 `json:"free_limit_tpd,omitempty"`
	RouterInstanceCode string `json:"router_instance_code,omitempty"`
	RouterModel        string `json:"router_model,omitempty"`
	// PAYG pricing fields — when present, SyncModels upserts an
	// ai_model_prices row so the gateway can bill PAYG requests.
	PriceInputPer1M    *string `json:"price_input_per_1m,omitempty"`
	PriceOutputPer1M   *string `json:"price_output_per_1m,omitempty"`
	PriceCachedPer1M   *string `json:"price_cached_per_1m,omitempty"`
	PriceReasoningPer1M *string `json:"price_reasoning_per_1m,omitempty"`
	PriceCurrency      string  `json:"price_currency,omitempty"`
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
			PaygExposed:       item.PaygExposed,
			IsFree:            item.IsFree,
			FreeLimitRPD:      item.FreeLimitRPD,
			FreeLimitRPM:      item.FreeLimitRPM,
			FreeLimitTPD:      item.FreeLimitTPD,
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

		// Optionally upsert PAYG price. When price fields are supplied
		// (even "0"), create/update the ai_model_prices row so the gateway
		// billing logic finds a non-nil price entry for this model.
		if item.PriceInputPer1M != nil || item.PriceOutputPer1M != nil {
			inputPrice := "0"
			if item.PriceInputPer1M != nil {
				inputPrice = *item.PriceInputPer1M
			}
			outputPrice := "0"
			if item.PriceOutputPer1M != nil {
				outputPrice = *item.PriceOutputPer1M
			}
			currency := item.PriceCurrency
			if currency == "" {
				currency = "USD"
			}

			var priceID uuid.UUID
			existingPrices := s.models.ListPrices(ctx)
			for _, p := range existingPrices {
				if p.ModelID == model.ID {
					priceID = p.ID
					break
				}
			}
			if priceID == uuid.Nil {
				priceID = uuid.New()
			}

			if _, err := s.models.UpsertPrice(ctx, store.AIModelPrice{
				ID:                  priceID,
				ModelID:             model.ID,
				InputPricePer1M:     inputPrice,
				OutputPricePer1M:    outputPrice,
				CachedPricePer1M:    item.PriceCachedPer1M,
				ReasoningPricePer1M: item.PriceReasoningPer1M,
				Currency:            currency,
				Active:              true,
			}); err != nil {
				return count, err
			}
		}

		count++
	}

	// Auto-cleanup: soft-delete models not in payload (set status='retired').
	// usage_ledger joins via uuid model_id so history is preserved.
	keep := make(map[string]struct{}, len(payload))
	for _, item := range payload {
		keep[item.PublicModel] = struct{}{}
	}
	for _, existing := range s.store.ListAllModels(ctx) {
		if _, ok := keep[existing.PublicModel]; ok {
			continue
		}
		if existing.Status == "retired" {
			continue
		}
		_ = s.store.UpdateModelStatus(ctx, existing.ID, "retired")
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
