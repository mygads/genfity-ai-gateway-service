package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"genfity-ai-gateway-service/internal/store"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) UpsertPlan(ctx context.Context, item store.SubscriptionPlanSnapshot) (store.SubscriptionPlanSnapshot, error) {
	metadata := rawJSON(item.Metadata)
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.subscription_plan_snapshots (
			id, plan_code, display_name, status, monthly_price, currency,
			quota_tokens_monthly, rate_limit_rpm, rate_limit_tpm, concurrent_limit,
			metadata, synced_from_genfity_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (plan_code) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			status = EXCLUDED.status,
			monthly_price = EXCLUDED.monthly_price,
			currency = EXCLUDED.currency,
			quota_tokens_monthly = EXCLUDED.quota_tokens_monthly,
			rate_limit_rpm = EXCLUDED.rate_limit_rpm,
			rate_limit_tpm = EXCLUDED.rate_limit_tpm,
			concurrent_limit = EXCLUDED.concurrent_limit,
			metadata = EXCLUDED.metadata,
			synced_from_genfity_at = EXCLUDED.synced_from_genfity_at,
			updated_at = now()
		RETURNING id, plan_code, display_name, status, monthly_price::text, currency,
			quota_tokens_monthly, rate_limit_rpm, rate_limit_tpm, concurrent_limit,
			metadata, synced_from_genfity_at, created_at, updated_at`,
		nilUUID(item.ID), item.PlanCode, item.DisplayName, defaultString(item.Status, "active"), item.MonthlyPrice,
		defaultString(item.Currency, "IDR"), item.QuotaTokensMonthly, item.RateLimitRPM, item.RateLimitTPM,
		item.ConcurrentLimit, metadata, defaultTime(item.SyncedFromGenfityAt),
	).Scan(&item.ID, &item.PlanCode, &item.DisplayName, &item.Status, &item.MonthlyPrice, &item.Currency,
		&item.QuotaTokensMonthly, &item.RateLimitRPM, &item.RateLimitTPM, &item.ConcurrentLimit,
		&item.Metadata, &item.SyncedFromGenfityAt, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return store.SubscriptionPlanSnapshot{}, err
	}
	return item, nil
}

func (s *PostgresStore) ListPlans(ctx context.Context) []store.SubscriptionPlanSnapshot {
	rows, err := s.pool.Query(ctx, `SELECT id, plan_code, display_name, status, monthly_price::text, currency, quota_tokens_monthly, rate_limit_rpm, rate_limit_tpm, concurrent_limit, metadata, synced_from_genfity_at, created_at, updated_at FROM ai_gateway.subscription_plan_snapshots ORDER BY plan_code ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.SubscriptionPlanSnapshot{}
	for rows.Next() {
		var item store.SubscriptionPlanSnapshot
		if rows.Scan(&item.ID, &item.PlanCode, &item.DisplayName, &item.Status, &item.MonthlyPrice, &item.Currency, &item.QuotaTokensMonthly, &item.RateLimitRPM, &item.RateLimitTPM, &item.ConcurrentLimit, &item.Metadata, &item.SyncedFromGenfityAt, &item.CreatedAt, &item.UpdatedAt) == nil {
			items = append(items, item)
		}
	}
	return items
}

func (s *PostgresStore) GetPlanByCode(ctx context.Context, planCode string) (*store.SubscriptionPlanSnapshot, error) {
	var item store.SubscriptionPlanSnapshot
	err := s.pool.QueryRow(ctx, `SELECT id, plan_code, display_name, status, monthly_price::text, currency, quota_tokens_monthly, rate_limit_rpm, rate_limit_tpm, concurrent_limit, metadata, synced_from_genfity_at, created_at, updated_at FROM ai_gateway.subscription_plan_snapshots WHERE plan_code = $1 LIMIT 1`, planCode).Scan(
		&item.ID, &item.PlanCode, &item.DisplayName, &item.Status, &item.MonthlyPrice, &item.Currency,
		&item.QuotaTokensMonthly, &item.RateLimitRPM, &item.RateLimitTPM, &item.ConcurrentLimit,
		&item.Metadata, &item.SyncedFromGenfityAt, &item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) UpsertAPIKey(ctx context.Context, item store.APIKey) (store.APIKey, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.api_keys (id, genfity_user_id, genfity_tenant_id, name, key_prefix, key_hash, status, last_used_at, expires_at, created_at, revoked_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			key_hash = EXCLUDED.key_hash,
			status = EXCLUDED.status,
			last_used_at = EXCLUDED.last_used_at,
			expires_at = EXCLUDED.expires_at,
			revoked_at = EXCLUDED.revoked_at
		RETURNING id, genfity_user_id, genfity_tenant_id, name, key_prefix, key_hash, status, last_used_at, expires_at, created_at, revoked_at`,
		nilUUID(item.ID), item.GenfityUserID, item.GenfityTenantID, item.Name, item.KeyPrefix, item.KeyHash,
		defaultString(item.Status, "active"), item.LastUsedAt, item.ExpiresAt, defaultTime(item.CreatedAt), item.RevokedAt,
	).Scan(&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.Name, &item.KeyPrefix, &item.KeyHash, &item.Status, &item.LastUsedAt, &item.ExpiresAt, &item.CreatedAt, &item.RevokedAt)
	if err != nil {
		return store.APIKey{}, err
	}
	return item, nil
}

func (s *PostgresStore) ListAPIKeysByUser(ctx context.Context, userID string) []store.APIKey {
	rows, err := s.pool.Query(ctx, `SELECT id, genfity_user_id, genfity_tenant_id, name, key_prefix, key_hash, status, last_used_at, expires_at, created_at, revoked_at FROM ai_gateway.api_keys WHERE genfity_user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.APIKey{}
	for rows.Next() {
		var item store.APIKey
		if scanAPIKey(rows, &item) == nil {
			items = append(items, item)
		}
	}
	return items
}

func (s *PostgresStore) FindAPIKeyByPrefix(ctx context.Context, prefix string) (*store.APIKey, error) {
	var item store.APIKey
	err := s.pool.QueryRow(ctx, `SELECT id, genfity_user_id, genfity_tenant_id, name, key_prefix, key_hash, status, last_used_at, expires_at, created_at, revoked_at FROM ai_gateway.api_keys WHERE key_prefix = $1 LIMIT 1`, prefix).Scan(&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.Name, &item.KeyPrefix, &item.KeyHash, &item.Status, &item.LastUsedAt, &item.ExpiresAt, &item.CreatedAt, &item.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) RevokeAPIKey(ctx context.Context, id uuid.UUID, revokedAt time.Time) error {
	cmd, err := s.pool.Exec(ctx, `UPDATE ai_gateway.api_keys SET status = 'revoked', revoked_at = $2 WHERE id = $1`, id, revokedAt)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) UpdateAPIKeyStatus(ctx context.Context, id uuid.UUID, status string) error {
	cmd, err := s.pool.Exec(ctx, `UPDATE ai_gateway.api_keys SET status = $2, revoked_at = CASE WHEN $2 = 'revoked' THEN now() ELSE NULL END WHERE id = $1`, id, status)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) UpsertModel(ctx context.Context, item store.AIModel) (store.AIModel, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.ai_models (id, public_model, display_name, description, status, context_window, supports_streaming, supports_tools, supports_vision)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (public_model) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			description = EXCLUDED.description,
			status = EXCLUDED.status,
			context_window = EXCLUDED.context_window,
			supports_streaming = EXCLUDED.supports_streaming,
			supports_tools = EXCLUDED.supports_tools,
			supports_vision = EXCLUDED.supports_vision,
			updated_at = now()
		RETURNING id, public_model, display_name, description, status, context_window, supports_streaming, supports_tools, supports_vision, created_at, updated_at`,
		nilUUID(item.ID), item.PublicModel, item.DisplayName, item.Description, defaultString(item.Status, "active"), item.ContextWindow, item.SupportsStreaming, item.SupportsTools, item.SupportsVision,
	).Scan(&item.ID, &item.PublicModel, &item.DisplayName, &item.Description, &item.Status, &item.ContextWindow, &item.SupportsStreaming, &item.SupportsTools, &item.SupportsVision, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return store.AIModel{}, err
	}
	return item, nil
}

func (s *PostgresStore) UpdateModel(ctx context.Context, item store.AIModel) (store.AIModel, error) {
	err := s.pool.QueryRow(ctx, `
		UPDATE ai_gateway.ai_models
		SET public_model = $2,
			display_name = $3,
			description = $4,
			status = $5,
			context_window = $6,
			supports_streaming = $7,
			supports_tools = $8,
			supports_vision = $9,
			updated_at = now()
		WHERE id = $1
		RETURNING id, public_model, display_name, description, status, context_window, supports_streaming, supports_tools, supports_vision, created_at, updated_at`,
		item.ID, item.PublicModel, item.DisplayName, item.Description, defaultString(item.Status, "active"), item.ContextWindow, item.SupportsStreaming, item.SupportsTools, item.SupportsVision,
	).Scan(&item.ID, &item.PublicModel, &item.DisplayName, &item.Description, &item.Status, &item.ContextWindow, &item.SupportsStreaming, &item.SupportsTools, &item.SupportsVision, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.AIModel{}, ErrNotFound
	}
	return item, err
}

func (s *PostgresStore) DeleteModel(ctx context.Context, id uuid.UUID) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM ai_gateway.ai_models WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListModels(ctx context.Context) []store.AIModel {
	rows, err := s.pool.Query(ctx, `SELECT id, public_model, display_name, description, status, context_window, supports_streaming, supports_tools, supports_vision, created_at, updated_at FROM ai_gateway.ai_models ORDER BY display_name ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.AIModel{}
	for rows.Next() {
		var item store.AIModel
		if scanModel(rows, &item) == nil {
			items = append(items, item)
		}
	}
	return items
}

func (s *PostgresStore) GetModelByID(ctx context.Context, id uuid.UUID) (*store.AIModel, error) {
	var item store.AIModel
	err := s.pool.QueryRow(ctx, `SELECT id, public_model, display_name, description, status, context_window, supports_streaming, supports_tools, supports_vision, created_at, updated_at FROM ai_gateway.ai_models WHERE id = $1 LIMIT 1`, id).Scan(&item.ID, &item.PublicModel, &item.DisplayName, &item.Description, &item.Status, &item.ContextWindow, &item.SupportsStreaming, &item.SupportsTools, &item.SupportsVision, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) GetModelByPublicName(ctx context.Context, publicModel string) (*store.AIModel, error) {
	var item store.AIModel
	err := s.pool.QueryRow(ctx, `SELECT id, public_model, display_name, description, status, context_window, supports_streaming, supports_tools, supports_vision, created_at, updated_at FROM ai_gateway.ai_models WHERE public_model = $1 LIMIT 1`, publicModel).Scan(&item.ID, &item.PublicModel, &item.DisplayName, &item.Description, &item.Status, &item.ContextWindow, &item.SupportsStreaming, &item.SupportsTools, &item.SupportsVision, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) UpsertPrice(ctx context.Context, item store.AIModelPrice) (store.AIModelPrice, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.ai_model_prices (id, model_id, input_price_per_1m, output_price_per_1m, cached_price_per_1m, reasoning_price_per_1m, currency, active)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE SET input_price_per_1m = EXCLUDED.input_price_per_1m, output_price_per_1m = EXCLUDED.output_price_per_1m, cached_price_per_1m = EXCLUDED.cached_price_per_1m, reasoning_price_per_1m = EXCLUDED.reasoning_price_per_1m, currency = EXCLUDED.currency, active = EXCLUDED.active
		RETURNING id, model_id, input_price_per_1m::text, output_price_per_1m::text, cached_price_per_1m::text, reasoning_price_per_1m::text, currency, active, created_at`,
		nilUUID(item.ID), item.ModelID, item.InputPricePer1M, item.OutputPricePer1M, item.CachedPricePer1M, item.ReasoningPricePer1M, defaultString(item.Currency, "IDR"), item.Active,
	).Scan(&item.ID, &item.ModelID, &item.InputPricePer1M, &item.OutputPricePer1M, &item.CachedPricePer1M, &item.ReasoningPricePer1M, &item.Currency, &item.Active, &item.CreatedAt)
	if err != nil {
		return store.AIModelPrice{}, err
	}
	return item, nil
}

func (s *PostgresStore) GetPriceByID(ctx context.Context, id uuid.UUID) (*store.AIModelPrice, error) {
	var item store.AIModelPrice
	err := s.pool.QueryRow(ctx, `SELECT id, model_id, input_price_per_1m::text, output_price_per_1m::text, cached_price_per_1m::text, reasoning_price_per_1m::text, currency, active, created_at FROM ai_gateway.ai_model_prices WHERE id = $1 LIMIT 1`, id).Scan(&item.ID, &item.ModelID, &item.InputPricePer1M, &item.OutputPricePer1M, &item.CachedPricePer1M, &item.ReasoningPricePer1M, &item.Currency, &item.Active, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) UpdatePrice(ctx context.Context, item store.AIModelPrice) (store.AIModelPrice, error) {
	err := s.pool.QueryRow(ctx, `
		UPDATE ai_gateway.ai_model_prices
		SET model_id = $2,
			input_price_per_1m = $3,
			output_price_per_1m = $4,
			cached_price_per_1m = $5,
			reasoning_price_per_1m = $6,
			currency = $7,
			active = $8
		WHERE id = $1
		RETURNING id, model_id, input_price_per_1m::text, output_price_per_1m::text, cached_price_per_1m::text, reasoning_price_per_1m::text, currency, active, created_at`,
		item.ID, item.ModelID, item.InputPricePer1M, item.OutputPricePer1M, item.CachedPricePer1M, item.ReasoningPricePer1M, defaultString(item.Currency, "IDR"), item.Active,
	).Scan(&item.ID, &item.ModelID, &item.InputPricePer1M, &item.OutputPricePer1M, &item.CachedPricePer1M, &item.ReasoningPricePer1M, &item.Currency, &item.Active, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.AIModelPrice{}, ErrNotFound
	}
	return item, err
}

func (s *PostgresStore) DeletePrice(ctx context.Context, id uuid.UUID) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM ai_gateway.ai_model_prices WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListPrices(ctx context.Context) []store.AIModelPrice {
	rows, err := s.pool.Query(ctx, `SELECT id, model_id, input_price_per_1m::text, output_price_per_1m::text, cached_price_per_1m::text, reasoning_price_per_1m::text, currency, active, created_at FROM ai_gateway.ai_model_prices ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.AIModelPrice{}
	for rows.Next() {
		var item store.AIModelPrice
		if rows.Scan(&item.ID, &item.ModelID, &item.InputPricePer1M, &item.OutputPricePer1M, &item.CachedPricePer1M, &item.ReasoningPricePer1M, &item.Currency, &item.Active, &item.CreatedAt) == nil {
			items = append(items, item)
		}
	}
	return items
}

func (s *PostgresStore) UpsertRoute(ctx context.Context, item store.AIModelRoute) (store.AIModelRoute, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.ai_model_routes (id, model_id, router_instance_code, router_model, status, metadata)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (id) DO UPDATE SET router_instance_code = EXCLUDED.router_instance_code, router_model = EXCLUDED.router_model, status = EXCLUDED.status, metadata = EXCLUDED.metadata
		RETURNING id, model_id, router_instance_code, router_model, status, metadata, created_at`,
		nilUUID(item.ID), item.ModelID, item.RouterInstanceCode, item.RouterModel, defaultString(item.Status, "active"), rawJSON(item.Metadata),
	).Scan(&item.ID, &item.ModelID, &item.RouterInstanceCode, &item.RouterModel, &item.Status, &item.Metadata, &item.CreatedAt)
	if err != nil {
		return store.AIModelRoute{}, err
	}
	return item, nil
}

func (s *PostgresStore) GetRouteByID(ctx context.Context, id uuid.UUID) (*store.AIModelRoute, error) {
	var item store.AIModelRoute
	err := s.pool.QueryRow(ctx, `SELECT id, model_id, router_instance_code, router_model, status, metadata, created_at FROM ai_gateway.ai_model_routes WHERE id = $1 LIMIT 1`, id).Scan(&item.ID, &item.ModelID, &item.RouterInstanceCode, &item.RouterModel, &item.Status, &item.Metadata, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) UpdateRoute(ctx context.Context, item store.AIModelRoute) (store.AIModelRoute, error) {
	err := s.pool.QueryRow(ctx, `
		UPDATE ai_gateway.ai_model_routes
		SET model_id = $2,
			router_instance_code = $3,
			router_model = $4,
			status = $5,
			metadata = $6
		WHERE id = $1
		RETURNING id, model_id, router_instance_code, router_model, status, metadata, created_at`,
		item.ID, item.ModelID, item.RouterInstanceCode, item.RouterModel, defaultString(item.Status, "active"), rawJSON(item.Metadata),
	).Scan(&item.ID, &item.ModelID, &item.RouterInstanceCode, &item.RouterModel, &item.Status, &item.Metadata, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.AIModelRoute{}, ErrNotFound
	}
	return item, err
}

func (s *PostgresStore) DeleteRoute(ctx context.Context, id uuid.UUID) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM ai_gateway.ai_model_routes WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListRoutes(ctx context.Context) []store.AIModelRoute {
	rows, err := s.pool.Query(ctx, `SELECT id, model_id, router_instance_code, router_model, status, metadata, created_at FROM ai_gateway.ai_model_routes ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.AIModelRoute{}
	for rows.Next() {
		var item store.AIModelRoute
		if scanRoute(rows, &item) == nil {
			items = append(items, item)
		}
	}
	return items
}

func (s *PostgresStore) GetRouteByModelID(ctx context.Context, modelID uuid.UUID) (*store.AIModelRoute, error) {
	var item store.AIModelRoute
	err := s.pool.QueryRow(ctx, `SELECT id, model_id, router_instance_code, router_model, status, metadata, created_at FROM ai_gateway.ai_model_routes WHERE model_id = $1 AND status = 'active' LIMIT 1`, modelID).Scan(&item.ID, &item.ModelID, &item.RouterInstanceCode, &item.RouterModel, &item.Status, &item.Metadata, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) UpsertEntitlement(ctx context.Context, item store.CustomerEntitlement) (store.CustomerEntitlement, error) {
	return s.upsertEntitlement(ctx, item)
}

func (s *PostgresStore) UpsertEntitlementByUser(ctx context.Context, item store.CustomerEntitlement) (store.CustomerEntitlement, error) {
	return s.upsertEntitlement(ctx, item)
}

func (s *PostgresStore) upsertEntitlement(ctx context.Context, item store.CustomerEntitlement) (store.CustomerEntitlement, error) {
	var metadata json.RawMessage
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.customer_entitlements (id, genfity_user_id, genfity_tenant_id, plan_code, status, period_start, period_end, quota_tokens_monthly, balance_snapshot, metadata, updated_from_genfity_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (genfity_user_id, plan_code) DO UPDATE SET genfity_tenant_id = EXCLUDED.genfity_tenant_id, status = EXCLUDED.status, period_start = EXCLUDED.period_start, period_end = EXCLUDED.period_end, quota_tokens_monthly = EXCLUDED.quota_tokens_monthly, balance_snapshot = EXCLUDED.balance_snapshot, metadata = EXCLUDED.metadata, updated_from_genfity_at = EXCLUDED.updated_from_genfity_at, updated_at = now()
		RETURNING id, genfity_user_id, genfity_tenant_id, plan_code, status, period_start, period_end, quota_tokens_monthly, balance_snapshot::text, metadata, updated_from_genfity_at`,
		nilUUID(item.ID), item.GenfityUserID, item.GenfityTenantID, item.PlanCode, defaultString(item.Status, "active"), item.PeriodStart, item.PeriodEnd, item.QuotaTokensMonthly, item.BalanceSnapshot, rawJSON(item.Metadata), defaultTime(item.UpdatedFromGenfityAt),
	).Scan(&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.PlanCode, &item.Status, &item.PeriodStart, &item.PeriodEnd, &item.QuotaTokensMonthly, &item.BalanceSnapshot, &metadata, &item.UpdatedFromGenfityAt)
	if err != nil {
		return store.CustomerEntitlement{}, err
	}
	item.Metadata = metadata
	return item, nil
}

func (s *PostgresStore) GetEntitlementByUser(ctx context.Context, userID string) (*store.CustomerEntitlement, error) {
	var item store.CustomerEntitlement
	var metadata json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT id, genfity_user_id, genfity_tenant_id, plan_code, status, period_start, period_end, quota_tokens_monthly, balance_snapshot::text, metadata, updated_from_genfity_at FROM ai_gateway.customer_entitlements WHERE genfity_user_id = $1 AND status = 'active' AND (period_end IS NULL OR period_end > now()) ORDER BY CASE WHEN metadata->>'pricingGroup' = 'unlimited_plan' THEN 0 WHEN metadata->>'pricingGroup' = 'credit_package' THEN 1 ELSE 2 END, updated_at DESC LIMIT 1`, userID).Scan(&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.PlanCode, &item.Status, &item.PeriodStart, &item.PeriodEnd, &item.QuotaTokensMonthly, &item.BalanceSnapshot, &metadata, &item.UpdatedFromGenfityAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	item.Metadata = metadata
	return &item, err
}

func (s *PostgresStore) UpsertBalanceSnapshot(ctx context.Context, userID string, balance string) (*store.CustomerEntitlement, error) {
	var item store.CustomerEntitlement
	var metadata json.RawMessage
	err := s.pool.QueryRow(ctx, `UPDATE ai_gateway.customer_entitlements SET balance_snapshot = $2, updated_from_genfity_at = now(), updated_at = now() WHERE genfity_user_id = $1 RETURNING id, genfity_user_id, genfity_tenant_id, plan_code, status, period_start, period_end, quota_tokens_monthly, balance_snapshot::text, metadata, updated_from_genfity_at`, userID, balance).Scan(&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.PlanCode, &item.Status, &item.PeriodStart, &item.PeriodEnd, &item.QuotaTokensMonthly, &item.BalanceSnapshot, &metadata, &item.UpdatedFromGenfityAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	item.Metadata = metadata
	return &item, err
}

func (s *PostgresStore) UpsertRouterInstance(ctx context.Context, item store.RouterInstance) (store.RouterInstance, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.router_instances (id, code, public_base_url, internal_base_url, status, encrypted_api_key, health_status, last_health_check_at, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (code) DO UPDATE SET public_base_url = EXCLUDED.public_base_url, internal_base_url = EXCLUDED.internal_base_url, status = EXCLUDED.status, encrypted_api_key = EXCLUDED.encrypted_api_key, health_status = EXCLUDED.health_status, last_health_check_at = EXCLUDED.last_health_check_at, metadata = EXCLUDED.metadata
		RETURNING id, code, public_base_url, internal_base_url, status, encrypted_api_key, health_status, last_health_check_at, metadata, created_at`,
		nilUUID(item.ID), item.Code, item.PublicBaseURL, item.InternalBaseURL, defaultString(item.Status, "active"), item.EncryptedAPIKey, item.HealthStatus, item.LastHealthCheckAt, rawJSON(item.Metadata),
	).Scan(&item.ID, &item.Code, &item.PublicBaseURL, &item.InternalBaseURL, &item.Status, &item.EncryptedAPIKey, &item.HealthStatus, &item.LastHealthCheckAt, &item.Metadata, &item.CreatedAt)
	if err != nil {
		return store.RouterInstance{}, err
	}
	return item, nil
}

func (s *PostgresStore) GetRouterInstanceByID(ctx context.Context, id uuid.UUID) (*store.RouterInstance, error) {
	var item store.RouterInstance
	err := s.pool.QueryRow(ctx, `SELECT id, code, public_base_url, internal_base_url, status, encrypted_api_key, health_status, last_health_check_at, metadata, created_at FROM ai_gateway.router_instances WHERE id = $1 LIMIT 1`, id).Scan(&item.ID, &item.Code, &item.PublicBaseURL, &item.InternalBaseURL, &item.Status, &item.EncryptedAPIKey, &item.HealthStatus, &item.LastHealthCheckAt, &item.Metadata, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) UpdateRouterInstance(ctx context.Context, item store.RouterInstance) (store.RouterInstance, error) {
	err := s.pool.QueryRow(ctx, `
		UPDATE ai_gateway.router_instances
		SET code = $2,
			public_base_url = $3,
			internal_base_url = $4,
			status = $5,
			encrypted_api_key = $6,
			health_status = $7,
			last_health_check_at = $8,
			metadata = $9
		WHERE id = $1
		RETURNING id, code, public_base_url, internal_base_url, status, encrypted_api_key, health_status, last_health_check_at, metadata, created_at`,
		item.ID, item.Code, item.PublicBaseURL, item.InternalBaseURL, defaultString(item.Status, "active"), item.EncryptedAPIKey, item.HealthStatus, item.LastHealthCheckAt, rawJSON(item.Metadata),
	).Scan(&item.ID, &item.Code, &item.PublicBaseURL, &item.InternalBaseURL, &item.Status, &item.EncryptedAPIKey, &item.HealthStatus, &item.LastHealthCheckAt, &item.Metadata, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.RouterInstance{}, ErrNotFound
	}
	return item, err
}

func (s *PostgresStore) DeleteRouterInstance(ctx context.Context, id uuid.UUID) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM ai_gateway.router_instances WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListRouterInstances(ctx context.Context) []store.RouterInstance {
	rows, err := s.pool.Query(ctx, `SELECT id, code, public_base_url, internal_base_url, status, encrypted_api_key, health_status, last_health_check_at, metadata, created_at FROM ai_gateway.router_instances ORDER BY code ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.RouterInstance{}
	for rows.Next() {
		var item store.RouterInstance
		if scanRouter(rows, &item) == nil {
			items = append(items, item)
		}
	}
	return items
}

func (s *PostgresStore) GetRouterInstance(ctx context.Context, code string) (*store.RouterInstance, error) {
	var item store.RouterInstance
	err := s.pool.QueryRow(ctx, `SELECT id, code, public_base_url, internal_base_url, status, encrypted_api_key, health_status, last_health_check_at, metadata, created_at FROM ai_gateway.router_instances WHERE code = $1 LIMIT 1`, code).Scan(&item.ID, &item.Code, &item.PublicBaseURL, &item.InternalBaseURL, &item.Status, &item.EncryptedAPIKey, &item.HealthStatus, &item.LastHealthCheckAt, &item.Metadata, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) SumUsageTokensByUserSince(ctx context.Context, userID string, since time.Time) int64 {
	var total int64
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(total_tokens), 0)::bigint FROM ai_gateway.usage_ledger WHERE genfity_user_id = $1 AND status = 'success' AND started_at >= $2`, userID, since).Scan(&total)
	return total
}

func (s *PostgresStore) ReserveQuotaTokens(ctx context.Context, userID string, tenantID *string, periodStart time.Time, periodEnd time.Time, tokens int64, limit int64) error {
	if tokens <= 0 || limit <= 0 {
		return nil
	}
	if tokens > limit {
		return ErrQuotaExceeded
	}
	cmd, err := s.pool.Exec(ctx, `
		INSERT INTO ai_gateway.quota_counters (genfity_user_id, genfity_tenant_id, period_start, period_end, tokens_reserved, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (genfity_user_id, period_start, period_end)
		DO UPDATE SET tokens_reserved = ai_gateway.quota_counters.tokens_reserved + EXCLUDED.tokens_reserved,
			updated_at = now()
		WHERE ai_gateway.quota_counters.tokens_used + ai_gateway.quota_counters.tokens_reserved + EXCLUDED.tokens_reserved <= $6`,
		userID, tenantID, periodStart, periodEnd, tokens, limit)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrQuotaExceeded
	}
	return nil
}

func (s *PostgresStore) FinalizeQuotaTokens(ctx context.Context, userID string, periodStart time.Time, periodEnd time.Time, reservedTokens int64, usedTokens int64, countRequest bool) error {
	if reservedTokens <= 0 && usedTokens <= 0 && !countRequest {
		return nil
	}
	requestCount := int64(0)
	if countRequest {
		requestCount = 1
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE ai_gateway.quota_counters
		SET tokens_reserved = GREATEST(tokens_reserved - $4, 0),
			tokens_used = tokens_used + $5,
			request_count = request_count + $6,
			updated_at = now()
		WHERE genfity_user_id = $1 AND period_start = $2 AND period_end = $3`,
		userID, periodStart, periodEnd, reservedTokens, usedTokens, requestCount)
	return err
}

func (s *PostgresStore) ReserveCreditBalance(ctx context.Context, userID string, planCode string, amountUsd float64) error {
	if amountUsd <= 0 {
		return nil
	}
	cmd, err := s.pool.Exec(ctx,
		`UPDATE ai_gateway.customer_entitlements
			 SET balance_reserved = COALESCE(balance_reserved, 0) + $3,
			     updated_at = now()
			 WHERE genfity_user_id = $1
			   AND plan_code = $2
			   AND metadata->>'pricingGroup' = 'credit_package'
			   AND status = 'active'
			   AND COALESCE(balance_snapshot, 0) - COALESCE(balance_reserved, 0) >= $3`,
		userID, planCode, amountUsd)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrInsufficientBalance
	}
	return nil
}

// FinalizeCreditBalance releases the reservation and debits the actual cost.
// If actualUsd exceeds reservedUsd (cost overrun), the debit is capped at the
// available balance — the entitlement balance can never go negative. Any
// remaining shortfall is silently absorbed by the platform; the caller should
// log overruns separately if they need to chase them.
func (s *PostgresStore) FinalizeCreditBalance(ctx context.Context, userID string, planCode string, reservedUsd float64, actualUsd float64) error {
	if reservedUsd <= 0 && actualUsd <= 0 {
		return nil
	}
	if actualUsd < 0 {
		actualUsd = 0
	}
	if reservedUsd < 0 {
		reservedUsd = 0
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE ai_gateway.customer_entitlements
			 SET balance_reserved = GREATEST(COALESCE(balance_reserved, 0) - $3, 0),
			     balance_snapshot = GREATEST(COALESCE(balance_snapshot, 0) - LEAST($4, COALESCE(balance_snapshot, 0)), 0),
			     updated_at = now()
			 WHERE genfity_user_id = $1
			   AND plan_code = $2
			   AND metadata->>'pricingGroup' = 'credit_package'
			   AND status = 'active'`,
		userID, planCode, reservedUsd, actualUsd)
	return err
}

func (s *PostgresStore) IncrementQuotaCounter(ctx context.Context, userID string, tenantID *string, periodStart time.Time, periodEnd time.Time, tokens int64) error {
	if tokens <= 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ai_gateway.quota_counters (genfity_user_id, genfity_tenant_id, period_start, period_end, tokens_used, request_count, updated_at)
		VALUES ($1, $2, $3, $4, $5, 1, now())
		ON CONFLICT (genfity_user_id, period_start, period_end)
		DO UPDATE SET tokens_used = ai_gateway.quota_counters.tokens_used + EXCLUDED.tokens_used,
			request_count = ai_gateway.quota_counters.request_count + 1,
			updated_at = now()`, userID, tenantID, periodStart, periodEnd, tokens)
	return err
}

func (s *PostgresStore) DebitCreditBalance(ctx context.Context, userID string, planCode string, debitUsd float64) error {
	cmd, err := s.pool.Exec(ctx,
		`UPDATE ai_gateway.customer_entitlements
			 SET balance_snapshot = balance_snapshot - $3,
			     updated_at = now()
			 WHERE genfity_user_id = $1
			   AND plan_code = $2
			   AND metadata->>'pricingGroup' = 'credit_package'
			   AND status = 'active'
			   AND COALESCE(balance_snapshot, 0) >= $3`,
		userID, planCode, debitUsd)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) AppendUsage(ctx context.Context, item store.UsageLedgerEntry) (store.UsageLedgerEntry, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.usage_ledger (id, request_id, genfity_user_id, genfity_tenant_id, api_key_id, public_model, router_model, router_instance_code, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, input_cost, output_cost, total_cost, status, error_code, latency_ms, started_at, finished_at, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		RETURNING id, request_id, genfity_user_id, genfity_tenant_id, api_key_id, public_model, router_model, router_instance_code, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, input_cost::text, output_cost::text, total_cost::text, status, error_code, latency_ms, started_at, finished_at, metadata`,
		nilUUID(item.ID), item.RequestID, item.GenfityUserID, item.GenfityTenantID, item.APIKeyID, item.PublicModel, item.RouterModel, item.RouterInstanceCode, item.PromptTokens, item.CompletionTokens, item.TotalTokens, item.CachedTokens, item.ReasoningTokens, item.InputCost, item.OutputCost, item.TotalCost, item.Status, item.ErrorCode, item.LatencyMS, defaultTime(item.StartedAt), item.FinishedAt, rawJSON(item.Metadata),
	).Scan(&item.ID, &item.RequestID, &item.GenfityUserID, &item.GenfityTenantID, &item.APIKeyID, &item.PublicModel, &item.RouterModel, &item.RouterInstanceCode, &item.PromptTokens, &item.CompletionTokens, &item.TotalTokens, &item.CachedTokens, &item.ReasoningTokens, &item.InputCost, &item.OutputCost, &item.TotalCost, &item.Status, &item.ErrorCode, &item.LatencyMS, &item.StartedAt, &item.FinishedAt, &item.Metadata)
	if err != nil {
		return store.UsageLedgerEntry{}, err
	}
	return item, nil
}

func (s *PostgresStore) ListUsage(ctx context.Context) []store.UsageLedgerEntry {
	return s.listUsage(ctx, `SELECT id, request_id, genfity_user_id, genfity_tenant_id, api_key_id, public_model, router_model, router_instance_code, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, input_cost::text, output_cost::text, total_cost::text, status, error_code, latency_ms, started_at, finished_at, metadata FROM ai_gateway.usage_ledger ORDER BY started_at DESC`)
}

func (s *PostgresStore) ListAllUsage(ctx context.Context, limit int) []store.UsageLedgerEntry {
	if limit <= 0 {
		limit = 100
	}
	return s.listUsage(ctx, `SELECT id, request_id, genfity_user_id, genfity_tenant_id, api_key_id, public_model, router_model, router_instance_code, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, input_cost::text, output_cost::text, total_cost::text, status, error_code, latency_ms, started_at, finished_at, metadata FROM ai_gateway.usage_ledger ORDER BY started_at DESC LIMIT `, limit)
}

func (s *PostgresStore) ListUsageByUser(ctx context.Context, userID string) []store.UsageLedgerEntry {
	return s.listUsage(ctx, `SELECT id, request_id, genfity_user_id, genfity_tenant_id, api_key_id, public_model, router_model, router_instance_code, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, input_cost::text, output_cost::text, total_cost::text, status, error_code, latency_ms, started_at, finished_at, metadata FROM ai_gateway.usage_ledger WHERE genfity_user_id = $1 ORDER BY started_at DESC`, userID)
}

func (s *PostgresStore) ListUsageByUserSince(ctx context.Context, userID string, since time.Time) []store.UsageLedgerEntry {
	return s.listUsage(ctx, `SELECT id, request_id, genfity_user_id, genfity_tenant_id, api_key_id, public_model, router_model, router_instance_code, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, input_cost::text, output_cost::text, total_cost::text, status, error_code, latency_ms, started_at, finished_at, metadata FROM ai_gateway.usage_ledger WHERE genfity_user_id = $1 AND started_at >= $2 ORDER BY started_at DESC`, userID, since)
}

func (s *PostgresStore) ListUsageByTenant(ctx context.Context, tenantID string) []store.UsageLedgerEntry {
	return s.listUsage(ctx, `SELECT id, request_id, genfity_user_id, genfity_tenant_id, api_key_id, public_model, router_model, router_instance_code, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, input_cost::text, output_cost::text, total_cost::text, status, error_code, latency_ms, started_at, finished_at, metadata FROM ai_gateway.usage_ledger WHERE genfity_tenant_id = $1 ORDER BY started_at DESC`, tenantID)
}

func (s *PostgresStore) listUsage(ctx context.Context, sql string, args ...any) []store.UsageLedgerEntry {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.UsageLedgerEntry{}
	for rows.Next() {
		var item store.UsageLedgerEntry
		if rows.Scan(&item.ID, &item.RequestID, &item.GenfityUserID, &item.GenfityTenantID, &item.APIKeyID, &item.PublicModel, &item.RouterModel, &item.RouterInstanceCode, &item.PromptTokens, &item.CompletionTokens, &item.TotalTokens, &item.CachedTokens, &item.ReasoningTokens, &item.InputCost, &item.OutputCost, &item.TotalCost, &item.Status, &item.ErrorCode, &item.LatencyMS, &item.StartedAt, &item.FinishedAt, &item.Metadata) == nil {
			items = append(items, item)
		}
	}
	return items
}

func scanAPIKey(row pgx.Row, item *store.APIKey) error {
	return row.Scan(&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.Name, &item.KeyPrefix, &item.KeyHash, &item.Status, &item.LastUsedAt, &item.ExpiresAt, &item.CreatedAt, &item.RevokedAt)
}

func scanModel(row pgx.Row, item *store.AIModel) error {
	return row.Scan(&item.ID, &item.PublicModel, &item.DisplayName, &item.Description, &item.Status, &item.ContextWindow, &item.SupportsStreaming, &item.SupportsTools, &item.SupportsVision, &item.CreatedAt, &item.UpdatedAt)
}

func scanRoute(row pgx.Row, item *store.AIModelRoute) error {
	return row.Scan(&item.ID, &item.ModelID, &item.RouterInstanceCode, &item.RouterModel, &item.Status, &item.Metadata, &item.CreatedAt)
}

func scanRouter(row pgx.Row, item *store.RouterInstance) error {
	return row.Scan(&item.ID, &item.Code, &item.PublicBaseURL, &item.InternalBaseURL, &item.Status, &item.EncryptedAPIKey, &item.HealthStatus, &item.LastHealthCheckAt, &item.Metadata, &item.CreatedAt)
}

func rawJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func defaultTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value
}

func nilUUID(value uuid.UUID) any {
	if value == uuid.Nil {
		return pgtype.UUID{Valid: false}
	}
	return value
}

// --- VirtualCombo methods ---

func (s *PostgresStore) UpsertVirtualCombo(ctx context.Context, combo store.VirtualCombo) store.VirtualCombo {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ai_gateway.virtual_combos (id, model_id, name, description, status, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			status = EXCLUDED.status,
			metadata = EXCLUDED.metadata,
			updated_at = now()`,
		combo.ID, combo.ModelID, combo.Name, combo.Description,
		defaultString(combo.Status, "active"), rawJSON(combo.Metadata),
	)
	if err != nil {
		return combo
	}
	_, _ = s.pool.Exec(ctx, `DELETE FROM ai_gateway.virtual_combo_entries WHERE combo_id = $1`, combo.ID)
	for _, e := range combo.Entries {
		entryID := e.ID
		if entryID == uuid.Nil {
			entryID = uuid.New()
		}
		triggerJSON, _ := json.Marshal(e.TriggerOn)
		_, _ = s.pool.Exec(ctx, `
			INSERT INTO ai_gateway.virtual_combo_entries
			(id, combo_id, priority, router_instance_code, router_model, trigger_on, metadata)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			entryID, combo.ID, e.Priority, e.RouterInstanceCode, e.RouterModel, triggerJSON, rawJSON(e.Metadata),
		)
	}
	return combo
}

func (s *PostgresStore) ListVirtualCombos(ctx context.Context) []store.VirtualCombo {
	rows, err := s.pool.Query(ctx, `SELECT id, model_id, name, description, status, metadata, created_at, updated_at FROM ai_gateway.virtual_combos ORDER BY name ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var combos []store.VirtualCombo
	for rows.Next() {
		var c store.VirtualCombo
		if err := rows.Scan(&c.ID, &c.ModelID, &c.Name, &c.Description, &c.Status, &c.Metadata, &c.CreatedAt, &c.UpdatedAt); err == nil {
			c.Entries = s.loadComboEntries(ctx, c.ID)
			combos = append(combos, c)
		}
	}
	return combos
}

func (s *PostgresStore) GetVirtualComboByID(ctx context.Context, id uuid.UUID) (*store.VirtualCombo, error) {
	var c store.VirtualCombo
	err := s.pool.QueryRow(ctx, `SELECT id, model_id, name, description, status, metadata, created_at, updated_at FROM ai_gateway.virtual_combos WHERE id = $1`, id).Scan(&c.ID, &c.ModelID, &c.Name, &c.Description, &c.Status, &c.Metadata, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	c.Entries = s.loadComboEntries(ctx, c.ID)
	return &c, nil
}

func (s *PostgresStore) DeleteVirtualCombo(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM ai_gateway.virtual_combos WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) GetVirtualComboByModelID(ctx context.Context, modelID uuid.UUID) (*store.VirtualCombo, error) {
	var c store.VirtualCombo
	err := s.pool.QueryRow(ctx, `SELECT id, model_id, name, description, status, metadata, created_at, updated_at FROM ai_gateway.virtual_combos WHERE model_id = $1 AND status = 'active' LIMIT 1`, modelID).Scan(&c.ID, &c.ModelID, &c.Name, &c.Description, &c.Status, &c.Metadata, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	c.Entries = s.loadComboEntries(ctx, c.ID)
	return &c, nil
}

func (s *PostgresStore) loadComboEntries(ctx context.Context, comboID uuid.UUID) []store.VirtualComboEntry {
	rows, err := s.pool.Query(ctx, `SELECT id, combo_id, priority, router_instance_code, router_model, trigger_on, metadata, created_at FROM ai_gateway.virtual_combo_entries WHERE combo_id = $1 ORDER BY priority ASC`, comboID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var entries []store.VirtualComboEntry
	for rows.Next() {
		var e store.VirtualComboEntry
		var triggerJSON []byte
		if err := rows.Scan(&e.ID, &e.ComboID, &e.Priority, &e.RouterInstanceCode, &e.RouterModel, &triggerJSON, &e.Metadata, &e.CreatedAt); err == nil {
			_ = json.Unmarshal(triggerJSON, &e.TriggerOn)
			entries = append(entries, e)
		}
	}
	return entries
}
