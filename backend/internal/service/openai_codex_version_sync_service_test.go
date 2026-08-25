//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// ---- test doubles ----

type codexVersionSyncSettingRepo struct {
	mu        sync.Mutex
	values    map[string]string
	updatedAt map[string]time.Time
	getErr    error
	setErr    error
	setCalls  int
}

func newCodexVersionSyncSettingRepo() *codexVersionSyncSettingRepo {
	return &codexVersionSyncSettingRepo{
		values:    map[string]string{},
		updatedAt: map[string]time.Time{},
	}
}

func (r *codexVersionSyncSettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	value, ok := r.values[key]
	if !ok {
		return nil, nil
	}
	return &Setting{Key: key, Value: value, UpdatedAt: r.updatedAt[key]}, nil
}

func (r *codexVersionSyncSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return "", r.getErr
	}
	return r.values[key], nil
}

func (r *codexVersionSyncSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setErr != nil {
		return r.setErr
	}
	r.setCalls++
	r.values[key] = value
	r.updatedAt[key] = time.Now()
	return nil
}

func (r *codexVersionSyncSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, nil
}
func (r *codexVersionSyncSettingRepo) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (r *codexVersionSyncSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return nil, nil
}
func (r *codexVersionSyncSettingRepo) Delete(context.Context, string) error { return nil }

func (r *codexVersionSyncSettingRepo) synced() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.values[SettingKeyOpenAICodexClientVersionSynced]
}

type codexVersionSyncGitHubClient struct {
	mu           sync.Mutex
	latest       *GitHubRelease
	latestErr    error
	list         []*GitHubRelease
	listErr      error
	latestCalls  int
	listCalls    int
	returnedList bool
}

func (c *codexVersionSyncGitHubClient) FetchLatestRelease(context.Context, string) (*GitHubRelease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latestCalls++
	return c.latest, c.latestErr
}

func (c *codexVersionSyncGitHubClient) FetchRecentReleases(context.Context, string, int) ([]*GitHubRelease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listCalls++
	c.returnedList = true
	return c.list, c.listErr
}

func (c *codexVersionSyncGitHubClient) DownloadFile(context.Context, string, string, int64) error {
	return nil
}
func (c *codexVersionSyncGitHubClient) FetchChecksumFile(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (c *codexVersionSyncGitHubClient) calls() (latest, list int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latestCalls, c.listCalls
}

func stable(tag string) *GitHubRelease { return &GitHubRelease{TagName: tag} }

// resetCodexResolvedVersionHighWater 重置进程级高水位，隔离同包用例之间的相互污染。
func resetCodexResolvedVersionHighWater() {
	codexResolvedVersionHighWater.Store("")
}

// ---- version normalization / resolution ----

func TestNormalizeCodexClientVersion(t *testing.T) {
	// 首尾空白（含换行）先被裁掉再校验。
	require.Equal(t, "0.149.1", NormalizeCodexClientVersion(" 0.149.1 "))
	require.Equal(t, "0.149.1", NormalizeCodexClientVersion("0.149.1\n"))
	require.Equal(t, "0.150.0-alpha.4", NormalizeCodexClientVersion("0.150.0-alpha.4"))
	// 拒绝任意字节：该值会被拼进出站 UA 与 version 头。
	for _, bad := range []string{
		"", "abc", "0", "v0.149.1", "0.149.1; rm -rf /", "0.149\n.1",
		"0.149.1 (evil)", "0.149.1\r\nX-Injected: 1", string(make([]byte, 80)),
	} {
		require.Empty(t, NormalizeCodexClientVersion(bad), "should reject %q", bad)
	}
}

// TestCompareVersionsNumericSegments 字典序会把 0.99.0 判成大于 0.146.0，
// 从而同时破坏「取最大值」与「只向前推进」两处判断。
func TestCompareVersionsNumericSegments(t *testing.T) {
	require.Equal(t, -1, CompareVersions("0.99.0", "0.146.0"))
	require.Equal(t, 1, CompareVersions("0.149.1", "0.149.0"))
	require.Equal(t, 0, CompareVersions("0.149.1", "0.149.1"))
}

func TestResolveCodexClientVersion(t *testing.T) {
	t.Cleanup(func() {
		SetCodexSyncedVersionProvider(nil)
		resetCodexResolvedVersionHighWater()
	})
	resetCodexResolvedVersionHighWater()

	// 未注入 provider：使用编译期常量。
	SetCodexSyncedVersionProvider(nil)
	require.Equal(t, codexCLIVersion, resolveCodexClientVersion())
	require.Equal(t, codexCLIUserAgent, codexCanonicalUserAgent())

	// 同步值更高：生效，并重建 UA。
	SetCodexSyncedVersionProvider(func() string { return "9.9.9" })
	require.Equal(t, "9.9.9", resolveCodexClientVersion())
	require.Contains(t, codexCanonicalUserAgent(), "codex_cli_rs/9.9.9")
	require.Contains(t, codexCanonicalUserAgent(), codexCLIUserAgentSuffix)

	// 读取侧只向前推进：provider 因数据库抖动返回空/非法/更低值时，
	// 保持已生效过的最高版本，不在一个 TTL 内退回常量。
	for _, v := range []string{"0.1.0", "garbage", ""} {
		SetCodexSyncedVersionProvider(func() string { return v })
		require.Equal(t, "9.9.9", resolveCodexClientVersion(), "synced=%q 时不应退回", v)
	}

	// 清掉高水位后才回落到编译期常量地板。
	resetCodexResolvedVersionHighWater()
	SetCodexSyncedVersionProvider(func() string { return "0.1.0" })
	require.Equal(t, codexCLIVersion, resolveCodexClientVersion())
}

// ---- release filtering ----

func TestLatestCodexStableReleaseVersion(t *testing.T) {
	t.Run("按前缀过滤并取最大值", func(t *testing.T) {
		got := latestCodexStableReleaseVersion([]*GitHubRelease{
			stable("rust-v0.146.0"),
			stable("rusty-v8-9.9.9"), // 同仓库其他组件
			stable("rust-v0.149.1"),
			stable("rust-v0.99.0"), // 字典序陷阱
		})
		require.Equal(t, "0.149.1", got)
	})

	t.Run("排除草稿与预发布", func(t *testing.T) {
		got := latestCodexStableReleaseVersion([]*GitHubRelease{
			{TagName: "rust-v0.150.0", Draft: true},
			{TagName: "rust-v0.151.0", Prerelease: true},
			stable("rust-v0.149.1"),
		})
		require.Equal(t, "0.149.1", got)
	})

	t.Run("排除带后缀与畸形 tag", func(t *testing.T) {
		require.Empty(t, latestCodexStableReleaseVersion([]*GitHubRelease{
			stable("rust-v0.150.0-alpha.8"),
			stable("rust-vv0.99.0-alpha.8"),
			stable("rust-vnot-a-version"),
			nil,
		}))
	})
}

// ---- sync loop ----

func newSyncSvc(repo SettingRepository, gh GitHubReleaseClient) *OpenAICodexVersionSyncService {
	return NewOpenAICodexVersionSyncService(repo, gh, openAICodexVersionSyncInterval, true)
}

func TestCodexVersionSyncRunOnce(t *testing.T) {
	t.Run("主路径命中时不再拉列表页", func(t *testing.T) {
		repo := newCodexVersionSyncSettingRepo()
		gh := &codexVersionSyncGitHubClient{latest: stable("rust-v0.149.1")}
		newSyncSvc(repo, gh).runOnce()

		latestCalls, listCalls := gh.calls()
		require.Equal(t, 1, latestCalls)
		require.Equal(t, 0, listCalls, "主路径命中不应再拉 10MB 的列表页")
		require.Equal(t, "0.149.1", repo.synced())
	})

	t.Run("latest 是异家族时回退列表扫描", func(t *testing.T) {
		repo := newCodexVersionSyncSettingRepo()
		gh := &codexVersionSyncGitHubClient{
			latest: stable("rusty-v8-9.9.9"),
			list:   []*GitHubRelease{stable("rust-v0.149.1")},
		}
		newSyncSvc(repo, gh).runOnce()

		_, listCalls := gh.calls()
		require.Equal(t, 1, listCalls)
		require.Equal(t, "0.149.1", repo.synced())
	})

	t.Run("latest 抓取失败时回退列表扫描", func(t *testing.T) {
		repo := newCodexVersionSyncSettingRepo()
		gh := &codexVersionSyncGitHubClient{
			latestErr: errors.New("boom"),
			list:      []*GitHubRelease{stable("rust-v0.149.1")},
		}
		newSyncSvc(repo, gh).runOnce()
		require.Equal(t, "0.149.1", repo.synced())
	})

	t.Run("两条路径都失败时保持既有值", func(t *testing.T) {
		repo := newCodexVersionSyncSettingRepo()
		require.NoError(t, repo.Set(context.Background(), SettingKeyOpenAICodexClientVersionSynced, "0.149.1"))
		gh := &codexVersionSyncGitHubClient{latestErr: errors.New("boom"), listErr: errors.New("boom")}
		newSyncSvc(repo, gh).runOnce()
		require.Equal(t, "0.149.1", repo.synced(), "抓取失败不得清空或降级")
	})

	t.Run("只向前推进，不降级", func(t *testing.T) {
		repo := newCodexVersionSyncSettingRepo()
		require.NoError(t, repo.Set(context.Background(), SettingKeyOpenAICodexClientVersionSynced, "0.149.1"))
		before := repo.setCalls
		gh := &codexVersionSyncGitHubClient{latest: stable("rust-v0.146.0")}
		newSyncSvc(repo, gh).runOnce()
		require.Equal(t, "0.149.1", repo.synced())
		require.Equal(t, before, repo.setCalls, "更旧的版本不应触发写入")
	})
}

func TestCodexVersionSyncStartupDebounce(t *testing.T) {
	t.Run("周期内已同步则跳过启动同步", func(t *testing.T) {
		repo := newCodexVersionSyncSettingRepo()
		require.NoError(t, repo.Set(context.Background(), SettingKeyOpenAICodexClientVersionSynced, "0.149.1"))
		gh := &codexVersionSyncGitHubClient{latest: stable("rust-v0.150.0")}
		newSyncSvc(repo, gh).runInitial()

		latestCalls, _ := gh.calls()
		require.Zero(t, latestCalls, "滚动发布/崩溃重启不应放大成对 GitHub 的连续请求")
	})

	t.Run("同步值陈旧则执行启动同步", func(t *testing.T) {
		repo := newCodexVersionSyncSettingRepo()
		require.NoError(t, repo.Set(context.Background(), SettingKeyOpenAICodexClientVersionSynced, "0.146.0"))
		repo.updatedAt[SettingKeyOpenAICodexClientVersionSynced] = time.Now().Add(-2 * openAICodexVersionSyncInterval)
		gh := &codexVersionSyncGitHubClient{latest: stable("rust-v0.149.1")}
		newSyncSvc(repo, gh).runInitial()
		require.Equal(t, "0.149.1", repo.synced())
	})

	t.Run("尚无同步值时执行启动同步", func(t *testing.T) {
		repo := newCodexVersionSyncSettingRepo()
		gh := &codexVersionSyncGitHubClient{latest: stable("rust-v0.149.1")}
		newSyncSvc(repo, gh).runInitial()
		require.Equal(t, "0.149.1", repo.synced())
	})
}

// TestCodexVersionSyncDisabledKeepsSyncedValue 关闭自动同步只停止拉取，
// 已同步到的版本号仍继续生效，避免关开关让出站版本号突然倒退。
func TestCodexVersionSyncDisabledKeepsSyncedValue(t *testing.T) {
	t.Cleanup(func() {
		SetCodexSyncedVersionProvider(nil)
		resetCodexResolvedVersionHighWater()
	})
	resetCodexResolvedVersionHighWater()

	repo := newCodexVersionSyncSettingRepo()
	require.NoError(t, repo.Set(context.Background(), SettingKeyOpenAICodexClientVersionSynced, "9.9.9"))
	gh := &codexVersionSyncGitHubClient{latest: stable("rust-v9.9.10")}

	svc := NewOpenAICodexVersionSyncService(repo, gh, openAICodexVersionSyncInterval, false)
	svc.RegisterVersionProvider()
	svc.Start()
	t.Cleanup(svc.Stop)

	InvalidateCodexSyncedVersionCache()
	require.Equal(t, "9.9.9", resolveCodexClientVersion())

	latestCalls, _ := gh.calls()
	require.Zero(t, latestCalls, "关闭后不应再拉取")
}

// TestCodexVersionAutoSyncDefaultsOn 反义命名保证零值 Config 是安全默认。
func TestCodexVersionAutoSyncDefaultsOn(t *testing.T) {
	cfg := &config.Config{}
	require.False(t, cfg.Gateway.DisableCodexVersionAutoSync,
		"零值 Config 必须表示「自动同步开启」，否则手工构造的 Config 会静默停掉版本跟随")
}

// TestCodexVersionSyncFeedsOutboundIdentity 端到端：同步值必须真的进到出站身份头。
func TestCodexVersionSyncFeedsOutboundIdentity(t *testing.T) {
	t.Cleanup(func() {
		SetCodexSyncedVersionProvider(nil)
		resetCodexResolvedVersionHighWater()
	})
	resetCodexResolvedVersionHighWater()
	withCodexNormalization(t, true)

	repo := newCodexVersionSyncSettingRepo()
	gh := &codexVersionSyncGitHubClient{latest: stable("rust-v9.9.9")}
	svc := newSyncSvc(repo, gh)
	svc.RegisterVersionProvider()
	svc.runOnce()

	header := http.Header{}
	header.Set("originator", "codex-tui")
	header.Set("user-agent", "codex-tui/0.1.0")
	enforceCodexIdentityHeaders(header)

	require.Equal(t, "9.9.9", header.Get("version"))
	require.Contains(t, header.Get("user-agent"), "codex_cli_rs/9.9.9")
	require.Equal(t, "codex_cli_rs", header.Get("originator"))
}
