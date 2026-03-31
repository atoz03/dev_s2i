package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	EndpointProbeModeTCP  = "tcp"
	EndpointProbeModeHEAD = "head"
	EndpointProbeModeGET  = "get"
)

type EndpointProbeRequest struct {
	TargetURL string
	Mode      string
	TimeoutMs int
	Headers   map[string]string
}

type EndpointProbeResult struct {
	TargetURL   string            `json:"target_url"`
	Mode        string            `json:"mode"`
	Success     bool              `json:"success"`
	StatusCode  int               `json:"status_code,omitempty"`
	LatencyMs   int64             `json:"latency_ms"`
	ResolvedUA  string            `json:"resolved_user_agent,omitempty"`
	ResponseLen int               `json:"response_len,omitempty"`
	Message     string            `json:"message,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

type EndpointBatchProbeRequest struct {
	Targets        []string          `json:"targets"`
	Mode           string            `json:"mode"`
	TimeoutMs      int               `json:"timeout_ms"`
	Headers        map[string]string `json:"headers"`
	MaxConcurrency int               `json:"max_concurrency"`
}

type EndpointProbeService struct {
	httpClientFactory func(timeout time.Duration) *http.Client
	settingService    *SettingService
}

func NewEndpointProbeService(settingService *SettingService) *EndpointProbeService {
	return &EndpointProbeService{
		settingService: settingService,
		httpClientFactory: func(timeout time.Duration) *http.Client {
			return &http.Client{Timeout: timeout}
		},
	}
}

func (s *EndpointProbeService) Probe(ctx context.Context, req EndpointProbeRequest) (*EndpointProbeResult, error) {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = EndpointProbeModeHEAD
	}
	if mode != EndpointProbeModeTCP && mode != EndpointProbeModeHEAD && mode != EndpointProbeModeGET {
		return nil, fmt.Errorf("unsupported probe mode: %s", mode)
	}
	timeout := 5 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	if timeout < 200*time.Millisecond {
		timeout = 200 * time.Millisecond
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	target := strings.TrimSpace(req.TargetURL)
	if target == "" {
		return nil, errors.New("target_url is required")
	}

	if mode == EndpointProbeModeTCP {
		return s.probeTCP(probeCtx, target)
	}
	return s.probeHTTP(probeCtx, target, mode, req.Headers)
}

func (s *EndpointProbeService) ProbeBatch(ctx context.Context, req EndpointBatchProbeRequest) ([]*EndpointProbeResult, error) {
	if len(req.Targets) == 0 {
		return nil, errors.New("targets is required")
	}
	maxConcurrency := req.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = 4
	}
	if maxConcurrency > 32 {
		maxConcurrency = 32
	}

	normalizedTargets := make([]string, 0, len(req.Targets))
	seen := make(map[string]struct{}, len(req.Targets))
	for _, target := range req.Targets {
		t := strings.TrimSpace(target)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		normalizedTargets = append(normalizedTargets, t)
	}
	if len(normalizedTargets) == 0 {
		return nil, errors.New("targets is empty")
	}

	type item struct {
		index  int
		result *EndpointProbeResult
	}
	results := make([]*EndpointProbeResult, len(normalizedTargets))
	sem := make(chan struct{}, maxConcurrency)
	out := make(chan item, len(normalizedTargets))
	var wg sync.WaitGroup

	for i, target := range normalizedTargets {
		wg.Add(1)
		go func(index int, url string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result, err := s.Probe(ctx, EndpointProbeRequest{
				TargetURL: url,
				Mode:      req.Mode,
				TimeoutMs: req.TimeoutMs,
				Headers:   req.Headers,
			})
			if err != nil {
				out <- item{
					index: index,
					result: &EndpointProbeResult{
						TargetURL: url,
						Mode:      strings.ToLower(strings.TrimSpace(req.Mode)),
						Success:   false,
						Message:   err.Error(),
					},
				}
				return
			}
			out <- item{index: index, result: result}
		}(i, target)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	for it := range out {
		results[it.index] = it.result
	}

	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a == nil || b == nil {
			return a != nil
		}
		if a.Success != b.Success {
			return a.Success
		}
		if a.LatencyMs != b.LatencyMs {
			return a.LatencyMs < b.LatencyMs
		}
		return a.TargetURL < b.TargetURL
	})
	return results, nil
}

func (s *EndpointProbeService) probeTCP(ctx context.Context, target string) (*EndpointProbeResult, error) {
	address := strings.TrimSpace(target)
	if u, err := neturl.Parse(address); err == nil && u.Host != "" {
		address = u.Host
	}
	if !strings.Contains(address, ":") {
		address += ":443"
	}
	start := time.Now()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return &EndpointProbeResult{
			TargetURL: target,
			Mode:      EndpointProbeModeTCP,
			Success:   false,
			LatencyMs: maxInt64Floor(latency, 0),
			Message:   err.Error(),
		}, nil
	}
	_ = conn.Close()
	return &EndpointProbeResult{
		TargetURL: target,
		Mode:      EndpointProbeModeTCP,
		Success:   true,
		LatencyMs: maxInt64Floor(latency, 0),
		Message:   "ok",
	}, nil
}

func (s *EndpointProbeService) probeHTTP(ctx context.Context, target, mode string, headers map[string]string) (*EndpointProbeResult, error) {
	parsed, err := neturl.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("target_url must be a valid http/https URL")
	}
	method := http.MethodHead
	if mode == EndpointProbeModeGET {
		method = http.MethodGet
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, parsed.String(), nil)
	if err != nil {
		return nil, err
	}

	resolvedUA := strings.TrimSpace(headers["User-Agent"])
	if resolvedUA == "" {
		resolvedUA = strings.TrimSpace(headers["user-agent"])
	}
	if s != nil && s.settingService != nil {
		defaultUA, forceUnified := s.settingService.GetUpstreamUserAgentSettings(ctx)
		defaultUA = strings.TrimSpace(defaultUA)
		switch {
		case forceUnified && defaultUA != "":
			resolvedUA = defaultUA
		case resolvedUA == "" && defaultUA != "":
			resolvedUA = defaultUA
		}
	}
	if resolvedUA == "" {
		resolvedUA = "sub2api-endpoint-probe/1.0"
	}
	httpReq.Header.Set("User-Agent", resolvedUA)
	for k, v := range headers {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		if strings.EqualFold(key, "user-agent") {
			continue
		}
		httpReq.Header.Set(key, strings.TrimSpace(v))
	}

	timeout := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
		if timeout <= 0 {
			timeout = 100 * time.Millisecond
		}
	}
	client := s.httpClientFactory(timeout)
	start := time.Now()
	resp, err := client.Do(httpReq)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return &EndpointProbeResult{
			TargetURL:  target,
			Mode:       mode,
			Success:    false,
			LatencyMs:  maxInt64Floor(latency, 0),
			ResolvedUA: resolvedUA,
			Message:    err.Error(),
		}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	readBytes := 0
	if mode == EndpointProbeModeGET {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*64))
		readBytes = len(body)
	}
	result := &EndpointProbeResult{
		TargetURL:   target,
		Mode:        mode,
		Success:     resp.StatusCode >= 200 && resp.StatusCode < 400,
		StatusCode:  resp.StatusCode,
		LatencyMs:   maxInt64Floor(latency, 0),
		ResolvedUA:  resolvedUA,
		ResponseLen: readBytes,
		Message:     resp.Status,
		Headers: map[string]string{
			"content-type": strings.TrimSpace(resp.Header.Get("Content-Type")),
			"server":       strings.TrimSpace(resp.Header.Get("Server")),
		},
	}
	return result, nil
}

func maxInt64Floor(v, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}
