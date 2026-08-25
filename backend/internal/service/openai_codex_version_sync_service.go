package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	// openAICodexVersionSyncInterval 自动同步间隔。上游客户端发版频率是天级，
	// 6 小时足够及时跟上，同时把对 GitHub API 的调用压到每天 4 次。
	openAICodexVersionSyncInterval = 6 * time.Hour
	// openAICodexVersionSyncTimeout 单次同步的整体超时。
	openAICodexVersionSyncTimeout = 30 * time.Second
	// openAICodexVersionReadTimeout 出站身份读取生效版本时的超时。
	// 这条是转发热路径上的读取（带 TTL 缓存），必须比同步任务短得多。
	openAICodexVersionReadTimeout = 3 * time.Second
	// openAICodexVersionSyncRepo 官方 Codex 客户端仓库。
	openAICodexVersionSyncRepo = "openai/codex"
	// openAICodexVersionSyncPerPage 回退路径单次拉取的 release 数量（主路径见
	// fetchLatestStableVersion）。该仓库预发布极密集——稳定版之间隔着 20 多个 alpha，
	// 实测 30 条里只有 2 条稳定版且第二条已排在第 26 位，因此这个页大小不能再往下调，
	// 否则整页扫不到稳定版、同步会静默停更。
	openAICodexVersionSyncPerPage = 30
	// openAICodexVersionTagPrefix 客户端 release 的 tag 前缀（如 rust-v0.149.1）。
	// 同仓库还有其他组件的 tag（如 rusty-v8-*），必须按前缀过滤，否则会同步到无关版本号。
	openAICodexVersionTagPrefix = "rust-v"
)

// OpenAICodexVersionSyncService 周期性把官方 Codex 客户端的最新稳定版版本号同步到设置，
// 供出站规范身份使用，避免为了跟上游版本号而不断改代码发版。
//
// 上游 /backend-api/codex 既对低于门槛的 version 直接 404，也会按客户端身份优先降载陈旧版本，
// 因此版本号停更等价于慢性故障；本服务让它自动跟随官方发布。
//
// 同步值写入 SettingKeyOpenAICodexClientVersionSynced（本服务独占写入），
// 该键不在 admin settings API 中暴露，不进入 settings 契约。
type OpenAICodexVersionSyncService struct {
	settingRepo  SettingRepository
	githubClient GitHubReleaseClient
	interval     time.Duration
	enabled      bool
	stopCh       chan struct{}
	stopOnce     sync.Once
	wg           sync.WaitGroup
}

func NewOpenAICodexVersionSyncService(
	settingRepo SettingRepository,
	githubClient GitHubReleaseClient,
	interval time.Duration,
	enabled bool,
) *OpenAICodexVersionSyncService {
	if interval <= 0 {
		interval = openAICodexVersionSyncInterval
	}
	return &OpenAICodexVersionSyncService{
		settingRepo:  settingRepo,
		githubClient: githubClient,
		interval:     interval,
		enabled:      enabled,
		stopCh:       make(chan struct{}),
	}
}

// RegisterVersionProvider 把已同步版本号接到出站身份解析链上。
// 与 Start 分开：即便自动同步被关闭，之前同步到的值仍应继续生效，
// 否则关开关会让出站版本号突然退回编译期常量。
func (s *OpenAICodexVersionSyncService) RegisterVersionProvider() {
	if s == nil || s.settingRepo == nil {
		return
	}
	SetCodexSyncedVersionProvider(func() string {
		// 这是每个 TTL 触发一次的热路径读取，用短超时，不复用同步任务的 30s 预算。
		ctx, cancel := context.WithTimeout(context.Background(), openAICodexVersionReadTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexClientVersionSynced)
		if err != nil {
			return ""
		}
		return value
	})
}

func (s *OpenAICodexVersionSyncService) Start() {
	if s == nil || !s.enabled || s.settingRepo == nil || s.githubClient == nil || s.interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.runInitial()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *OpenAICodexVersionSyncService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

// runInitial 执行启动时的首次同步。若同步值在一个同步周期内已被刷新过则跳过：
// 频繁重启、滚动发布或崩溃重启会把「启动即同步」放大成对 GitHub 的连续请求，
// 而版本号是天级变化的，重启后没有立刻重新拉取的必要。
func (s *OpenAICodexVersionSyncService) runInitial() {
	if s.syncedWithinInterval() {
		return
	}
	s.runOnce()
}

// syncedWithinInterval 判断已同步值是否仍在一个同步周期内。
// 借设置行自身的 UpdatedAt 判断，无需额外记录时间戳的设置项。
// 读取失败或尚无有效同步值时返回 false，让启动同步照常执行。
func (s *OpenAICodexVersionSyncService) syncedWithinInterval() bool {
	if s.interval <= 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), openAICodexVersionSyncTimeout)
	defer cancel()

	setting, err := s.settingRepo.Get(ctx, SettingKeyOpenAICodexClientVersionSynced)
	if err != nil || setting == nil || setting.UpdatedAt.IsZero() {
		return false
	}
	if NormalizeCodexClientVersion(setting.Value) == "" {
		return false
	}
	return time.Since(setting.UpdatedAt) < s.interval
}

func (s *OpenAICodexVersionSyncService) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), openAICodexVersionSyncTimeout)
	defer cancel()

	latest := s.fetchLatestStableVersion(ctx)
	if latest == "" {
		return
	}

	current := NormalizeCodexClientVersion(s.currentSyncedVersion(ctx))
	// 只向前推进：上游偶发返回旧数据或重新发布历史 tag 时不把已同步的版本号降级。
	if current != "" && CompareVersions(latest, current) <= 0 {
		return
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAICodexClientVersionSynced, latest); err != nil {
		slog.Warn("openai_codex_version_sync_persist_failed", "version", latest, "error", err)
		return
	}
	InvalidateCodexSyncedVersionCache()
	slog.Info("openai_codex_version_synced", "previous", current, "version", latest)
}

// fetchLatestStableVersion 取官方最新稳定版客户端版本号；取不到时返回空串，
// 由调用方保持既有值（不清空、不降级），各失败分支自行落日志。
//
// 主路径 /releases/latest：该端点本身就排除 draft 与 prerelease，直接给出最新正式发布，
// 因此不受该仓库预发布密度的影响，也不需要为了「窗口里得有一条稳定版」而多拉数据——
// 单条 release 约 0.3MB，而 per_page=30 的列表页约 10MB。
//
// 回退列表扫描：latest 是跨 tag 家族按 published_at 取的，若同仓库其他组件
// （如 rusty-v8-*）某天发了正式 release 而成为 latest，主路径会被 rust-v 前缀过滤挡掉；
// 此时必须扫一页 release 才能继续跟随官方版本，否则版本号会静默停更。
// 两条路径共用同一套过滤（前缀 / draft / prerelease / 版本号形态），语义不会分叉。
func (s *OpenAICodexVersionSyncService) fetchLatestStableVersion(ctx context.Context) string {
	release, err := s.githubClient.FetchLatestRelease(ctx, openAICodexVersionSyncRepo)
	if err != nil {
		slog.Warn("openai_codex_version_sync_latest_fetch_failed", "error", err)
	} else if version := latestCodexStableReleaseVersion([]*GitHubRelease{release}); version != "" {
		return version
	}

	// 主路径没拿到可用版本（抓取失败，或 latest 不是客户端 tag 家族的稳定版）。
	releases, err := s.githubClient.FetchRecentReleases(ctx, openAICodexVersionSyncRepo, openAICodexVersionSyncPerPage)
	if err != nil {
		slog.Warn("openai_codex_version_sync_fetch_failed", "error", err)
		return ""
	}
	version := latestCodexStableReleaseVersion(releases)
	if version == "" {
		slog.Warn("openai_codex_version_sync_no_stable_release", "repo", openAICodexVersionSyncRepo)
	}
	return version
}

func (s *OpenAICodexVersionSyncService) currentSyncedVersion(ctx context.Context) string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexClientVersionSynced)
	if err != nil {
		return ""
	}
	return value
}

// latestCodexStableReleaseVersion 从 release 列表里挑出最大的稳定版客户端版本号。
// 过滤条件：tag 前缀为 rust-v（排除同仓库其他组件的 tag）、非草稿、非预发布、
// 版本号不带 -alpha/-beta 之类后缀。取最大值而非最新发布，避免重新发布历史 tag 造成回退。
// 主路径的单条 /releases/latest 结果也走本函数（单元素切片），保证两条取数路径的过滤语义一致。
func latestCodexStableReleaseVersion(releases []*GitHubRelease) string {
	best := ""
	for _, release := range releases {
		if release == nil || release.Draft || release.Prerelease {
			continue
		}
		tag := strings.TrimSpace(release.TagName)
		if !strings.HasPrefix(tag, openAICodexVersionTagPrefix) {
			continue
		}
		version := NormalizeCodexClientVersion(strings.TrimPrefix(tag, openAICodexVersionTagPrefix))
		if version == "" || strings.Contains(version, "-") {
			continue
		}
		if best == "" || CompareVersions(version, best) > 0 {
			best = version
		}
	}
	return best
}
