package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const endpointProbeRunnerTickInterval = 15 * time.Second

type EndpointProbeRunnerService struct {
	planService *EndpointProbePlanService

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

func NewEndpointProbeRunnerService(planService *EndpointProbePlanService) *EndpointProbeRunnerService {
	return &EndpointProbeRunnerService{
		planService: planService,
		stopCh:      make(chan struct{}),
	}
}

func (s *EndpointProbeRunnerService) Start() {
	if s == nil || s.planService == nil {
		return
	}
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go s.runLoop()
		logger.LegacyPrintf("service.endpoint_probe_runner", "[EndpointProbeRunner] started")
	})
}

func (s *EndpointProbeRunnerService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.wg.Wait()
	})
}

func (s *EndpointProbeRunnerService) runLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(endpointProbeRunnerTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if err := s.planService.ExecuteDuePlans(ctx, 20); err != nil {
				logger.LegacyPrintf("service.endpoint_probe_runner", "[EndpointProbeRunner] execute due plans error: %v", err)
			}
			cancel()
		case <-s.stopCh:
			return
		}
	}
}
