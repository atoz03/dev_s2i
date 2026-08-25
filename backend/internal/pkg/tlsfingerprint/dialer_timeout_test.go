package tlsfingerprint

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

// 零值 net.Dialer 的 Timeout 为 0，DNS 解析与 TCP 握手没有任何上限，
// 上游/代理被解析到不可达 IP 时只能等内核 TCP 重传耗尽（Linux 约 130 秒）。
func TestNewBaseDialerHasConnectTimeout(t *testing.T) {
	d := newBaseDialer()
	require.Equal(t, dialTimeout, d.Timeout, "建连超时必须显式设置，零值意味着无上限")
	require.Positive(t, d.Timeout)
	require.Equal(t, dialKeepAlive, d.KeepAlive)
}

// NewDialer 的 baseDialer 缺省分支同样不能退化成零值 dialer。
// 用已取消的 ctx 断言，避免依赖测试环境的网络可达性。
func TestNewDialerDefaultBaseDialerRespectsContext(t *testing.T) {
	d := NewDialer(&Profile{}, nil)
	require.NotNil(t, d.baseDialer)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	conn, err := d.baseDialer(ctx, "tcp", "198.51.100.1:443")
	if conn != nil {
		_ = conn.Close()
	}

	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 2*time.Second,
		"缺省 baseDialer 必须尊重 ctx，而不是一路等到内核 TCP 重传结束")
}

// 显式传入的 baseDialer 不得被缺省分支覆盖。
func TestNewDialerKeepsProvidedBaseDialer(t *testing.T) {
	stub := &plainDialerStub{}
	d := NewDialer(&Profile{}, func(_ context.Context, network, addr string) (net.Conn, error) {
		return stub.Dial(network, addr)
	})

	_, err := d.baseDialer(context.Background(), "tcp", "example.invalid:443")

	require.Error(t, err)
	require.True(t, stub.called)
}

// SOCKS5 分支必须走 DialContext：proxy.Dialer.Dial 完全忽略 ctx，
// 调用方的取消与超时都传不进去。
func TestDialSOCKS5ContextPrefersContextDialer(t *testing.T) {
	proxyURL, err := url.Parse("socks5://198.51.100.1:1080")
	require.NoError(t, err)

	socksDialer, err := proxy.SOCKS5("tcp", proxyURL.Host, nil, newBaseDialer())
	require.NoError(t, err)
	require.Implements(t, (*proxy.ContextDialer)(nil), socksDialer,
		"x/net/proxy 的 SOCKS5 dialer 必须实现 ContextDialer，否则 ctx 传不进隧道建连")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立刻取消：走 DialContext 才会即时返回

	start := time.Now()
	conn, err := dialSOCKS5Context(ctx, socksDialer, "example.invalid:443")
	if conn != nil {
		_ = conn.Close()
	}

	require.Error(t, err)
	require.Less(t, time.Since(start), 2*time.Second, "已取消的 ctx 必须立即生效")
}

// 断言 dialSOCKS5Context 对不支持 ContextDialer 的实现仍能回退，避免接口断言失败时静默 panic。
func TestDialSOCKS5ContextFallsBackToDial(t *testing.T) {
	fallback := &plainDialerStub{}

	_, err := dialSOCKS5Context(context.Background(), fallback, "example.invalid:443")

	require.Error(t, err)
	require.True(t, fallback.called, "不实现 ContextDialer 时必须回退到 Dial")
}

type plainDialerStub struct{ called bool }

func (d *plainDialerStub) Dial(_, _ string) (net.Conn, error) {
	d.called = true
	return nil, net.ErrClosed
}
