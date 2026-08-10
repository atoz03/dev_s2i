package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSDownstreamWriteContextPreservesClientLifecycleOnLeaseLoss(t *testing.T) {
	lifecycleCtx, cancelLifecycle := context.WithCancelCause(context.Background())
	defer cancelLifecycle(context.Canceled)
	controlCtx, cancelControl := context.WithCancelCause(lifecycleCtx)
	hooks := &OpenAIWSIngressHooks{ClientLifecycleContext: lifecycleCtx}

	writeCtx, cancelWrite := newOpenAIWSDownstreamWriteContext(controlCtx, hooks, time.Second)
	defer cancelWrite()
	cancelControl(errors.New("openai websocket ingress lease lost"))

	require.Error(t, controlCtx.Err())
	require.NoError(t, writeCtx.Err(), "lease cancellation must not interrupt the terminal event write")
	cancelLifecycle(context.Canceled)
	require.Eventually(t, func() bool { return writeCtx.Err() != nil }, time.Second, time.Millisecond)
}
