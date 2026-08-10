package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAIWSIngressLeaseCacheStub struct {
	ConcurrencyCache

	mu       sync.Mutex
	owned    bool
	released bool
}

func (c *openAIWSIngressLeaseCacheStub) AcquireOpenAIWSIngressLease(context.Context, int64, int, string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owned {
		return false, nil
	}
	c.owned = true
	return true, nil
}

func (c *openAIWSIngressLeaseCacheStub) RefreshOpenAIWSIngressLease(context.Context, int64, string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.owned, nil
}

func (c *openAIWSIngressLeaseCacheStub) ReleaseOpenAIWSIngressLease(context.Context, int64, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.owned = false
	c.released = true
	return nil
}

func TestAcquireOpenAIWSIngressLeaseLifecycle(t *testing.T) {
	cache := &openAIWSIngressLeaseCacheStub{}
	svc := NewConcurrencyService(cache)
	lease, acquired, err := svc.AcquireOpenAIWSIngressLease(context.Background(), 42, 1)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, lease)
	require.NoError(t, lease.Context().Err())

	lease.Release()
	require.Error(t, lease.Context().Err())
	cache.mu.Lock()
	require.True(t, cache.released)
	cache.mu.Unlock()
}

func TestAcquireOpenAIWSIngressLeaseDisabled(t *testing.T) {
	lease, acquired, err := NewConcurrencyService(nil).AcquireOpenAIWSIngressLease(context.Background(), 42, 0)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Nil(t, lease)
}
