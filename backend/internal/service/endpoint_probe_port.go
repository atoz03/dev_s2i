package service

import (
	"context"
	"time"
)

type EndpointProbePlan struct {
	ID              int64             `json:"id"`
	Name            string            `json:"name"`
	Enabled         bool              `json:"enabled"`
	Mode            string            `json:"mode"`
	Targets         []string          `json:"targets"`
	Headers         map[string]string `json:"headers"`
	TimeoutMs       int               `json:"timeout_ms"`
	IntervalSeconds int               `json:"interval_seconds"`
	MaxConcurrency  int               `json:"max_concurrency"`
	LastRunAt       *time.Time        `json:"last_run_at"`
	NextRunAt       *time.Time        `json:"next_run_at"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type EndpointProbeHistory struct {
	ID         int64     `json:"id"`
	PlanID     int64     `json:"plan_id"`
	TargetURL  string    `json:"target_url"`
	Mode       string    `json:"mode"`
	Success    bool      `json:"success"`
	StatusCode int       `json:"status_code,omitempty"`
	LatencyMs  int64     `json:"latency_ms"`
	Message    string    `json:"message,omitempty"`
	ResolvedUA string    `json:"resolved_user_agent,omitempty"`
	ProbedAt   time.Time `json:"probed_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type EndpointProbePlanRepository interface {
	Create(ctx context.Context, plan *EndpointProbePlan) (*EndpointProbePlan, error)
	Update(ctx context.Context, plan *EndpointProbePlan) (*EndpointProbePlan, error)
	Delete(ctx context.Context, id int64) error
	GetByID(ctx context.Context, id int64) (*EndpointProbePlan, error)
	List(ctx context.Context) ([]*EndpointProbePlan, error)
	ListDue(ctx context.Context, now time.Time, limit int) ([]*EndpointProbePlan, error)
	UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error
}

type EndpointProbeHistoryRepository interface {
	InsertBatch(ctx context.Context, planID int64, items []*EndpointProbeResult, probedAt time.Time) error
	ListByPlanID(ctx context.Context, planID int64, limit int) ([]*EndpointProbeHistory, error)
	PruneByPlanID(ctx context.Context, planID int64, keepCount int) error
}
