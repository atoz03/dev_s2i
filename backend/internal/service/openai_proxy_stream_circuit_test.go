package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIProxyStreamCircuitThresholdTTLAndSuccessReset(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 2,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		maxEntries:       16,
	})

	tripped, _ := circuit.recordFailure(1, base)
	require.False(t, tripped)
	require.True(t, circuit.recordSuccess(1))
	tripped, _ = circuit.recordFailure(1, base.Add(10*time.Second))
	require.False(t, tripped)
	tripped, until := circuit.recordFailure(1, base.Add(20*time.Second))
	require.True(t, tripped)
	require.True(t, circuit.isBlocked(1, until.Add(-time.Nanosecond)))
	require.False(t, circuit.isBlocked(1, until))
}

func TestOpenAIProxyStreamCircuitCollapsesBurstFailures(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 2,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		collapseInterval: 3 * time.Second,
		maxEntries:       16,
	})

	tripped, _ := circuit.recordFailure(1, base)
	require.False(t, tripped)
	tripped, _ = circuit.recordFailure(1, base.Add(time.Second))
	require.False(t, tripped)
	tripped, _ = circuit.recordFailure(1, base.Add(5*time.Second))
	require.True(t, tripped)
}

func TestOpenAIProxyStreamCircuitDisabledAndBypass(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	disabled := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		disabled: true, failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: time.Minute, maxEntries: 16,
	})
	tripped, _ := disabled.recordFailure(1, base)
	require.False(t, tripped)

	proxyID := int64(7)
	account := &Account{ID: 1, Platform: PlatformOpenAI, ProxyID: &proxyID}
	svc := &OpenAIGatewayService{openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: time.Minute, maxEntries: 16,
	})}
	svc.openaiProxyStreamCircuit.recordFailure(proxyID, time.Now())
	require.True(t, svc.isOpenAIProxyStreamQuarantined(context.Background(), account))
	require.False(t, svc.isOpenAIProxyStreamQuarantined(withOpenAIProxyStreamQuarantineBypass(context.Background()), account))
}

func TestOpenAIProxyStreamCircuitActiveBlockCountAndBounds(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: 10 * time.Minute, maxEntries: 2,
	})
	circuit.recordFailure(1, base)
	circuit.recordFailure(2, base.Add(time.Second))
	require.Equal(t, 2, circuit.activeBlockCount(base.Add(2*time.Second)))
	circuit.recordFailure(3, base.Add(4*time.Second))
	circuit.mu.Lock()
	require.Len(t, circuit.entries, 2)
	_, oldestRetained := circuit.entries[1]
	circuit.mu.Unlock()
	require.False(t, oldestRetained)
}
