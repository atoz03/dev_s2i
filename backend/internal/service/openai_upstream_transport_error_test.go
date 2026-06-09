package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClassifyOpenAITransportError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		persistent bool
	}{
		{name: "nil", err: nil, persistent: false},
		{name: "connection refused", err: syscall.ECONNREFUSED, persistent: true},
		{name: "dns not found", err: &net.DNSError{IsNotFound: true}, persistent: true},
		{name: "marker", err: fmt.Errorf("proxy error: no such host"), persistent: true},
		{name: "transient", err: fmt.Errorf("unexpected EOF"), persistent: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyOpenAITransportError(tt.err)
			require.Equal(t, tt.persistent, got.Persistent)
		})
	}
}

func TestHandleOpenAIUpstreamTransportError_ReturnsFailoverAndBlocksPersistentError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	account := &Account{ID: 7, Name: "openai", Platform: PlatformOpenAI}
	svc := &OpenAIGatewayService{}

	err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account, fmt.Errorf("dial tcp: connection refused"), false)

	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "Upstream request failed")
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))

	msg, ok := c.Get(OpsUpstreamErrorMessageKey)
	require.True(t, ok)
	msgText, ok := msg.(string)
	require.True(t, ok)
	require.Contains(t, msgText, "connection refused")
	require.Empty(t, rec.Body.String())
}

func TestHandleOpenAIUpstreamTransportError_ClientCancelSkipsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	account := &Account{ID: 8, Name: "openai", Platform: PlatformOpenAI}
	svc := &OpenAIGatewayService{}

	err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account, context.Canceled, false)

	require.ErrorIs(t, err, context.Canceled)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Empty(t, rec.Body.String())
}
