package repository

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildUpstreamTransportSetsDialTimeout(t *testing.T) {
	transport, err := buildUpstreamTransport(defaultPoolSettings(nil), nil)
	require.NoError(t, err)
	require.NotNil(t, transport.DialContext)
	require.Equal(t, defaultUpstreamTLSHandshakeTimeout, transport.TLSHandshakeTimeout)
}

func TestNewUpstreamDialerHasBoundedTimeout(t *testing.T) {
	dialer := newUpstreamDialer()
	require.Greater(t, dialer.Timeout, time.Duration(0))
	require.Equal(t, defaultUpstreamDialTimeout, dialer.Timeout)
	require.Equal(t, defaultUpstreamDialKeepAlive, dialer.KeepAlive)
}

func TestBuildUpstreamTransportKeepsDialTimeoutWithProxy(t *testing.T) {
	for _, rawURL := range []string{"http://127.0.0.1:1080", "socks5h://127.0.0.1:1080"} {
		t.Run(rawURL, func(t *testing.T) {
			proxyURL, err := url.Parse(rawURL)
			require.NoError(t, err)

			transport, err := buildUpstreamTransport(defaultPoolSettings(nil), proxyURL)
			require.NoError(t, err)
			require.NotNil(t, transport.DialContext)
		})
	}
}

func TestUpstreamDialerRespectsContextCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn, err := newUpstreamDialer().DialContext(ctx, "tcp", addr)
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
}
