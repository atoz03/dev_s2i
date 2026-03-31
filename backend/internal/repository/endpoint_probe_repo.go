package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type endpointProbePlanRepository struct {
	db *sql.DB
}

func NewEndpointProbePlanRepository(db *sql.DB) service.EndpointProbePlanRepository {
	return &endpointProbePlanRepository{db: db}
}

func (r *endpointProbePlanRepository) Create(ctx context.Context, plan *service.EndpointProbePlan) (*service.EndpointProbePlan, error) {
	targetsJSON, headersJSON, err := marshalProbePlanPayload(plan)
	if err != nil {
		return nil, err
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO endpoint_probe_plans (name, enabled, mode, targets, headers, timeout_ms, interval_seconds, max_concurrency, next_run_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7, $8, $9, NOW(), NOW())
		RETURNING id, name, enabled, mode, targets, headers, timeout_ms, interval_seconds, max_concurrency, last_run_at, next_run_at, created_at, updated_at
	`, plan.Name, plan.Enabled, plan.Mode, targetsJSON, headersJSON, plan.TimeoutMs, plan.IntervalSeconds, plan.MaxConcurrency, plan.NextRunAt)
	return scanEndpointProbePlan(row)
}

func (r *endpointProbePlanRepository) Update(ctx context.Context, plan *service.EndpointProbePlan) (*service.EndpointProbePlan, error) {
	targetsJSON, headersJSON, err := marshalProbePlanPayload(plan)
	if err != nil {
		return nil, err
	}
	row := r.db.QueryRowContext(ctx, `
		UPDATE endpoint_probe_plans
		SET name=$2, enabled=$3, mode=$4, targets=$5::jsonb, headers=$6::jsonb, timeout_ms=$7, interval_seconds=$8, max_concurrency=$9, next_run_at=$10, updated_at=NOW()
		WHERE id=$1
		RETURNING id, name, enabled, mode, targets, headers, timeout_ms, interval_seconds, max_concurrency, last_run_at, next_run_at, created_at, updated_at
	`, plan.ID, plan.Name, plan.Enabled, plan.Mode, targetsJSON, headersJSON, plan.TimeoutMs, plan.IntervalSeconds, plan.MaxConcurrency, plan.NextRunAt)
	return scanEndpointProbePlan(row)
}

func (r *endpointProbePlanRepository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM endpoint_probe_plans WHERE id=$1`, id)
	return err
}

func (r *endpointProbePlanRepository) GetByID(ctx context.Context, id int64) (*service.EndpointProbePlan, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, enabled, mode, targets, headers, timeout_ms, interval_seconds, max_concurrency, last_run_at, next_run_at, created_at, updated_at
		FROM endpoint_probe_plans WHERE id=$1
	`, id)
	return scanEndpointProbePlan(row)
}

func (r *endpointProbePlanRepository) List(ctx context.Context) ([]*service.EndpointProbePlan, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, enabled, mode, targets, headers, timeout_ms, interval_seconds, max_concurrency, last_run_at, next_run_at, created_at, updated_at
		FROM endpoint_probe_plans
		ORDER BY id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanEndpointProbePlans(rows)
}

func (r *endpointProbePlanRepository) ListDue(ctx context.Context, now time.Time, limit int) ([]*service.EndpointProbePlan, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, enabled, mode, targets, headers, timeout_ms, interval_seconds, max_concurrency, last_run_at, next_run_at, created_at, updated_at
		FROM endpoint_probe_plans
		WHERE enabled = true AND next_run_at IS NOT NULL AND next_run_at <= $1
		ORDER BY next_run_at ASC
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanEndpointProbePlans(rows)
}

func (r *endpointProbePlanRepository) UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE endpoint_probe_plans SET last_run_at=$2, next_run_at=$3, updated_at=NOW()
		WHERE id=$1
	`, id, lastRunAt, nextRunAt)
	return err
}

type endpointProbeHistoryRepository struct {
	db *sql.DB
}

func NewEndpointProbeHistoryRepository(db *sql.DB) service.EndpointProbeHistoryRepository {
	return &endpointProbeHistoryRepository{db: db}
}

func (r *endpointProbeHistoryRepository) InsertBatch(ctx context.Context, planID int64, items []*service.EndpointProbeResult, probedAt time.Time) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO endpoint_probe_results (plan_id, target_url, mode, success, status_code, latency_ms, message, resolved_user_agent, probed_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
	`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for _, item := range items {
		if item == nil {
			continue
		}
		if _, err = stmt.ExecContext(
			ctx,
			planID,
			item.TargetURL,
			item.Mode,
			item.Success,
			item.StatusCode,
			item.LatencyMs,
			item.Message,
			item.ResolvedUA,
			probedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *endpointProbeHistoryRepository) ListByPlanID(ctx context.Context, planID int64, limit int) ([]*service.EndpointProbeHistory, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, plan_id, target_url, mode, success, status_code, latency_ms, message, resolved_user_agent, probed_at, created_at
		FROM endpoint_probe_results
		WHERE plan_id = $1
		ORDER BY id DESC
		LIMIT $2
	`, planID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var result []*service.EndpointProbeHistory
	for rows.Next() {
		item := &service.EndpointProbeHistory{}
		if err = rows.Scan(
			&item.ID, &item.PlanID, &item.TargetURL, &item.Mode, &item.Success,
			&item.StatusCode, &item.LatencyMs, &item.Message, &item.ResolvedUA,
			&item.ProbedAt, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *endpointProbeHistoryRepository) PruneByPlanID(ctx context.Context, planID int64, keepCount int) error {
	if keepCount <= 0 {
		keepCount = 2000
	}
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM endpoint_probe_results
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY plan_id ORDER BY id DESC) AS rn
				FROM endpoint_probe_results
				WHERE plan_id = $1
			) ranked
			WHERE rn > $2
		)
	`, planID, keepCount)
	return err
}

func marshalProbePlanPayload(plan *service.EndpointProbePlan) (targetsJSON string, headersJSON string, err error) {
	targets := plan.Targets
	if targets == nil {
		targets = []string{}
	}
	headers := plan.Headers
	if headers == nil {
		headers = map[string]string{}
	}

	targetsRaw, err := json.Marshal(targets)
	if err != nil {
		return "", "", fmt.Errorf("marshal targets: %w", err)
	}
	headersRaw, err := json.Marshal(headers)
	if err != nil {
		return "", "", fmt.Errorf("marshal headers: %w", err)
	}
	return string(targetsRaw), string(headersRaw), nil
}

type endpointProbePlanScannable interface {
	Scan(dest ...any) error
}

func scanEndpointProbePlan(row endpointProbePlanScannable) (*service.EndpointProbePlan, error) {
	var (
		targetsRaw []byte
		headersRaw []byte
		plan       service.EndpointProbePlan
	)
	if err := row.Scan(
		&plan.ID, &plan.Name, &plan.Enabled, &plan.Mode, &targetsRaw, &headersRaw,
		&plan.TimeoutMs, &plan.IntervalSeconds, &plan.MaxConcurrency,
		&plan.LastRunAt, &plan.NextRunAt, &plan.CreatedAt, &plan.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(targetsRaw) > 0 {
		_ = json.Unmarshal(targetsRaw, &plan.Targets)
	}
	if len(headersRaw) > 0 {
		_ = json.Unmarshal(headersRaw, &plan.Headers)
	}
	if plan.Targets == nil {
		plan.Targets = []string{}
	}
	if plan.Headers == nil {
		plan.Headers = map[string]string{}
	}
	return &plan, nil
}

func scanEndpointProbePlans(rows *sql.Rows) ([]*service.EndpointProbePlan, error) {
	var out []*service.EndpointProbePlan
	for rows.Next() {
		plan, err := scanEndpointProbePlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, plan)
	}
	return out, rows.Err()
}
