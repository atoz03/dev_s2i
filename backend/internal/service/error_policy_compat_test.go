//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const antigravityMaxRetries = 3

type AntigravityGatewayService struct {
	rateLimitService *RateLimitService
}

type handleModelRateLimitResult struct{}

type antigravityRetryLoopParams struct {
	ctx             context.Context
	prefix          string
	account         *Account
	accessToken     string
	action          string
	body            []byte
	httpUpstream    HTTPUpstream
	requestedModel  string
	groupID         int64
	sessionHash     string
	isStickySession bool
	handleError     func(context.Context, string, *Account, int, http.Header, []byte, string, int64, string, bool) *handleModelRateLimitResult
}

type antigravityRetryLoopResult struct {
	resp *http.Response
}

type AntigravityAccountSwitchError struct {
	OriginalAccountID int64
}

func (e *AntigravityAccountSwitchError) Error() string {
	return fmt.Sprintf("switch antigravity account %d", e.OriginalAccountID)
}

func cloneHTTPResponse(statusCode int, headers http.Header, body []byte) *http.Response {
	clonedHeaders := make(http.Header, len(headers))
	for key, values := range headers {
		clonedHeaders[key] = append([]string(nil), values...)
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     clonedHeaders,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func (s *AntigravityGatewayService) applyErrorPolicy(
	params antigravityRetryLoopParams,
	statusCode int,
	headers http.Header,
	body []byte,
) (bool, int, error) {
	if s == nil || s.rateLimitService == nil || params.account == nil {
		return false, statusCode, nil
	}

	ctx := params.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	switch s.rateLimitService.CheckErrorPolicy(ctx, params.account, statusCode, body) {
	case ErrorPolicyNone:
		return false, statusCode, nil
	case ErrorPolicySkipped:
		return true, http.StatusInternalServerError, nil
	case ErrorPolicyMatched:
		s.rateLimitService.HandleUpstreamError(ctx, params.account, statusCode, headers, body)
		if params.handleError != nil {
			params.handleError(
				ctx,
				params.prefix,
				params.account,
				statusCode,
				headers,
				body,
				params.requestedModel,
				params.groupID,
				params.sessionHash,
				params.isStickySession,
			)
		}
		return true, statusCode, nil
	case ErrorPolicyTempUnscheduled:
		s.rateLimitService.HandleUpstreamError(ctx, params.account, statusCode, headers, body)
		return true, statusCode, &AntigravityAccountSwitchError{OriginalAccountID: params.account.ID}
	default:
		return false, statusCode, nil
	}
}

func (s *AntigravityGatewayService) antigravityRetryLoop(
	params antigravityRetryLoopParams,
) (*antigravityRetryLoopResult, error) {
	ctx := params.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if params.httpUpstream == nil {
		return nil, fmt.Errorf("httpUpstream is nil")
	}

	var lastStatus int
	var lastHeaders http.Header
	var lastBody []byte

	for attempt := 0; attempt < antigravityMaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://antigravity.test", bytes.NewReader(params.body))
		if err != nil {
			return nil, err
		}

		resp, err := params.httpUpstream.Do(req, "", params.account.ID, params.account.Concurrency)
		if err != nil {
			return nil, err
		}

		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}

		lastStatus = resp.StatusCode
		lastHeaders = resp.Header
		lastBody = body

		handled, outStatus, retErr := s.applyErrorPolicy(params, resp.StatusCode, resp.Header, body)
		if handled {
			if retErr != nil {
				return nil, retErr
			}
			return &antigravityRetryLoopResult{resp: cloneHTTPResponse(outStatus, resp.Header, body)}, nil
		}

		if attempt == antigravityMaxRetries-1 {
			break
		}
		if err := sleepWithContext(ctx, time.Second); err != nil {
			return nil, err
		}
	}

	if params.handleError != nil {
		params.handleError(
			ctx,
			params.prefix,
			params.account,
			lastStatus,
			lastHeaders,
			lastBody,
			params.requestedModel,
			params.groupID,
			params.sessionHash,
			params.isStickySession,
		)
	}

	return &antigravityRetryLoopResult{
		resp: cloneHTTPResponse(lastStatus, lastHeaders, lastBody),
	}, nil
}
