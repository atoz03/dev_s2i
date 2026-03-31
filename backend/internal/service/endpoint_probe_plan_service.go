package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultEndpointProbeIntervalSeconds = 60
	defaultEndpointProbeTimeoutMs       = 5000
	defaultEndpointProbeConcurrency     = 4
	defaultEndpointProbeMaxHistory      = 2000
)

type EndpointProbePlanService struct {
	planRepo    EndpointProbePlanRepository
	historyRepo EndpointProbeHistoryRepository
	probeSvc    *EndpointProbeService
}

func NewEndpointProbePlanService(
	planRepo EndpointProbePlanRepository,
	historyRepo EndpointProbeHistoryRepository,
	probeSvc *EndpointProbeService,
) *EndpointProbePlanService {
	return &EndpointProbePlanService{
		planRepo:    planRepo,
		historyRepo: historyRepo,
		probeSvc:    probeSvc,
	}
}

func (s *EndpointProbePlanService) CreatePlan(ctx context.Context, plan *EndpointProbePlan) (*EndpointProbePlan, error) {
	if err := s.normalizePlan(plan); err != nil {
		return nil, err
	}
	nextRun := time.Now().Add(time.Duration(plan.IntervalSeconds) * time.Second)
	plan.NextRunAt = &nextRun
	return s.planRepo.Create(ctx, plan)
}

func (s *EndpointProbePlanService) UpdatePlan(ctx context.Context, plan *EndpointProbePlan) (*EndpointProbePlan, error) {
	if plan == nil || plan.ID <= 0 {
		return nil, errors.New("invalid plan id")
	}
	if err := s.normalizePlan(plan); err != nil {
		return nil, err
	}
	if plan.Enabled {
		nextRun := time.Now().Add(time.Duration(plan.IntervalSeconds) * time.Second)
		plan.NextRunAt = &nextRun
	} else {
		plan.NextRunAt = nil
	}
	return s.planRepo.Update(ctx, plan)
}

func (s *EndpointProbePlanService) DeletePlan(ctx context.Context, id int64) error {
	return s.planRepo.Delete(ctx, id)
}

func (s *EndpointProbePlanService) GetPlan(ctx context.Context, id int64) (*EndpointProbePlan, error) {
	return s.planRepo.GetByID(ctx, id)
}

func (s *EndpointProbePlanService) ListPlans(ctx context.Context) ([]*EndpointProbePlan, error) {
	return s.planRepo.List(ctx)
}

func (s *EndpointProbePlanService) ListResults(ctx context.Context, planID int64, limit int) ([]*EndpointProbeHistory, error) {
	return s.historyRepo.ListByPlanID(ctx, planID, limit)
}

func (s *EndpointProbePlanService) RunNow(ctx context.Context, planID int64) ([]*EndpointProbeResult, error) {
	plan, err := s.planRepo.GetByID(ctx, planID)
	if err != nil {
		return nil, err
	}
	results, err := s.runPlanProbe(ctx, plan)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	nextRun := now.Add(time.Duration(plan.IntervalSeconds) * time.Second)
	if err = s.planRepo.UpdateAfterRun(ctx, plan.ID, now, nextRun); err != nil {
		return results, err
	}
	return results, nil
}

func (s *EndpointProbePlanService) ExecuteDuePlans(ctx context.Context, limit int) error {
	plans, err := s.planRepo.ListDue(ctx, time.Now(), limit)
	if err != nil {
		return err
	}
	for _, plan := range plans {
		_, runErr := s.runPlanProbe(ctx, plan)
		now := time.Now()
		nextRun := now.Add(time.Duration(plan.IntervalSeconds) * time.Second)
		if !plan.Enabled {
			nextRun = now
		}
		updateErr := s.planRepo.UpdateAfterRun(ctx, plan.ID, now, nextRun)
		if runErr != nil {
			return runErr
		}
		if updateErr != nil {
			return updateErr
		}
	}
	return nil
}

func (s *EndpointProbePlanService) runPlanProbe(ctx context.Context, plan *EndpointProbePlan) ([]*EndpointProbeResult, error) {
	if s == nil || s.probeSvc == nil {
		return nil, errors.New("endpoint probe service not configured")
	}
	results, err := s.probeSvc.ProbeBatch(ctx, EndpointBatchProbeRequest{
		Targets:        plan.Targets,
		Mode:           plan.Mode,
		TimeoutMs:      plan.TimeoutMs,
		Headers:        plan.Headers,
		MaxConcurrency: plan.MaxConcurrency,
	})
	if err != nil {
		return nil, err
	}
	if err = s.historyRepo.InsertBatch(ctx, plan.ID, results, time.Now()); err != nil {
		return nil, err
	}
	_ = s.historyRepo.PruneByPlanID(ctx, plan.ID, defaultEndpointProbeMaxHistory)
	return results, nil
}

func (s *EndpointProbePlanService) normalizePlan(plan *EndpointProbePlan) error {
	if plan == nil {
		return errors.New("plan is required")
	}
	plan.Name = strings.TrimSpace(plan.Name)
	if plan.Name == "" {
		return errors.New("name is required")
	}
	if len(plan.Targets) == 0 {
		return errors.New("targets is required")
	}
	mode := strings.ToLower(strings.TrimSpace(plan.Mode))
	if mode == "" {
		mode = EndpointProbeModeHEAD
	}
	if mode != EndpointProbeModeTCP && mode != EndpointProbeModeHEAD && mode != EndpointProbeModeGET {
		return fmt.Errorf("invalid mode: %s", mode)
	}
	plan.Mode = mode

	targets := make([]string, 0, len(plan.Targets))
	seen := make(map[string]struct{}, len(plan.Targets))
	for _, raw := range plan.Targets {
		target := strings.TrimSpace(raw)
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return errors.New("targets is empty")
	}
	plan.Targets = targets

	if plan.Headers == nil {
		plan.Headers = map[string]string{}
	}
	if plan.TimeoutMs <= 0 {
		plan.TimeoutMs = defaultEndpointProbeTimeoutMs
	}
	if plan.IntervalSeconds <= 0 {
		plan.IntervalSeconds = defaultEndpointProbeIntervalSeconds
	}
	if plan.MaxConcurrency <= 0 {
		plan.MaxConcurrency = defaultEndpointProbeConcurrency
	}
	if plan.MaxConcurrency > 32 {
		plan.MaxConcurrency = 32
	}
	return nil
}
