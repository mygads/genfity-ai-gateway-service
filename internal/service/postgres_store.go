package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
			max_requests_per_period, rate_limit_rpd,
			metadata, synced_from_genfity_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (plan_code) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			status = EXCLUDED.status,
			monthly_price = EXCLUDED.monthly_price,
			currency = EXCLUDED.currency,
			quota_tokens_monthly = EXCLUDED.quota_tokens_monthly,
			rate_limit_rpm = EXCLUDED.rate_limit_rpm,
			rate_limit_tpm = EXCLUDED.rate_limit_tpm,
			concurrent_limit = EXCLUDED.concurrent_limit,
			max_requests_per_period = EXCLUDED.max_requests_per_period,
			rate_limit_rpd = EXCLUDED.rate_limit_rpd,
			metadata = EXCLUDED.metadata,
			synced_from_genfity_at = EXCLUDED.synced_from_genfity_at,
			updated_at = now()
		RETURNING id, plan_code, display_name, status, monthly_price::text, currency,
			quota_tokens_monthly, rate_limit_rpm, rate_limit_tpm, concurrent_limit,
			max_requests_per_period, rate_limit_rpd,
			metadata, synced_from_genfity_at, created_at, updated_at`,
		nilUUID(item.ID), item.PlanCode, item.DisplayName, defaultString(item.Status, "active"), item.MonthlyPrice,
		defaultString(item.Currency, "IDR"), item.QuotaTokensMonthly, item.RateLimitRPM, item.RateLimitTPM,
		item.ConcurrentLimit, item.MaxRequestsPerPeriod, item.RateLimitRPD, metadata, defaultTime(item.SyncedFromGenfityAt),
	).Scan(&item.ID, &item.PlanCode, &item.DisplayName, &item.Status, &item.MonthlyPrice, &item.Currency,
		&item.QuotaTokensMonthly, &item.RateLimitRPM, &item.RateLimitTPM, &item.ConcurrentLimit,
		&item.MaxRequestsPerPeriod, &item.RateLimitRPD,
		&item.Metadata, &item.SyncedFromGenfityAt, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return store.SubscriptionPlanSnapshot{}, err
	}
	return item, nil
}

func (s *PostgresStore) ListPlans(ctx context.Context) []store.SubscriptionPlanSnapshot {
	rows, err := s.pool.Query(ctx, `SELECT id, plan_code, display_name, status, monthly_price::text, currency, quota_tokens_monthly, rate_limit_rpm, rate_limit_tpm, concurrent_limit, max_requests_per_period, rate_limit_rpd, metadata, synced_from_genfity_at, created_at, updated_at FROM ai_gateway.subscription_plan_snapshots ORDER BY plan_code ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.SubscriptionPlanSnapshot{}
	for rows.Next() {
		var item store.SubscriptionPlanSnapshot
		if rows.Scan(&item.ID, &item.PlanCode, &item.DisplayName, &item.Status, &item.MonthlyPrice, &item.Currency, &item.QuotaTokensMonthly, &item.RateLimitRPM, &item.RateLimitTPM, &item.ConcurrentLimit, &item.MaxRequestsPerPeriod, &item.RateLimitRPD, &item.Metadata, &item.SyncedFromGenfityAt, &item.CreatedAt, &item.UpdatedAt) == nil {
			items = append(items, item)
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return items
}

func (s *PostgresStore) GetPlanByCode(ctx context.Context, planCode string) (*store.SubscriptionPlanSnapshot, error) {
	var item store.SubscriptionPlanSnapshot
	err := s.pool.QueryRow(ctx, `SELECT id, plan_code, display_name, status, monthly_price::text, currency, quota_tokens_monthly, rate_limit_rpm, rate_limit_tpm, concurrent_limit, max_requests_per_period, rate_limit_rpd, metadata, synced_from_genfity_at, created_at, updated_at FROM ai_gateway.subscription_plan_snapshots WHERE plan_code = $1 LIMIT 1`, planCode).Scan(
		&item.ID, &item.PlanCode, &item.DisplayName, &item.Status, &item.MonthlyPrice, &item.Currency,
		&item.QuotaTokensMonthly, &item.RateLimitRPM, &item.RateLimitTPM, &item.ConcurrentLimit,
		&item.MaxRequestsPerPeriod, &item.RateLimitRPD,
		&item.Metadata, &item.SyncedFromGenfityAt, &item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) UpsertAPIKey(ctx context.Context, item store.APIKey) (store.APIKey, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.api_keys (id, genfity_user_id, genfity_tenant_id, name, key_prefix, key_hash, status, billing_source, last_used_at, expires_at, created_at, regenerated_at, revoked_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			key_prefix = EXCLUDED.key_prefix,
			key_hash = EXCLUDED.key_hash,
			status = EXCLUDED.status,
			billing_source = EXCLUDED.billing_source,
			last_used_at = EXCLUDED.last_used_at,
			expires_at = EXCLUDED.expires_at,
			regenerated_at = EXCLUDED.regenerated_at,
			revoked_at = EXCLUDED.revoked_at
		RETURNING id, genfity_user_id, genfity_tenant_id, name, key_prefix, key_hash, status, billing_source, last_used_at, expires_at, created_at, regenerated_at, revoked_at`,
		nilUUID(item.ID), item.GenfityUserID, item.GenfityTenantID, item.Name, item.KeyPrefix, item.KeyHash,
		defaultString(item.Status, "active"), defaultString(item.BillingSource, "subscription"),
		item.LastUsedAt, item.ExpiresAt, defaultTime(item.CreatedAt), item.RegeneratedAt, item.RevokedAt,
	).Scan(&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.Name, &item.KeyPrefix, &item.KeyHash, &item.Status, &item.BillingSource, &item.LastUsedAt, &item.ExpiresAt, &item.CreatedAt, &item.RegeneratedAt, &item.RevokedAt)
	if err != nil {
		return store.APIKey{}, err
	}
	return item, nil
}

func (s *PostgresStore) ListAPIKeysByUser(ctx context.Context, userID string) []store.APIKey {
	rows, err := s.pool.Query(ctx, `SELECT id, genfity_user_id, genfity_tenant_id, name, key_prefix, key_hash, status, billing_source, last_used_at, expires_at, created_at, regenerated_at, revoked_at FROM ai_gateway.api_keys WHERE genfity_user_id = $1 ORDER BY created_at DESC`, userID)
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
	if rows.Err() != nil {
		return nil
	}
	return items
}

func (s *PostgresStore) FindAPIKeyByPrefix(ctx context.Context, prefix string) (*store.APIKey, error) {
	var item store.APIKey
	err := s.pool.QueryRow(ctx, `SELECT id, genfity_user_id, genfity_tenant_id, name, key_prefix, key_hash, status, billing_source, last_used_at, expires_at, created_at, regenerated_at, revoked_at FROM ai_gateway.api_keys WHERE key_prefix = $1 LIMIT 1`, prefix).Scan(&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.Name, &item.KeyPrefix, &item.KeyHash, &item.Status, &item.BillingSource, &item.LastUsedAt, &item.ExpiresAt, &item.CreatedAt, &item.RegeneratedAt, &item.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) GetAPIKeyByID(ctx context.Context, id uuid.UUID) (*store.APIKey, error) {
	var item store.APIKey
	err := s.pool.QueryRow(ctx, `SELECT id, genfity_user_id, genfity_tenant_id, name, key_prefix, key_hash, status, billing_source, last_used_at, expires_at, created_at, regenerated_at, revoked_at FROM ai_gateway.api_keys WHERE id = $1 LIMIT 1`, id).Scan(&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.Name, &item.KeyPrefix, &item.KeyHash, &item.Status, &item.BillingSource, &item.LastUsedAt, &item.ExpiresAt, &item.CreatedAt, &item.RegeneratedAt, &item.RevokedAt)
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

func (s *PostgresStore) UpdateAPIKeyBillingSource(ctx context.Context, id uuid.UUID, source string) error {
	cmd, err := s.pool.Exec(ctx, `UPDATE ai_gateway.api_keys SET billing_source = $2 WHERE id = $1`, id, source)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) UpdateAPIKeyLastUsedAt(ctx context.Context, id uuid.UUID, ts time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE ai_gateway.api_keys SET last_used_at = $2 WHERE id = $1`, id, ts)
	return err
}

func (s *PostgresStore) UpsertModel(ctx context.Context, item store.AIModel) (store.AIModel, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.ai_models (id, public_model, display_name, description, status, model_type, context_window, supports_streaming, supports_tools, supports_vision, payg_exposed, is_free, free_limit_rpd, free_limit_rpm, free_limit_tpd)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (public_model) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			description = EXCLUDED.description,
			status = EXCLUDED.status,
			model_type = EXCLUDED.model_type,
			context_window = EXCLUDED.context_window,
			supports_streaming = EXCLUDED.supports_streaming,
			supports_tools = EXCLUDED.supports_tools,
			supports_vision = EXCLUDED.supports_vision,
			payg_exposed = EXCLUDED.payg_exposed,
			is_free = EXCLUDED.is_free,
			free_limit_rpd = EXCLUDED.free_limit_rpd,
			free_limit_rpm = EXCLUDED.free_limit_rpm,
			free_limit_tpd = EXCLUDED.free_limit_tpd,
			updated_at = now()
		RETURNING id, public_model, display_name, description, status, model_type, context_window, supports_streaming, supports_tools, supports_vision, payg_exposed, is_free, free_limit_rpd, free_limit_rpm, free_limit_tpd, created_at, updated_at`,
		nilUUID(item.ID), item.PublicModel, item.DisplayName, item.Description, defaultString(item.Status, "active"), defaultString(item.ModelType, "text"), item.ContextWindow, item.SupportsStreaming, item.SupportsTools, item.SupportsVision, item.PaygExposed, item.IsFree, item.FreeLimitRPD, item.FreeLimitRPM, item.FreeLimitTPD,
	).Scan(&item.ID, &item.PublicModel, &item.DisplayName, &item.Description, &item.Status, &item.ModelType, &item.ContextWindow, &item.SupportsStreaming, &item.SupportsTools, &item.SupportsVision, &item.PaygExposed, &item.IsFree, &item.FreeLimitRPD, &item.FreeLimitRPM, &item.FreeLimitTPD, &item.CreatedAt, &item.UpdatedAt)
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
			model_type = $6,
			context_window = $7,
			supports_streaming = $8,
			supports_tools = $9,
			supports_vision = $10,
			payg_exposed = $11,
			is_free = $12,
			free_limit_rpd = $13,
			free_limit_rpm = $14,
			free_limit_tpd = $15,
			updated_at = now()
		WHERE id = $1
		RETURNING id, public_model, display_name, description, status, model_type, context_window, supports_streaming, supports_tools, supports_vision, payg_exposed, is_free, free_limit_rpd, free_limit_rpm, free_limit_tpd, created_at, updated_at`,
		item.ID, item.PublicModel, item.DisplayName, item.Description, defaultString(item.Status, "active"), defaultString(item.ModelType, "text"), item.ContextWindow, item.SupportsStreaming, item.SupportsTools, item.SupportsVision, item.PaygExposed, item.IsFree, item.FreeLimitRPD, item.FreeLimitRPM, item.FreeLimitTPD,
	).Scan(&item.ID, &item.PublicModel, &item.DisplayName, &item.Description, &item.Status, &item.ModelType, &item.ContextWindow, &item.SupportsStreaming, &item.SupportsTools, &item.SupportsVision, &item.PaygExposed, &item.IsFree, &item.FreeLimitRPD, &item.FreeLimitRPM, &item.FreeLimitTPD, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.AIModel{}, ErrNotFound
	}
	return item, err
}

func (s *PostgresStore) UpdateModelStatus(ctx context.Context, id uuid.UUID, status string) error {
	cmd, err := s.pool.Exec(ctx, `UPDATE ai_gateway.ai_models SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListAllModels(ctx context.Context) []store.AIModel {
	rows, err := s.pool.Query(ctx, `SELECT id, public_model, display_name, description, status, model_type, context_window, supports_streaming, supports_tools, supports_vision, payg_exposed, is_free, free_limit_rpd, free_limit_rpm, free_limit_tpd, created_at, updated_at FROM ai_gateway.ai_models ORDER BY public_model ASC`)
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
	if rows.Err() != nil {
		return nil
	}
	return items
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
	rows, err := s.pool.Query(ctx, `SELECT id, public_model, display_name, description, status, model_type, context_window, supports_streaming, supports_tools, supports_vision, payg_exposed, is_free, free_limit_rpd, free_limit_rpm, free_limit_tpd, created_at, updated_at FROM ai_gateway.ai_models ORDER BY display_name ASC`)
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
	if rows.Err() != nil {
		return nil
	}
	return items
}

func (s *PostgresStore) GetModelByID(ctx context.Context, id uuid.UUID) (*store.AIModel, error) {
	var item store.AIModel
	err := s.pool.QueryRow(ctx, `SELECT id, public_model, display_name, description, status, model_type, context_window, supports_streaming, supports_tools, supports_vision, payg_exposed, is_free, free_limit_rpd, free_limit_rpm, free_limit_tpd, created_at, updated_at FROM ai_gateway.ai_models WHERE id = $1 LIMIT 1`, id).Scan(&item.ID, &item.PublicModel, &item.DisplayName, &item.Description, &item.Status, &item.ModelType, &item.ContextWindow, &item.SupportsStreaming, &item.SupportsTools, &item.SupportsVision, &item.PaygExposed, &item.IsFree, &item.FreeLimitRPD, &item.FreeLimitRPM, &item.FreeLimitTPD, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &item, err
}

func (s *PostgresStore) GetModelByPublicName(ctx context.Context, publicModel string) (*store.AIModel, error) {
	var item store.AIModel
	err := s.pool.QueryRow(ctx, `SELECT id, public_model, display_name, description, status, model_type, context_window, supports_streaming, supports_tools, supports_vision, payg_exposed, is_free, free_limit_rpd, free_limit_rpm, free_limit_tpd, created_at, updated_at FROM ai_gateway.ai_models WHERE public_model = $1 LIMIT 1`, publicModel).Scan(&item.ID, &item.PublicModel, &item.DisplayName, &item.Description, &item.Status, &item.ModelType, &item.ContextWindow, &item.SupportsStreaming, &item.SupportsTools, &item.SupportsVision, &item.PaygExposed, &item.IsFree, &item.FreeLimitRPD, &item.FreeLimitRPM, &item.FreeLimitTPD, &item.CreatedAt, &item.UpdatedAt)
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
	if rows.Err() != nil {
		return nil
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
	// PRD v3 Phase 4 — credit_package and payg_topup are upserted as
	// SINGLE active row per user, keyed on (genfity_user_id, status,
	// pricing_group) via partial unique indexes (migration 00018).
	//
	// Why split paths instead of a single ON CONFLICT clause: postgres
	// requires the ON CONFLICT target to be a single index. Subscription
	// rows (unlimited_plan etc.) still use the legacy
	// (genfity_user_id, plan_code) unique key so the same user can hold
	// multiple subscription rows for history (active + replaced). Credit
	// and PAYG rows must NOT — they would split the user's balance and
	// non-deterministically debit one row while the app tracked the
	// other. So we route by pricing_group:
	//
	//   pricing_group='credit_package' or 'payg_topup' → upsert by
	//     (genfity_user_id) WHERE pricing_group=X AND status='active'
	//     This lets a top-up that switches plan_code (starter→developer)
	//     stack onto the existing single credit row instead of creating
	//     a parallel row.
	//
	//   anything else (subscription / unlimited_plan) → upsert by
	//     (genfity_user_id, plan_code), preserving legacy behavior so
	//     an unlimited renewal updates in place and a replacement
	//     leaves the old row at status='replaced'.
	pg := ""
	if item.PricingGroup != nil {
		pg = strings.TrimSpace(*item.PricingGroup)
	}
	status := strings.TrimSpace(defaultString(item.Status, "active"))
	if status == "active" && (pg == "credit_package" || pg == "payg_topup") {
		return s.upsertSingleRowEntitlement(ctx, item, pg)
	}
	return s.upsertEntitlementByPlanCode(ctx, item)
}

// upsertSingleRowEntitlement enforces the "one active row" invariant
// for credit_package and payg_topup by checking for an existing active
// row first and updating it (even when plan_code changed) instead of
// inserting a new one. The unique partial index from migration 00018
// is the safety net — if a race between two concurrent upserts both
// pass the SELECT, one INSERT will fail with 23505 and we retry the
// update path.
func (s *PostgresStore) upsertSingleRowEntitlement(ctx context.Context, item store.CustomerEntitlement, pricingGroup string) (store.CustomerEntitlement, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.CustomerEntitlement{}, err
	}
	defer tx.Rollback(ctx)

	// Free the target plan_code slot before INSERT/UPDATE. The legacy
	// customer_entitlements_user_plan_idx UNIQUE (genfity_user_id,
	// plan_code) spans ALL statuses, so a terminal-status row that still
	// holds the incoming plan_code (e.g. a 'consolidated' credit row left
	// by migration 00018, or an 'expired'/'replaced' row) collides when we
	// (a) INSERT a fresh single credit/PAYG row with that plan_code, or
	// (b) rename the existing active row's plan_code to it (starter →
	// developer top-up). That 23505 used to abort the whole sync batch and
	// stamp every unrelated row in the batch as failed. These terminal
	// rows carry zero balance (consolidation zeroes them) and are
	// audit-only, so deleting them is safe — and we never touch 'active'
	// rows, so the user's live balance row is preserved.
	if item.PlanCode != "" {
		if _, err := tx.Exec(ctx,
			`DELETE FROM ai_gateway.customer_entitlements
			 WHERE genfity_user_id = $1 AND plan_code = $2 AND status <> 'active'`,
			item.GenfityUserID, item.PlanCode); err != nil {
			return store.CustomerEntitlement{}, err
		}
	}

	var existingID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id FROM ai_gateway.customer_entitlements
		WHERE genfity_user_id = $1
		  AND status = 'active'
		  AND pricing_group = $2
		FOR UPDATE`,
		item.GenfityUserID, pricingGroup,
	).Scan(&existingID)

	var metadata json.RawMessage
	if errors.Is(err, pgx.ErrNoRows) {
		// No existing active row — insert. The partial unique index
		// guards against a concurrent insert; if it triggers we fall
		// through to the update path below.
		err := tx.QueryRow(ctx, `
			INSERT INTO ai_gateway.customer_entitlements (
				id, genfity_user_id, genfity_tenant_id, plan_code, status,
				period_start, period_end, quota_tokens_monthly, balance_snapshot,
				credit_balance, credit_expires_at, payg_usd_balance, pricing_group,
				metadata, updated_from_genfity_at
			)
			VALUES ($1,$2,$3,$4,'active',$5,$6,$7,$8,COALESCE($9::numeric,0),$10,COALESCE($11::numeric,0),$12,$13,$14)
			RETURNING id, genfity_user_id, genfity_tenant_id, plan_code, status,
				period_start, period_end, quota_tokens_monthly, balance_snapshot::text,
				credit_balance::text, credit_expires_at, payg_usd_balance::text,
				pricing_group, metadata, updated_from_genfity_at`,
			nilUUID(item.ID), item.GenfityUserID, item.GenfityTenantID, item.PlanCode,
			item.PeriodStart, item.PeriodEnd, item.QuotaTokensMonthly, item.BalanceSnapshot,
			item.CreditBalance, item.CreditExpiresAt, item.PaygUsdBalance, &pricingGroup,
			rawJSON(item.Metadata), defaultTime(item.UpdatedFromGenfityAt),
		).Scan(
			&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.PlanCode, &item.Status,
			&item.PeriodStart, &item.PeriodEnd, &item.QuotaTokensMonthly, &item.BalanceSnapshot,
			&item.CreditBalance, &item.CreditExpiresAt, &item.PaygUsdBalance, &item.PricingGroup,
			&metadata, &item.UpdatedFromGenfityAt,
		)
		if err != nil {
			return store.CustomerEntitlement{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return store.CustomerEntitlement{}, err
		}
		item.Metadata = metadata
		return item, nil
	}
	if err != nil {
		return store.CustomerEntitlement{}, err
	}

	// Existing active row found — UPDATE in place. plan_code is allowed
	// to change (starter → developer top-up) so the user's current plan
	// label always reflects the latest top-up. Reserved field is left
	// alone (in-flight reservations belong to gateway runtime, not sync).
	err = tx.QueryRow(ctx, `
		UPDATE ai_gateway.customer_entitlements SET
			genfity_tenant_id = $2,
			plan_code = $3,
			period_start = $4,
			period_end = $5,
			quota_tokens_monthly = $6,
			balance_snapshot = $7,
			credit_balance = COALESCE($8::numeric, credit_balance),
			credit_expires_at = $9,
			payg_usd_balance = COALESCE($10::numeric, payg_usd_balance),
			pricing_group = $11,
			metadata = $12,
			updated_from_genfity_at = $13,
			updated_at = now()
		WHERE id = $1
		RETURNING id, genfity_user_id, genfity_tenant_id, plan_code, status,
			period_start, period_end, quota_tokens_monthly, balance_snapshot::text,
			credit_balance::text, credit_expires_at, payg_usd_balance::text,
			pricing_group, metadata, updated_from_genfity_at`,
		existingID, item.GenfityTenantID, item.PlanCode,
		item.PeriodStart, item.PeriodEnd, item.QuotaTokensMonthly, item.BalanceSnapshot,
		item.CreditBalance, item.CreditExpiresAt, item.PaygUsdBalance, &pricingGroup,
		rawJSON(item.Metadata), defaultTime(item.UpdatedFromGenfityAt),
	).Scan(
		&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.PlanCode, &item.Status,
		&item.PeriodStart, &item.PeriodEnd, &item.QuotaTokensMonthly, &item.BalanceSnapshot,
		&item.CreditBalance, &item.CreditExpiresAt, &item.PaygUsdBalance, &item.PricingGroup,
		&metadata, &item.UpdatedFromGenfityAt,
	)
	if err != nil {
		return store.CustomerEntitlement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.CustomerEntitlement{}, err
	}
	item.Metadata = metadata
	return item, nil
}

func (s *PostgresStore) upsertEntitlementByPlanCode(ctx context.Context, item store.CustomerEntitlement) (store.CustomerEntitlement, error) {
	var metadata json.RawMessage
	// Subscription / unlimited_plan path: keep legacy upsert by
	// (user, plan_code). Same user may hold multiple rows here for
	// history (active + replaced) — that's intentional and audited.
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.customer_entitlements (
			id, genfity_user_id, genfity_tenant_id, plan_code, status,
			period_start, period_end, quota_tokens_monthly, balance_snapshot,
			credit_balance, credit_expires_at, payg_usd_balance, pricing_group,
			metadata, updated_from_genfity_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,COALESCE($10::numeric,0),$11,COALESCE($12::numeric,0),$13,$14,$15)
		ON CONFLICT (genfity_user_id, plan_code) DO UPDATE SET
			genfity_tenant_id = EXCLUDED.genfity_tenant_id,
			status = EXCLUDED.status,
			period_start = EXCLUDED.period_start,
			period_end = EXCLUDED.period_end,
			quota_tokens_monthly = EXCLUDED.quota_tokens_monthly,
			balance_snapshot = EXCLUDED.balance_snapshot,
			credit_balance = EXCLUDED.credit_balance,
			credit_expires_at = EXCLUDED.credit_expires_at,
			payg_usd_balance = EXCLUDED.payg_usd_balance,
			pricing_group = EXCLUDED.pricing_group,
			metadata = EXCLUDED.metadata,
			updated_from_genfity_at = EXCLUDED.updated_from_genfity_at,
			updated_at = now()
		RETURNING id, genfity_user_id, genfity_tenant_id, plan_code, status,
			period_start, period_end, quota_tokens_monthly, balance_snapshot::text,
			credit_balance::text, credit_expires_at, payg_usd_balance::text,
			pricing_group, metadata, updated_from_genfity_at`,
		nilUUID(item.ID), item.GenfityUserID, item.GenfityTenantID, item.PlanCode, defaultString(item.Status, "active"),
		item.PeriodStart, item.PeriodEnd, item.QuotaTokensMonthly, item.BalanceSnapshot,
		item.CreditBalance, item.CreditExpiresAt, item.PaygUsdBalance, item.PricingGroup,
		rawJSON(item.Metadata), defaultTime(item.UpdatedFromGenfityAt),
	).Scan(
		&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.PlanCode, &item.Status,
		&item.PeriodStart, &item.PeriodEnd, &item.QuotaTokensMonthly, &item.BalanceSnapshot,
		&item.CreditBalance, &item.CreditExpiresAt, &item.PaygUsdBalance, &item.PricingGroup,
		&metadata, &item.UpdatedFromGenfityAt,
	)
	if err != nil {
		return store.CustomerEntitlement{}, err
	}
	item.Metadata = metadata
	return item, nil
}

func (s *PostgresStore) GetEntitlementByUser(ctx context.Context, userID string) (*store.CustomerEntitlement, error) {
	var item store.CustomerEntitlement
	var metadata json.RawMessage
	err := s.pool.QueryRow(ctx, `
		SELECT id, genfity_user_id, genfity_tenant_id, plan_code, status,
			period_start, period_end, quota_tokens_monthly,
			balance_snapshot::text,
			credit_balance::text, credit_balance_reserved::text, credit_expires_at,
			payg_usd_balance::text, payg_usd_balance_reserved::text,
			pricing_group, metadata, updated_from_genfity_at
		FROM ai_gateway.customer_entitlements
		WHERE genfity_user_id = $1
		  AND status = 'active'
		  AND (period_end IS NULL OR period_end > now())
		ORDER BY CASE
			WHEN COALESCE(pricing_group, metadata->>'pricingGroup') IN ('unlimited', 'unlimited_plan') THEN 0
			WHEN COALESCE(pricing_group, metadata->>'pricingGroup') = 'credit_package' THEN 1
			ELSE 2
		END,
		-- Tiebreaker: prefer the most recently activated subscription.
		-- We sort by period_start DESC first because a freshly-bought
		-- plan always has a later period_start than the one it replaced;
		-- updated_at is the safety net when period_start is null (legacy
		-- rows). Sorting by period_end DESC instead — as the legacy
		-- query did — picks the row with the latest expiry, which means
		-- a stale 3-day trial can outrank a brand-new 1-day plan and
		-- continue to enforce its (smaller) allowedModels list.
		period_start DESC NULLS LAST, updated_at DESC, period_end DESC NULLS LAST
		LIMIT 1`, userID).Scan(
		&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.PlanCode, &item.Status,
		&item.PeriodStart, &item.PeriodEnd, &item.QuotaTokensMonthly,
		&item.BalanceSnapshot,
		&item.CreditBalance, &item.CreditBalanceReserved, &item.CreditExpiresAt,
		&item.PaygUsdBalance, &item.PaygUsdBalanceReserved,
		&item.PricingGroup, &metadata, &item.UpdatedFromGenfityAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	item.Metadata = metadata
	return &item, err
}

// ListActiveEntitlementsByUser returns every active, non-expired
// entitlement row for the user. The caller picks the right row for
// the request — credit-pinned keys must read CreditBalance from the
// credit_package row, not the unlimited row whose CreditBalance is
// always NULL. Same priority-ordered as GetEntitlementByUser so
// consumers that take the first row keep the legacy behavior.
func (s *PostgresStore) ListActiveEntitlementsByUser(ctx context.Context, userID string) ([]store.CustomerEntitlement, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, genfity_user_id, genfity_tenant_id, plan_code, status,
			period_start, period_end, quota_tokens_monthly,
			balance_snapshot::text,
			credit_balance::text, credit_balance_reserved::text, credit_expires_at,
			payg_usd_balance::text, payg_usd_balance_reserved::text,
			pricing_group, metadata, updated_from_genfity_at
		FROM ai_gateway.customer_entitlements
		WHERE genfity_user_id = $1
		  AND status = 'active'
		  AND (period_end IS NULL OR period_end > now())
		ORDER BY CASE
			WHEN COALESCE(pricing_group, metadata->>'pricingGroup') IN ('unlimited', 'unlimited_plan') THEN 0
			WHEN COALESCE(pricing_group, metadata->>'pricingGroup') = 'credit_package' THEN 1
			ELSE 2
		END,
		period_start DESC NULLS LAST, updated_at DESC, period_end DESC NULLS LAST`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.CustomerEntitlement
	for rows.Next() {
		var item store.CustomerEntitlement
		var metadata json.RawMessage
		if err := rows.Scan(
			&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.PlanCode, &item.Status,
			&item.PeriodStart, &item.PeriodEnd, &item.QuotaTokensMonthly,
			&item.BalanceSnapshot,
			&item.CreditBalance, &item.CreditBalanceReserved, &item.CreditExpiresAt,
			&item.PaygUsdBalance, &item.PaygUsdBalanceReserved,
			&item.PricingGroup, &metadata, &item.UpdatedFromGenfityAt,
		); err != nil {
			return nil, err
		}
		item.Metadata = metadata
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpsertBalanceSnapshot(ctx context.Context, userID string, balance string, paygBalance *string, creditExpiresAt *time.Time) (*store.CustomerEntitlement, error) {
	var affected int64
	var creditRow *store.CustomerEntitlement

	// Credit balance belongs on the user's active credit_package row ONLY.
	// The old behaviour updated "the most recently updated active row"
	// (ORDER BY updated_at DESC LIMIT 1) regardless of pricing_group, so an
	// admin grant or top-up landed credit_balance on the user's
	// unlimited_plan row whenever that row had been touched more recently.
	// The billing path (ReserveRequestCredits) reads credit_balance
	// exclusively from the credit_package row, so those credits were
	// invisible and the user got insufficient_credit_balance despite a
	// positive balance. Target the credit_package row explicitly — the
	// partial unique index guarantees at most one such active row.
	{
		var item store.CustomerEntitlement
		var metadata json.RawMessage
		setClauses := `balance_snapshot = $2, credit_balance = $2::numeric, updated_from_genfity_at = now(), updated_at = now()`
		args := []any{userID, balance}
		argIdx := 3
		if creditExpiresAt != nil {
			setClauses += fmt.Sprintf(`, credit_expires_at = $%d`, argIdx)
			args = append(args, *creditExpiresAt)
			argIdx++
		}
		query := fmt.Sprintf(`UPDATE ai_gateway.customer_entitlements SET %s
			WHERE genfity_user_id = $1 AND status = 'active' AND pricing_group = 'credit_package'
			RETURNING id, genfity_user_id, genfity_tenant_id, plan_code, status, period_start, period_end, quota_tokens_monthly, balance_snapshot::text, metadata, updated_from_genfity_at`, setClauses)
		err := s.pool.QueryRow(ctx, query, args...).Scan(&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.PlanCode, &item.Status, &item.PeriodStart, &item.PeriodEnd, &item.QuotaTokensMonthly, &item.BalanceSnapshot, &metadata, &item.UpdatedFromGenfityAt)
		if err == nil {
			item.Metadata = metadata
			creditRow = &item
			affected++
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}

	// PAYG balance belongs on the active payg_topup row ONLY — same reason
	// as credit above. Writing it onto a credit_package or unlimited row
	// would be invisible to ReservePaygUsdBalance.
	if paygBalance != nil {
		cmd, err := s.pool.Exec(ctx,
			`UPDATE ai_gateway.customer_entitlements
			   SET payg_usd_balance = $2::numeric, updated_from_genfity_at = now(), updated_at = now()
			 WHERE genfity_user_id = $1 AND status = 'active' AND pricing_group = 'payg_topup'`,
			userID, *paygBalance)
		if err != nil {
			return nil, err
		}
		affected += cmd.RowsAffected()
	}

	// No credit_package or payg_topup row exists for this user. The
	// canonical fix lives in genfity-app, which now ensures a
	// credit_package entitlement exists and syncs it (carrying plan_code +
	// balance) via the entitlement path before/alongside this balance push.
	// Surface ErrNotFound so the caller can tell "nothing to update" apart
	// from a successful write.
	if affected == 0 {
		return nil, ErrNotFound
	}
	return creditRow, nil
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
		INSERT INTO ai_gateway.quota_counters (genfity_user_id, period_start, period_end, tokens_used, tokens_reserved, request_count, updated_at)
		VALUES ($1, $2, $3, $5, 0, $6, now())
		ON CONFLICT (genfity_user_id, period_start, period_end)
		DO UPDATE SET tokens_reserved = GREATEST(ai_gateway.quota_counters.tokens_reserved - $4, 0),
			tokens_used = ai_gateway.quota_counters.tokens_used + $5,
			request_count = ai_gateway.quota_counters.request_count + $6,
			updated_at = now()`,
		userID, periodStart, periodEnd, reservedTokens, usedTokens, requestCount)
	return err
}

func (s *PostgresStore) ReserveCreditBalance(ctx context.Context, userID string, planCode string, amountUsd float64) error {
	if amountUsd <= 0 {
		return nil
	}
	// PRD v3 Phase 4: don't filter by plan_code. The user's plan_code
	// can change at runtime when they top up to a higher tier
	// (starter→developer); a reserve held against the OLD plan_code
	// would silently fail to lock balance after the sync. Migration
	// 00018 guarantees a single active credit_package row per user, so
	// targeting (genfity_user_id, status='active', pricing_group=
	// 'credit_package') is unambiguous and survives plan_code changes.
	cmd, err := s.pool.Exec(ctx,
		`UPDATE ai_gateway.customer_entitlements
			 SET credit_balance_reserved = COALESCE(credit_balance_reserved, 0) + $2,
			     updated_at = now()
			 WHERE genfity_user_id = $1
			   AND status = 'active'
			   AND pricing_group = 'credit_package'
			   AND COALESCE(credit_balance, 0) - COALESCE(credit_balance_reserved, 0) >= $2`,
		userID, amountUsd)
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
	// Same single-row targeting as ReserveCreditBalance — plan_code is
	// not part of the key so a topup that bumps plan_code mid-flight
	// still finalizes against the same row.
	_, err := s.pool.Exec(ctx,
		`UPDATE ai_gateway.customer_entitlements
			 SET credit_balance_reserved = GREATEST(COALESCE(credit_balance_reserved, 0) - $2, 0),
			     credit_balance = GREATEST(COALESCE(credit_balance, 0) - LEAST($3, COALESCE(credit_balance, 0)), 0),
			     balance_snapshot = GREATEST(COALESCE(balance_snapshot, 0) - LEAST($3, COALESCE(balance_snapshot, 0)), 0),
			     updated_at = now()
			 WHERE genfity_user_id = $1
			   AND status = 'active'
			   AND pricing_group = 'credit_package'`,
		userID, reservedUsd, actualUsd)
	return err
}

// PRD v3 Phase 2 — request-credit + PAYG USD balance reserve/finalize.
//
// These operate on the new columns added in migration 00005:
//   credit_balance / credit_balance_reserved  — integer-credit schema
//   payg_usd_balance / payg_usd_balance_reserved — USD-balance schema
//
// Reservation uses a guarded UPDATE (non-negative invariant checked in
// WHERE clause) so if the SQL returns 0 rows, the caller knows the
// reservation was rejected for insufficient funds rather than a
// not-found condition. The CHECK constraints on the columns catch any
// concurrency bug that gets past the WHERE.
//
// The UPDATE picks ANY active entitlement belonging to the user — we
// don't require a planCode because stackable schemas (credit_package,
// payg_topup) mean a user can hold multiple entitlements and the
// billing ledger is the source of truth for the aggregate balance.
// Picking by sort_order ASC (oldest entitlement first) gives FIFO-ish
// consumption which is what the PRD requires.

func (s *PostgresStore) ReserveRequestCredits(ctx context.Context, userID string, amount float64) error {
	if amount <= 0 {
		return nil
	}
	// Migration 00018 enforces "one active credit_package row per user"
	// (partial unique index). Lookup is deterministic — no ORDER BY +
	// LIMIT 1 over multiple rows that could race or pick the wrong row.
	// The CHECK constraint customer_entitlements_credit_balance_nonneg
	// + the WHERE clause make this a single atomic operation.
	cmd, err := s.pool.Exec(ctx,
		`UPDATE ai_gateway.customer_entitlements
			 SET credit_balance_reserved = credit_balance_reserved + $2,
			     updated_at = now()
			 WHERE genfity_user_id = $1
			   AND status = 'active'
			   AND pricing_group = 'credit_package'
			   AND credit_balance - credit_balance_reserved >= $2`,
		userID, amount)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrInsufficientBalance
	}
	return nil
}

func (s *PostgresStore) FinalizeRequestCredits(ctx context.Context, userID string, reservedAmount, actualAmount float64) error {
	if reservedAmount <= 0 && actualAmount <= 0 {
		return nil
	}
	if reservedAmount < 0 {
		reservedAmount = 0
	}
	if actualAmount < 0 {
		actualAmount = 0
	}
	// Same single-row guarantee as ReserveRequestCredits — finalize
	// always touches the row that was just reserved against, not
	// "whichever row has the highest balance" (the legacy ORDER BY
	// could pick a different row when multiple credit_package rows
	// existed, leaving reservations orphaned on one row and debits
	// landing on another).
	_, err := s.pool.Exec(ctx,
		`UPDATE ai_gateway.customer_entitlements
			 SET credit_balance_reserved = GREATEST(credit_balance_reserved - $2, 0),
			     credit_balance = GREATEST(credit_balance - LEAST($3, credit_balance), 0),
			     updated_at = now()
			 WHERE genfity_user_id = $1
			   AND status = 'active'
			   AND pricing_group = 'credit_package'`,
		userID, reservedAmount, actualAmount)
	return err
}

func (s *PostgresStore) ReservePaygUsdBalance(ctx context.Context, userID string, amount float64) error {
	if amount <= 0 {
		return nil
	}
	// Single-row guarantee from migration 00018: at most one active
	// payg_topup row per user.
	cmd, err := s.pool.Exec(ctx,
		`UPDATE ai_gateway.customer_entitlements
			 SET payg_usd_balance_reserved = payg_usd_balance_reserved + $2,
			     updated_at = now()
			 WHERE genfity_user_id = $1
			   AND status = 'active'
			   AND pricing_group = 'payg_topup'
			   AND payg_usd_balance - payg_usd_balance_reserved >= $2`,
		userID, amount)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrInsufficientBalance
	}
	return nil
}

func (s *PostgresStore) FinalizePaygUsdBalance(ctx context.Context, userID string, reservedAmount, actualAmount float64) error {
	if reservedAmount <= 0 && actualAmount <= 0 {
		return nil
	}
	if reservedAmount < 0 {
		reservedAmount = 0
	}
	if actualAmount < 0 {
		actualAmount = 0
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE ai_gateway.customer_entitlements
			 SET payg_usd_balance_reserved = GREATEST(payg_usd_balance_reserved - $2, 0),
			     payg_usd_balance = GREATEST(payg_usd_balance - LEAST($3, payg_usd_balance), 0),
			     updated_at = now()
			 WHERE genfity_user_id = $1
			   AND status = 'active'
			   AND pricing_group = 'payg_topup'`,
		userID, reservedAmount, actualAmount)
	return err
}

// Model credit cost: synced catalog. Upsert key is full_model_id.

func (s *PostgresStore) UpsertModelCreditCost(ctx context.Context, cost store.ModelCreditCost) (store.ModelCreditCost, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.model_credit_cost (full_model_id, credits_per_req, is_free, is_active, notes, metadata, synced_at)
		VALUES ($1, $2::numeric, $3, $4, $5, COALESCE($6, '{}'::jsonb), now())
		ON CONFLICT (full_model_id) DO UPDATE SET
			credits_per_req = EXCLUDED.credits_per_req,
			is_free = EXCLUDED.is_free,
			is_active = EXCLUDED.is_active,
			notes = EXCLUDED.notes,
			metadata = EXCLUDED.metadata,
			synced_at = now(),
			updated_at = now()
		RETURNING id, full_model_id, credits_per_req::text, is_free, is_active, notes, metadata, synced_at, created_at, updated_at`,
		cost.FullModelID, cost.CreditsPerReq, cost.IsFree, cost.IsActive, cost.Notes, cost.Metadata)
	var out store.ModelCreditCost
	if err := row.Scan(&out.ID, &out.FullModelID, &out.CreditsPerReq, &out.IsFree, &out.IsActive, &out.Notes, &out.Metadata, &out.SyncedAt, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return store.ModelCreditCost{}, err
	}
	return out, nil
}

func (s *PostgresStore) GetModelCreditCost(ctx context.Context, fullModelID string) (*store.ModelCreditCost, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, full_model_id, credits_per_req::text, is_free, is_active, notes, metadata, synced_at, created_at, updated_at
		   FROM ai_gateway.model_credit_cost
		   WHERE full_model_id = $1`,
		fullModelID)
	var out store.ModelCreditCost
	if err := row.Scan(&out.ID, &out.FullModelID, &out.CreditsPerReq, &out.IsFree, &out.IsActive, &out.Notes, &out.Metadata, &out.SyncedAt, &out.CreatedAt, &out.UpdatedAt); err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

func (s *PostgresStore) ListModelCreditCosts(ctx context.Context) []store.ModelCreditCost {
	rows, err := s.pool.Query(ctx,
		`SELECT id, full_model_id, credits_per_req::text, is_free, is_active, notes, metadata, synced_at, created_at, updated_at
		   FROM ai_gateway.model_credit_cost
		   ORDER BY full_model_id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]store.ModelCreditCost, 0)
	for rows.Next() {
		var item store.ModelCreditCost
		if err := rows.Scan(&item.ID, &item.FullModelID, &item.CreditsPerReq, &item.IsFree, &item.IsActive, &item.Notes, &item.Metadata, &item.SyncedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			continue
		}
		out = append(out, item)
	}
	return out
}

// PAYG top-up rate catalog — synced from genfity-app.

func (s *PostgresStore) UpsertPaygTopupRate(ctx context.Context, rate store.PaygTopupRate) (store.PaygTopupRate, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO ai_gateway.payg_topup_rate (
			code, display_name, usd_amount, price_idr, rate_usd_idr,
			status, sort_order, valid_from, valid_until, is_promo, metadata, synced_at
		)
		VALUES ($1, $2, $3::numeric, $4::numeric, $5::numeric, $6, $7, $8, $9, $10, COALESCE($11, '{}'::jsonb), now())
		ON CONFLICT (code) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			usd_amount   = EXCLUDED.usd_amount,
			price_idr    = EXCLUDED.price_idr,
			rate_usd_idr = EXCLUDED.rate_usd_idr,
			status       = EXCLUDED.status,
			sort_order   = EXCLUDED.sort_order,
			valid_from   = EXCLUDED.valid_from,
			valid_until  = EXCLUDED.valid_until,
			is_promo     = EXCLUDED.is_promo,
			metadata     = EXCLUDED.metadata,
			synced_at    = now(),
			updated_at   = now()
		RETURNING id, code, display_name, usd_amount::text, price_idr::text, rate_usd_idr::text,
			status, sort_order, valid_from, valid_until, is_promo, metadata, synced_at, created_at, updated_at`,
		rate.Code, rate.DisplayName, rate.UsdAmount, rate.PriceIdr, rate.RateUsdIdr,
		rate.Status, rate.SortOrder, rate.ValidFrom, rate.ValidUntil, rate.IsPromo, rate.Metadata)
	var out store.PaygTopupRate
	if err := row.Scan(&out.ID, &out.Code, &out.DisplayName, &out.UsdAmount, &out.PriceIdr, &out.RateUsdIdr,
		&out.Status, &out.SortOrder, &out.ValidFrom, &out.ValidUntil, &out.IsPromo, &out.Metadata, &out.SyncedAt, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return store.PaygTopupRate{}, err
	}
	return out, nil
}

func (s *PostgresStore) GetPaygTopupRate(ctx context.Context, code string) (*store.PaygTopupRate, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, code, display_name, usd_amount::text, price_idr::text, rate_usd_idr::text,
			status, sort_order, valid_from, valid_until, is_promo, metadata, synced_at, created_at, updated_at
		   FROM ai_gateway.payg_topup_rate
		   WHERE code = $1`, code)
	var out store.PaygTopupRate
	if err := row.Scan(&out.ID, &out.Code, &out.DisplayName, &out.UsdAmount, &out.PriceIdr, &out.RateUsdIdr,
		&out.Status, &out.SortOrder, &out.ValidFrom, &out.ValidUntil, &out.IsPromo, &out.Metadata, &out.SyncedAt, &out.CreatedAt, &out.UpdatedAt); err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

func (s *PostgresStore) ListPaygTopupRates(ctx context.Context) []store.PaygTopupRate {
	rows, err := s.pool.Query(ctx,
		`SELECT id, code, display_name, usd_amount::text, price_idr::text, rate_usd_idr::text,
			status, sort_order, valid_from, valid_until, is_promo, metadata, synced_at, created_at, updated_at
		   FROM ai_gateway.payg_topup_rate
		   ORDER BY sort_order, code`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]store.PaygTopupRate, 0)
	for rows.Next() {
		var item store.PaygTopupRate
		if err := rows.Scan(&item.ID, &item.Code, &item.DisplayName, &item.UsdAmount, &item.PriceIdr, &item.RateUsdIdr,
			&item.Status, &item.SortOrder, &item.ValidFrom, &item.ValidUntil, &item.IsPromo, &item.Metadata, &item.SyncedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			continue
		}
		out = append(out, item)
	}
	return out
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

func (s *PostgresStore) ReleaseStaleReservations(ctx context.Context, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		olderThan = 5 * time.Minute
	}
	// Release reservations whose row hasn't been touched within the
	// threshold. A live in-flight request bumps updated_at on every
	// reserve/finalize, so anything older is by definition orphaned —
	// either the process panicked, was killed, or the client
	// disconnected before the deferred rollback fired. Targets both
	// credit_balance_reserved (credit_package) and
	// payg_usd_balance_reserved (payg_topup) since both pricing groups
	// can hold stuck reservations.
	cmd, err := s.pool.Exec(ctx, `
		UPDATE ai_gateway.customer_entitlements
		   SET credit_balance_reserved = 0,
		       payg_usd_balance_reserved = 0,
		       updated_at = now()
		 WHERE status = 'active'
		   AND (COALESCE(credit_balance_reserved, 0) > 0 OR COALESCE(payg_usd_balance_reserved, 0) > 0)
		   AND updated_at < now() - ($1::bigint || ' milliseconds')::interval`,
		olderThan.Milliseconds())
	if err != nil {
		return 0, err
	}
	return cmd.RowsAffected(), nil
}

func (s *PostgresStore) DebitCreditBalance(ctx context.Context, userID string, planCode string, debitUsd float64) error {
	// PRD v3 Phase 4: target by (user, pricing_group='credit_package',
	// status='active') instead of plan_code. plan_code may have changed
	// between reservation and callback (top-up upgrade) and the legacy
	// filter would silently no-op the debit.
	cmd, err := s.pool.Exec(ctx,
		`UPDATE ai_gateway.customer_entitlements
			 SET credit_balance = GREATEST(COALESCE(credit_balance, 0) - $2, 0),
			     balance_snapshot = GREATEST(COALESCE(balance_snapshot, 0) - $2, 0),
			     updated_at = now()
			 WHERE genfity_user_id = $1
			   AND status = 'active'
			   AND pricing_group = 'credit_package'
			   AND GREATEST(COALESCE(credit_balance, 0) - COALESCE(credit_balance_reserved, 0), 0) >= $2`,
		userID, debitUsd)
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
		INSERT INTO ai_gateway.usage_ledger (id, request_id, genfity_user_id, genfity_tenant_id, api_key_id, public_model, router_model, router_instance_code, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, input_cost, output_cost, total_cost, billing_mode, amount_credits, balance_after_credits, balance_after_usd, status, error_code, latency_ms, started_at, finished_at, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)
		RETURNING id, request_id, genfity_user_id, genfity_tenant_id, api_key_id, public_model, router_model, router_instance_code, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, input_cost::text, output_cost::text, total_cost::text, billing_mode, amount_credits::text, balance_after_credits::text, balance_after_usd::text, status, error_code, latency_ms, started_at, finished_at, metadata`,
		nilUUID(item.ID), item.RequestID, item.GenfityUserID, item.GenfityTenantID, item.APIKeyID, item.PublicModel, item.RouterModel, item.RouterInstanceCode, item.PromptTokens, item.CompletionTokens, item.TotalTokens, item.CachedTokens, item.ReasoningTokens, item.InputCost, item.OutputCost, item.TotalCost, item.BillingMode, item.AmountCredits, item.BalanceAfterCredits, item.BalanceAfterUsd, item.Status, item.ErrorCode, item.LatencyMS, defaultTime(item.StartedAt), item.FinishedAt, rawJSON(item.Metadata),
	).Scan(&item.ID, &item.RequestID, &item.GenfityUserID, &item.GenfityTenantID, &item.APIKeyID, &item.PublicModel, &item.RouterModel, &item.RouterInstanceCode, &item.PromptTokens, &item.CompletionTokens, &item.TotalTokens, &item.CachedTokens, &item.ReasoningTokens, &item.InputCost, &item.OutputCost, &item.TotalCost, &item.BillingMode, &item.AmountCredits, &item.BalanceAfterCredits, &item.BalanceAfterUsd, &item.Status, &item.ErrorCode, &item.LatencyMS, &item.StartedAt, &item.FinishedAt, &item.Metadata)
	if err != nil {
		return store.UsageLedgerEntry{}, err
	}
	return item, nil
}

const usageLedgerSelectColumns = `id, request_id, genfity_user_id, genfity_tenant_id, api_key_id, public_model, router_model, router_instance_code, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, input_cost::text, output_cost::text, total_cost::text, billing_mode, amount_credits::text, balance_after_credits::text, balance_after_usd::text, status, error_code, latency_ms, started_at, finished_at, metadata`

func (s *PostgresStore) ListUsage(ctx context.Context) []store.UsageLedgerEntry {
	return s.listUsage(ctx, `SELECT `+usageLedgerSelectColumns+` FROM ai_gateway.usage_ledger ORDER BY started_at DESC`)
}

func (s *PostgresStore) ListAllUsage(ctx context.Context, limit int) []store.UsageLedgerEntry {
	if limit <= 0 {
		limit = 100
	}
	return s.listUsage(ctx, `SELECT `+usageLedgerSelectColumns+` FROM ai_gateway.usage_ledger ORDER BY started_at DESC LIMIT $1`, limit)
}

// ListUsageLogs powers the admin "Logs" modal. Returns the page rows
// plus a total row count for the same filter so the UI can render a
// pager. Total ignores limit/offset so the count is stable across
// pages.
func (s *PostgresStore) ListUsageLogs(ctx context.Context, f store.UsageLogFilter) ([]store.UsageLedgerEntry, int, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	conds := []string{"1 = 1"}
	args := []any{}
	pos := 0
	addArg := func(v any) string {
		args = append(args, v)
		pos++
		return fmt.Sprintf("$%d", pos)
	}

	if f.UserID != "" {
		conds = append(conds, "genfity_user_id = "+addArg(f.UserID))
	}
	if f.APIKeyID != nil {
		conds = append(conds, "api_key_id = "+addArg(*f.APIKeyID))
	}
	if f.Status != "" {
		conds = append(conds, "status = "+addArg(f.Status))
	}
	if f.BillingMode != "" {
		conds = append(conds, "billing_mode = "+addArg(f.BillingMode))
	}
	if f.PublicModel != "" {
		conds = append(conds, "public_model = "+addArg(f.PublicModel))
	}
	if f.Search != "" {
		needle := "%" + strings.ToLower(f.Search) + "%"
		conds = append(conds, "(LOWER(genfity_user_id) LIKE "+addArg(needle)+" OR LOWER(public_model) LIKE "+addArg(needle)+" OR LOWER(request_id) LIKE "+addArg(needle)+")")
	}
	if !f.From.IsZero() {
		conds = append(conds, "started_at >= "+addArg(f.From))
	}
	if !f.To.IsZero() {
		conds = append(conds, "started_at < "+addArg(f.To))
	}

	whereSQL := strings.Join(conds, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM ai_gateway.usage_ledger WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := addArg(limit)
	offsetArg := addArg(offset)

	query := `SELECT ` + usageLedgerSelectColumns +
		` FROM ai_gateway.usage_ledger WHERE ` + whereSQL +
		` ORDER BY started_at DESC LIMIT ` + limitArg + ` OFFSET ` + offsetArg

	items := s.listUsage(ctx, query, args...)
	return items, total, nil
}

func (s *PostgresStore) ListUsageSummaryGrouped(ctx context.Context, since time.Time) []store.UsageSummaryRow {
	query := `
		SELECT
			COALESCE(NULLIF(metadata->>'pricing_group', ''), 'unknown') AS pricing_group,
			genfity_user_id,
			COUNT(*)::int AS request_count,
			COALESCE(SUM(prompt_tokens), 0)::bigint AS input_tokens,
			COALESCE(SUM(completion_tokens), 0)::bigint AS output_tokens,
			COALESCE(SUM(total_tokens), 0)::bigint AS total_tokens,
			COALESCE(SUM(total_cost), 0)::text AS total_cost,
			MAX(started_at) AS last_active
		FROM ai_gateway.usage_ledger
		WHERE ($1::timestamptz IS NULL OR started_at >= $1)
		GROUP BY pricing_group, genfity_user_id
		ORDER BY COALESCE(SUM(total_cost), 0) DESC`

	var sinceArg any
	if !since.IsZero() {
		sinceArg = since
	}

	rows, err := s.pool.Query(ctx, query, sinceArg)
	if err != nil {
		return nil
	}
	defer rows.Close()

	items := []store.UsageSummaryRow{}
	for rows.Next() {
		var item store.UsageSummaryRow
		if rows.Scan(&item.PricingGroup, &item.GenfityUserID, &item.RequestCount, &item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.TotalCost, &item.LastActive) == nil {
			items = append(items, item)
		}
	}
	return items
}

func (s *PostgresStore) ListProviderStats(ctx context.Context, since time.Time) []store.ProviderStatsRow {
	query := `
		SELECT
			COALESCE(NULLIF(split_part(router_model, '/', 1), ''), 'unknown') AS prefix,
			COUNT(*)::bigint AS total_count,
			COUNT(*) FILTER (WHERE status = 'success')::bigint AS success_count,
			COUNT(*) FILTER (WHERE status != 'success')::bigint AS error_count
		FROM ai_gateway.usage_ledger
		WHERE ($1::timestamptz IS NULL OR started_at >= $1)
		GROUP BY prefix
		ORDER BY total_count DESC`

	var sinceArg any
	if !since.IsZero() {
		sinceArg = since
	}

	rows, err := s.pool.Query(ctx, query, sinceArg)
	if err != nil {
		return nil
	}
	defer rows.Close()

	items := []store.ProviderStatsRow{}
	for rows.Next() {
		var item store.ProviderStatsRow
		if rows.Scan(&item.Prefix, &item.TotalCount, &item.SuccessCount, &item.ErrorCount) == nil {
			items = append(items, item)
		}
	}
	return items
}

// ListUsageTimeseries buckets usage_ledger into hour or day windows
// depending on `bucket`. Anything other than "hour" falls back to
// "day". The result is empty (not nil) when no rows match.
func (s *PostgresStore) ListUsageTimeseries(ctx context.Context, since time.Time, bucket string) []store.UsageTimeseriesPoint {
	trunc := "day"
	if bucket == "hour" {
		trunc = "hour"
	}
	query := `
		SELECT
			date_trunc('` + trunc + `', started_at) AS bucket,
			COUNT(*)::bigint AS request_count,
			COUNT(*) FILTER (WHERE status = 'success')::bigint AS success_count,
			COUNT(*) FILTER (WHERE status != 'success')::bigint AS error_count,
			COALESCE(SUM(prompt_tokens), 0)::bigint AS input_tokens,
			COALESCE(SUM(completion_tokens), 0)::bigint AS output_tokens,
			COALESCE(SUM(total_tokens), 0)::bigint AS total_tokens,
			COALESCE(SUM(total_cost), 0)::text AS total_cost
		FROM ai_gateway.usage_ledger
		WHERE ($1::timestamptz IS NULL OR started_at >= $1)
		GROUP BY bucket
		ORDER BY bucket ASC`

	var sinceArg any
	if !since.IsZero() {
		sinceArg = since
	}
	rows, err := s.pool.Query(ctx, query, sinceArg)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.UsageTimeseriesPoint{}
	for rows.Next() {
		var item store.UsageTimeseriesPoint
		if rows.Scan(&item.Bucket, &item.RequestCount, &item.SuccessCount, &item.ErrorCount, &item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.TotalCost) == nil {
			items = append(items, item)
		}
	}
	return items
}

// ListTopModels returns the highest-cost public_model entries since
// `since`, ordered by total_cost desc. Limit defaults to 10 when 0.
func (s *PostgresStore) ListTopModels(ctx context.Context, since time.Time, limit int) []store.TopModelRow {
	if limit <= 0 {
		limit = 10
	}
	query := `
		SELECT
			public_model,
			COUNT(*)::bigint AS request_count,
			COALESCE(SUM(prompt_tokens), 0)::bigint AS input_tokens,
			COALESCE(SUM(completion_tokens), 0)::bigint AS output_tokens,
			COALESCE(SUM(total_tokens), 0)::bigint AS total_tokens,
			COALESCE(SUM(total_cost), 0)::text AS total_cost,
			COUNT(*) FILTER (WHERE status = 'success')::bigint AS success_count,
			COUNT(*) FILTER (WHERE status != 'success')::bigint AS error_count
		FROM ai_gateway.usage_ledger
		WHERE ($1::timestamptz IS NULL OR started_at >= $1)
		GROUP BY public_model
		ORDER BY COALESCE(SUM(total_cost), 0) DESC, COUNT(*) DESC
		LIMIT $2`

	var sinceArg any
	if !since.IsZero() {
		sinceArg = since
	}
	rows, err := s.pool.Query(ctx, query, sinceArg, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.TopModelRow{}
	for rows.Next() {
		var item store.TopModelRow
		if rows.Scan(&item.PublicModel, &item.RequestCount, &item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.TotalCost, &item.SuccessCount, &item.ErrorCount) == nil {
			items = append(items, item)
		}
	}
	return items
}

// ListBillingModeBreakdown groups usage_ledger by billing_mode. NULL
// values are reported as "subscription_unmetered" so the admin chart
// can clearly distinguish them from rows that explicitly went through
// a priority-billing path.
func (s *PostgresStore) ListBillingModeBreakdown(ctx context.Context, since time.Time) []store.BillingModeBreakdownRow {
	query := `
		SELECT
			COALESCE(NULLIF(billing_mode, ''), 'subscription_unmetered') AS billing_mode,
			COUNT(*)::bigint AS request_count,
			COALESCE(SUM(total_tokens), 0)::bigint AS total_tokens,
			COALESCE(SUM(total_cost), 0)::text AS total_cost
		FROM ai_gateway.usage_ledger
		WHERE ($1::timestamptz IS NULL OR started_at >= $1)
		GROUP BY billing_mode
		ORDER BY COUNT(*) DESC`

	var sinceArg any
	if !since.IsZero() {
		sinceArg = since
	}
	rows, err := s.pool.Query(ctx, query, sinceArg)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.BillingModeBreakdownRow{}
	for rows.Next() {
		var item store.BillingModeBreakdownRow
		if rows.Scan(&item.BillingMode, &item.RequestCount, &item.TotalTokens, &item.TotalCost) == nil {
			items = append(items, item)
		}
	}
	return items
}

// ListStatusBreakdown returns request counts grouped by the `status`
// column. Used to render the success/error donut.
func (s *PostgresStore) ListStatusBreakdown(ctx context.Context, since time.Time) []store.StatusBreakdownRow {
	query := `
		SELECT
			COALESCE(NULLIF(status, ''), 'unknown') AS bucket,
			COUNT(*)::bigint AS request_count
		FROM ai_gateway.usage_ledger
		WHERE ($1::timestamptz IS NULL OR started_at >= $1)
		GROUP BY bucket
		ORDER BY COUNT(*) DESC`

	var sinceArg any
	if !since.IsZero() {
		sinceArg = since
	}
	rows, err := s.pool.Query(ctx, query, sinceArg)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.StatusBreakdownRow{}
	for rows.Next() {
		var item store.StatusBreakdownRow
		if rows.Scan(&item.Bucket, &item.RequestCount) == nil {
			items = append(items, item)
		}
	}
	return items
}

// ListErrorCodeBreakdown returns the top error_code values for the
// time window. Skips rows that succeeded or have a null error_code.
func (s *PostgresStore) ListErrorCodeBreakdown(ctx context.Context, since time.Time, limit int) []store.StatusBreakdownRow {
	if limit <= 0 {
		limit = 10
	}
	query := `
		SELECT
			COALESCE(NULLIF(error_code, ''), 'unknown') AS bucket,
			COUNT(*)::bigint AS request_count
		FROM ai_gateway.usage_ledger
		WHERE ($1::timestamptz IS NULL OR started_at >= $1)
		  AND status != 'success'
		  AND error_code IS NOT NULL
		GROUP BY bucket
		ORDER BY COUNT(*) DESC
		LIMIT $2`

	var sinceArg any
	if !since.IsZero() {
		sinceArg = since
	}
	rows, err := s.pool.Query(ctx, query, sinceArg, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.StatusBreakdownRow{}
	for rows.Next() {
		var item store.StatusBreakdownRow
		if rows.Scan(&item.Bucket, &item.RequestCount) == nil {
			items = append(items, item)
		}
	}
	return items
}

// LatencyStats computes aggregate latency_ms statistics. Postgres
// percentile_cont is used for p50/p95/p99. Returns the zero struct
// when no rows have a non-null latency_ms.
func (s *PostgresStore) LatencyStats(ctx context.Context, since time.Time) store.LatencyStats {
	query := `
		SELECT
			COUNT(*)::bigint AS sample_size,
			COALESCE(AVG(latency_ms)::float8, 0) AS avg_ms,
			COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms)::float8, 0) AS p50_ms,
			COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)::float8, 0) AS p95_ms,
			COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms)::float8, 0) AS p99_ms,
			COALESCE(MAX(latency_ms)::float8, 0) AS max_ms
		FROM ai_gateway.usage_ledger
		WHERE ($1::timestamptz IS NULL OR started_at >= $1)
		  AND latency_ms IS NOT NULL`

	var sinceArg any
	if !since.IsZero() {
		sinceArg = since
	}
	row := s.pool.QueryRow(ctx, query, sinceArg)
	var stats store.LatencyStats
	if err := row.Scan(&stats.SampleSize, &stats.AvgMS, &stats.P50MS, &stats.P95MS, &stats.P99MS, &stats.MaxMS); err != nil {
		return store.LatencyStats{}
	}
	return stats
}

// ListPrefixHourlyStats buckets one prefix's usage_ledger rows into
// hourly windows. Empty prefix matches NULL/empty router_model rows
// (pre-upstream failures — model_not_allowed, billing_failed, etc.).
func (s *PostgresStore) ListPrefixHourlyStats(ctx context.Context, prefix string, since time.Time) []store.PrefixHourlyPoint {
	var query string
	args := []any{}
	args = append(args, nullableTime(since))

	if prefix == "" || prefix == "unknown" {
		query = `
			SELECT
				date_trunc('hour', started_at) AS bucket,
				COUNT(*) FILTER (WHERE status = 'success')::bigint AS success_count,
				COUNT(*) FILTER (WHERE status != 'success')::bigint AS error_count
			FROM ai_gateway.usage_ledger
			WHERE ($1::timestamptz IS NULL OR started_at >= $1)
			  AND (router_model IS NULL OR router_model = '' OR split_part(router_model, '/', 1) = '')
			GROUP BY bucket
			ORDER BY bucket ASC`
	} else {
		query = `
			SELECT
				date_trunc('hour', started_at) AS bucket,
				COUNT(*) FILTER (WHERE status = 'success')::bigint AS success_count,
				COUNT(*) FILTER (WHERE status != 'success')::bigint AS error_count
			FROM ai_gateway.usage_ledger
			WHERE ($1::timestamptz IS NULL OR started_at >= $1)
			  AND split_part(router_model, '/', 1) = $2
			GROUP BY bucket
			ORDER BY bucket ASC`
		args = append(args, prefix)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.PrefixHourlyPoint{}
	for rows.Next() {
		var item store.PrefixHourlyPoint
		if rows.Scan(&item.Bucket, &item.SuccessCount, &item.ErrorCount) == nil {
			items = append(items, item)
		}
	}
	return items
}

// ListPrefixModelStats returns per-model success/error counts within a
// prefix. For the genfity combo prefix this surfaces the actual child
// model that handled each request (claude-opus-4-7, gemini-2.5-pro,
// etc.). For "unknown" rows it shows which public_model the customer
// asked for when the request was rejected pre-upstream.
func (s *PostgresStore) ListPrefixModelStats(ctx context.Context, prefix string, since time.Time, limit int) []store.PrefixModelRow {
	if limit <= 0 {
		limit = 50
	}
	var query string
	args := []any{nullableTime(since)}

	if prefix == "" || prefix == "unknown" {
		query = `
			SELECT
				COALESCE(NULLIF(router_model, ''), '—') AS router_model,
				public_model,
				COUNT(*)::bigint AS total_count,
				COUNT(*) FILTER (WHERE status = 'success')::bigint AS success_count,
				COUNT(*) FILTER (WHERE status != 'success')::bigint AS error_count
			FROM ai_gateway.usage_ledger
			WHERE ($1::timestamptz IS NULL OR started_at >= $1)
			  AND (router_model IS NULL OR router_model = '' OR split_part(router_model, '/', 1) = '')
			GROUP BY router_model, public_model
			ORDER BY total_count DESC
			LIMIT $2`
		args = append(args, limit)
	} else {
		query = `
			SELECT
				router_model,
				public_model,
				COUNT(*)::bigint AS total_count,
				COUNT(*) FILTER (WHERE status = 'success')::bigint AS success_count,
				COUNT(*) FILTER (WHERE status != 'success')::bigint AS error_count
			FROM ai_gateway.usage_ledger
			WHERE ($1::timestamptz IS NULL OR started_at >= $1)
			  AND split_part(router_model, '/', 1) = $2
			GROUP BY router_model, public_model
			ORDER BY total_count DESC
			LIMIT $3`
		args = append(args, prefix, limit)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.PrefixModelRow{}
	for rows.Next() {
		var item store.PrefixModelRow
		if rows.Scan(&item.RouterModel, &item.PublicModel, &item.TotalCount, &item.SuccessCount, &item.ErrorCount) == nil {
			items = append(items, item)
		}
	}
	return items
}

// ListPrefixErrorCodes returns the top error_code values for a given
// prefix. Skips rows where status is success or error_code is NULL.
func (s *PostgresStore) ListPrefixErrorCodes(ctx context.Context, prefix string, since time.Time, limit int) []store.StatusBreakdownRow {
	if limit <= 0 {
		limit = 10
	}
	var query string
	args := []any{nullableTime(since)}

	if prefix == "" || prefix == "unknown" {
		query = `
			SELECT
				COALESCE(NULLIF(error_code, ''), 'unknown') AS bucket,
				COUNT(*)::bigint AS request_count
			FROM ai_gateway.usage_ledger
			WHERE ($1::timestamptz IS NULL OR started_at >= $1)
			  AND (router_model IS NULL OR router_model = '' OR split_part(router_model, '/', 1) = '')
			  AND status != 'success'
			  AND error_code IS NOT NULL
			GROUP BY bucket
			ORDER BY COUNT(*) DESC
			LIMIT $2`
		args = append(args, limit)
	} else {
		query = `
			SELECT
				COALESCE(NULLIF(error_code, ''), 'unknown') AS bucket,
				COUNT(*)::bigint AS request_count
			FROM ai_gateway.usage_ledger
			WHERE ($1::timestamptz IS NULL OR started_at >= $1)
			  AND split_part(router_model, '/', 1) = $2
			  AND status != 'success'
			  AND error_code IS NOT NULL
			GROUP BY bucket
			ORDER BY COUNT(*) DESC
			LIMIT $3`
		args = append(args, prefix, limit)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items := []store.StatusBreakdownRow{}
	for rows.Next() {
		var item store.StatusBreakdownRow
		if rows.Scan(&item.Bucket, &item.RequestCount) == nil {
			items = append(items, item)
		}
	}
	return items
}

// nullableTime returns nil for the zero Time value so the SQL
// `$1::timestamptz IS NULL OR started_at >= $1` predicate works
// correctly when the caller wants no time filter.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (s *PostgresStore) ListCreditBalances(ctx context.Context) []store.CreditBalanceRow {
	query := `
		SELECT
			genfity_user_id,
			credit_balance::text,
			COALESCE(credit_balance_reserved, 0)::text AS credit_used
		FROM ai_gateway.customer_entitlements
		WHERE status = 'active'
		  AND COALESCE(pricing_group, '') = 'credit_package'
		  AND credit_balance IS NOT NULL`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	items := []store.CreditBalanceRow{}
	for rows.Next() {
		var item store.CreditBalanceRow
		if rows.Scan(&item.GenfityUserID, &item.CreditBalance, &item.CreditUsed) == nil {
			items = append(items, item)
		}
	}
	return items
}

func (s *PostgresStore) ListUsageByAPIKey(ctx context.Context, apiKeyID uuid.UUID, limit int) []store.UsageLedgerEntry {
	if limit <= 0 {
		limit = 100
	}
	return s.listUsage(ctx, `SELECT `+usageLedgerSelectColumns+` FROM ai_gateway.usage_ledger WHERE api_key_id = $1 ORDER BY started_at DESC LIMIT $2`, apiKeyID, limit)
}

func (s *PostgresStore) ListUsageByUser(ctx context.Context, userID string) []store.UsageLedgerEntry {
	return s.listUsage(ctx, `SELECT `+usageLedgerSelectColumns+` FROM ai_gateway.usage_ledger WHERE genfity_user_id = $1 ORDER BY started_at DESC`, userID)
}

func (s *PostgresStore) ListUsageByUserSince(ctx context.Context, userID string, since time.Time) []store.UsageLedgerEntry {
	return s.listUsage(ctx, `SELECT `+usageLedgerSelectColumns+` FROM ai_gateway.usage_ledger WHERE genfity_user_id = $1 AND started_at >= $2 ORDER BY started_at DESC`, userID, since)
}

func (s *PostgresStore) ListUsageByTenant(ctx context.Context, tenantID string) []store.UsageLedgerEntry {
	return s.listUsage(ctx, `SELECT `+usageLedgerSelectColumns+` FROM ai_gateway.usage_ledger WHERE genfity_tenant_id = $1 ORDER BY started_at DESC`, tenantID)
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
		if rows.Scan(&item.ID, &item.RequestID, &item.GenfityUserID, &item.GenfityTenantID, &item.APIKeyID, &item.PublicModel, &item.RouterModel, &item.RouterInstanceCode, &item.PromptTokens, &item.CompletionTokens, &item.TotalTokens, &item.CachedTokens, &item.ReasoningTokens, &item.InputCost, &item.OutputCost, &item.TotalCost, &item.BillingMode, &item.AmountCredits, &item.BalanceAfterCredits, &item.BalanceAfterUsd, &item.Status, &item.ErrorCode, &item.LatencyMS, &item.StartedAt, &item.FinishedAt, &item.Metadata) == nil {
			items = append(items, item)
		}
	}
	return items
}

func scanAPIKey(row pgx.Row, item *store.APIKey) error {
	return row.Scan(&item.ID, &item.GenfityUserID, &item.GenfityTenantID, &item.Name, &item.KeyPrefix, &item.KeyHash, &item.Status, &item.BillingSource, &item.LastUsedAt, &item.ExpiresAt, &item.CreatedAt, &item.RegeneratedAt, &item.RevokedAt)
}

func scanModel(row pgx.Row, item *store.AIModel) error {
	return row.Scan(&item.ID, &item.PublicModel, &item.DisplayName, &item.Description, &item.Status, &item.ModelType, &item.ContextWindow, &item.SupportsStreaming, &item.SupportsTools, &item.SupportsVision, &item.PaygExposed, &item.IsFree, &item.FreeLimitRPD, &item.FreeLimitRPM, &item.FreeLimitTPD, &item.CreatedAt, &item.UpdatedAt)
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
//
// Removed in 2026-05 (PRD §3.3). The combo system now lives in CLIProxyAPI
// under <auth-dir>/combos.json, so the gateway no longer owns combo tables
// or CRUD. A follow-up migration drops virtual_combos and
// virtual_combo_entries from the database.

// --- Pending callback queue (migration 00019) ---

func (s *PostgresStore) EnqueuePendingCallback(ctx context.Context, item store.PendingCallback) error {
	// Idempotent on (request_id, billing_mode): if this row was already
	// enqueued by a prior retry attempt, leave it as-is. We do NOT bump
	// next_attempt_at — that's the worker's job. Doing it here would
	// reset the backoff every time a duplicate enqueue lands.
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ai_gateway.pending_callbacks (
			request_id, user_id, billing_mode, amount_credits, amount_usd,
			model, notes, status
		)
		VALUES ($1, $2, $3, $4::numeric, $5::numeric, $6, $7, 'pending')
		ON CONFLICT (request_id, billing_mode) DO NOTHING`,
		item.RequestID, item.UserID, item.BillingMode,
		item.AmountCredits, item.AmountUSD, item.Model, item.Notes,
	)
	return err
}

func (s *PostgresStore) ListDuePendingCallbacks(ctx context.Context, limit int) ([]store.PendingCallback, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, request_id, user_id, billing_mode,
			amount_credits::text, amount_usd::text, model, notes,
			attempts, last_error, last_attempt_at, next_attempt_at,
			status, created_at
		FROM ai_gateway.pending_callbacks
		WHERE status = 'pending' AND next_attempt_at <= now()
		ORDER BY next_attempt_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.PendingCallback
	for rows.Next() {
		var item store.PendingCallback
		if err := rows.Scan(
			&item.ID, &item.RequestID, &item.UserID, &item.BillingMode,
			&item.AmountCredits, &item.AmountUSD, &item.Model, &item.Notes,
			&item.Attempts, &item.LastError, &item.LastAttemptAt, &item.NextAttemptAt,
			&item.Status, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresStore) MarkCallbackSucceeded(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM ai_gateway.pending_callbacks WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) MarkCallbackRetry(ctx context.Context, id uuid.UUID, lastError string, nextAttemptAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE ai_gateway.pending_callbacks
		   SET attempts = attempts + 1,
		       last_error = $2,
		       last_attempt_at = now(),
		       next_attempt_at = $3
		 WHERE id = $1`, id, lastError, nextAttemptAt)
	return err
}

func (s *PostgresStore) MarkCallbackAbandoned(ctx context.Context, id uuid.UUID, status string, lastError string) error {
	if status != "abandoned" && status != "failed_permanent" {
		status = "abandoned"
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE ai_gateway.pending_callbacks
		   SET status = $2,
		       attempts = attempts + 1,
		       last_error = $3,
		       last_attempt_at = now()
		 WHERE id = $1`, id, status, lastError)
	return err
}
