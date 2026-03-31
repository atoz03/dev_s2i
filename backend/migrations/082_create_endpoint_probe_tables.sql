CREATE TABLE IF NOT EXISTS endpoint_probe_plans (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    mode VARCHAR(16) NOT NULL DEFAULT 'head',
    targets JSONB NOT NULL DEFAULT '[]'::jsonb,
    headers JSONB NOT NULL DEFAULT '{}'::jsonb,
    timeout_ms INTEGER NOT NULL DEFAULT 5000,
    interval_seconds INTEGER NOT NULL DEFAULT 60,
    max_concurrency INTEGER NOT NULL DEFAULT 4,
    last_run_at TIMESTAMPTZ NULL,
    next_run_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_endpoint_probe_plans_enabled_next_run
    ON endpoint_probe_plans(enabled, next_run_at);

CREATE TABLE IF NOT EXISTS endpoint_probe_results (
    id BIGSERIAL PRIMARY KEY,
    plan_id BIGINT NOT NULL REFERENCES endpoint_probe_plans(id) ON DELETE CASCADE,
    target_url TEXT NOT NULL,
    mode VARCHAR(16) NOT NULL,
    success BOOLEAN NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 0,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    message TEXT NOT NULL DEFAULT '',
    resolved_user_agent TEXT NOT NULL DEFAULT '',
    probed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_endpoint_probe_results_plan_id_created_at
    ON endpoint_probe_results(plan_id, created_at DESC);
