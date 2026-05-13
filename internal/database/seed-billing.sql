-- Gateway-side catalog seed, mirroring genfity-app's seed-billing.sql.
-- The gateway reads model_credit_cost and payg_topup_rate on the hot
-- path; genfity-app is the source of truth but there is no push
-- endpoint yet, so we seed directly against the gateway DB.
--
-- Run as:
--   sudo -u postgres psql -d genfity_ai_gateway -f /tmp/seed-gateway-billing.sql

-- 1) Model credit costs ---------------------------------------------------
INSERT INTO ai_gateway.model_credit_cost (full_model_id, credits_per_req, is_free)
VALUES
  ('mtr/minimax-m2.5',      0.1, false),
  ('mtr/gpt-5.4',           0.8, false),
  ('mtr/gpt-5.4-mini',      0.3, false),
  ('mtr/gpt-5.5',           1.1, false),
  ('mtr/claude-opus-4.6',   1.1, false),
  ('mtr/claude-opus-4.7',   1.5, false),
  ('mtr/claude-sonnet-4.6', 0.8, false),
  ('mtr/glm-5',             0.2, false),
  ('mtr/llama-3.3',         0.1, false),
  ('mtr/gpt-oss-120b',      0.1, false),
  ('mtr/qwen3.5',           0.1, false),
  ('mtr/step-3.5-flash',    0.0, true)
ON CONFLICT (full_model_id) DO UPDATE SET
  credits_per_req = EXCLUDED.credits_per_req,
  is_free         = EXCLUDED.is_free,
  synced_at       = NOW(),
  updated_at      = NOW();

-- 2) PAYG top-up rates ----------------------------------------------------
INSERT INTO ai_gateway.payg_topup_rate (code, display_name, usd_amount, price_idr, rate_usd_idr, is_promo, sort_order)
VALUES
  ('payg_promo_10',    'Promo $10',    10, 100000, 10000.0000, true,   5),
  ('payg_starter_10',  'Starter $10',  10, 180000, 18000.0000, false, 10),
  ('payg_standard_50', 'Standard $50', 50, 900000, 18000.0000, false, 20)
ON CONFLICT (code) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  usd_amount   = EXCLUDED.usd_amount,
  price_idr    = EXCLUDED.price_idr,
  rate_usd_idr = EXCLUDED.rate_usd_idr,
  is_promo     = EXCLUDED.is_promo,
  sort_order   = EXCLUDED.sort_order,
  synced_at    = NOW(),
  updated_at   = NOW();

-- Summary
SELECT 'model_credit_cost' AS kind, count(*) FROM ai_gateway.model_credit_cost
UNION ALL
SELECT 'payg_topup_rate', count(*) FROM ai_gateway.payg_topup_rate;
