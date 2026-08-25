package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"golang.org/x/sync/singleflight"
)

// chatgptCodexModelsURL 是 ChatGPT Codex 模型清单端点。
// 使用包变量便于测试替换为本地桩服务。
var chatgptCodexModelsURL = "https://chatgpt.com/backend-api/codex/models"

const (
	codexModelsManifestBodyLimit       int64 = 8 << 20
	codexModelsManifestCacheBodyLimit        = 1 << 20
	codexModelsManifestCacheMaxEntries       = 64
	codexModelsManifestCacheTTL              = 30 * time.Second
	codexModelsManifestCacheStaleTTL         = 5 * time.Minute
	codexModelsManifestRequestTimeout        = 15 * time.Second
)

// CodexModelsManifest 保存返回给客户端的模型清单和缓存元数据。
type CodexModelsManifest struct {
	Body         []byte
	ETag         string
	upstreamETag string
	NotModified  bool
}

type codexModelsManifestUpstreamError struct {
	err       error
	retryable bool
}

func (e *codexModelsManifestUpstreamError) Error() string { return e.err.Error() }

func (e *codexModelsManifestUpstreamError) Unwrap() error { return e.err }

// IsRetryableCodexModelsManifestError 判断换一个账号是否可能成功。
func IsRetryableCodexModelsManifestError(err error) bool {
	var upstreamErr *codexModelsManifestUpstreamError
	return errors.As(err, &upstreamErr) && upstreamErr.retryable
}

type codexModelsManifestRequest struct {
	url                string
	headers            http.Header
	proxyURL           string
	accountID          int64
	accountConcurrency int
	useAPIKeyUpstream  bool
}

type codexModelsManifestCacheEntry struct {
	manifest   *CodexModelsManifest
	order      uint64
	expiresAt  time.Time
	staleUntil time.Time
}

type codexModelsManifestCacheState uint8

const (
	codexModelsManifestCacheMiss codexModelsManifestCacheState = iota
	codexModelsManifestCacheFresh
	codexModelsManifestCacheStale
)

type codexModelsManifestCache struct {
	mu        sync.Mutex
	entries   map[string]codexModelsManifestCacheEntry
	nextOrder uint64
	refresh   singleflight.Group
}

func (c *codexModelsManifestCache) get(key string, now time.Time) (*CodexModelsManifest, codexModelsManifestCacheState) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, codexModelsManifestCacheMiss
	}
	if !now.Before(entry.staleUntil) {
		delete(c.entries, key)
		return nil, codexModelsManifestCacheMiss
	}
	if now.Before(entry.expiresAt) {
		return entry.manifest, codexModelsManifestCacheFresh
	}
	return entry.manifest, codexModelsManifestCacheStale
}

func (c *codexModelsManifestCache) set(key string, manifest *CodexModelsManifest, now time.Time) {
	if manifest == nil || len(manifest.Body) > codexModelsManifestCacheBodyLimit {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]codexModelsManifestCacheEntry)
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= codexModelsManifestCacheMaxEntries {
		oldestKey := ""
		var oldestOrder uint64
		for candidateKey, entry := range c.entries {
			if !now.Before(entry.staleUntil) {
				delete(c.entries, candidateKey)
				continue
			}
			if oldestKey == "" || entry.order < oldestOrder {
				oldestKey = candidateKey
				oldestOrder = entry.order
			}
		}
		if len(c.entries) >= codexModelsManifestCacheMaxEntries && oldestKey != "" {
			delete(c.entries, oldestKey)
		}
	}

	c.nextOrder++
	c.entries[key] = codexModelsManifestCacheEntry{
		manifest:   manifest,
		order:      c.nextOrder,
		expiresAt:  now.Add(codexModelsManifestCacheTTL),
		staleUntil: now.Add(codexModelsManifestCacheStaleTTL),
	}
}

// FetchCodexModelsManifest 根据账号类型获取 Codex 模型清单：
// OAuth 账号走 ChatGPT backend，API Key 账号走其自定义上游的 /v1/models。
func (s *OpenAIGatewayService) FetchCodexModelsManifest(ctx context.Context, account *Account, clientVersion, ifNoneMatch string) (*CodexModelsManifest, error) {
	if account == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_ACCOUNT_REQUIRED", "account is required")
	}
	authToken, tokenType, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_CREDENTIALS_FAILED", "resolve account token: %v", err)
	}

	clientVersion = strings.TrimSpace(clientVersion)
	if clientVersion == "" {
		// 必须用生效版本而非编译期常量：自动同步推进后，用旧版本号请求清单会拿到
		// 少了新模型的 manifest，重新制造「模型发现不到」的问题。
		clientVersion = resolveCodexClientVersion()
	}

	requestEndpoint := chatgptCodexModelsURL
	useAPIKeyUpstream := false
	appendModelsPath := false
	switch tokenType {
	case "oauth":
		if strings.TrimSpace(authToken) == "" {
			return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_TOKEN_MISSING", "account has no Codex backend access token")
		}
	case "apikey":
		baseURL := strings.TrimSpace(account.GetCredential("base_url"))
		if baseURL == "" || isOfficialOpenAIModelsBaseURL(baseURL) {
			return nil, infraerrors.New(
				http.StatusBadGateway,
				"OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_UNSUPPORTED",
				"Codex models manifest requires a custom API key upstream base URL",
			)
		}
		normalizedBaseURL, validateErr := s.validateUpstreamBaseURL(baseURL)
		if validateErr != nil {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_INVALID", "invalid Codex models upstream base URL: %v", validateErr)
		}
		requestEndpoint = normalizedBaseURL
		useAPIKeyUpstream = true
		appendModelsPath = true
	default:
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_ACCOUNT_TYPE_UNSUPPORTED", "account token type %q cannot fetch the Codex models manifest", tokenType)
	}

	requestURL, err := buildCodexModelsManifestURL(requestEndpoint, appendModelsPath, clientVersion)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_REQUEST_FAILED", "build codex models request URL: %v", err)
	}

	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+authToken)
	headers.Set("Accept", "application/json")
	headers.Set("Originator", openai.CodexDefaultOriginator)
	headers.Set("Version", clientVersion)
	headers.Set("User-Agent", codexCLIUserAgent)
	// clientVersion 来自客户端的 ?client_version=，低于上游门槛时清单请求会被 404
	// （issue #3901），模型发现随之失败。统一走出站身份收口，URL 上的 client_version 保持原值。
	enforceCodexIdentityHeaders(headers)
	if !useAPIKeyUpstream {
		if chatgptAccountID := account.GetChatGPTAccountID(); chatgptAccountID != "" {
			headers.Set("chatgpt-account-id", chatgptAccountID)
		}
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	request := codexModelsManifestRequest{
		url:                requestURL.String(),
		headers:            headers,
		proxyURL:           proxyURL,
		accountID:          account.ID,
		accountConcurrency: account.Concurrency,
		useAPIKeyUpstream:  useAPIKeyUpstream,
	}
	if useAPIKeyUpstream {
		return s.fetchCachedAPIKeyCodexModelsManifest(ctx, request, ifNoneMatch)
	}
	return s.fetchCodexModelsManifestUpstream(ctx, request, ifNoneMatch, account)
}

func (s *OpenAIGatewayService) fetchCachedAPIKeyCodexModelsManifest(ctx context.Context, request codexModelsManifestRequest, ifNoneMatch string) (*CodexModelsManifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cacheKey := buildCodexModelsManifestCacheKey(request)
	manifest, state := s.codexModelsManifestCache.get(cacheKey, time.Now())
	if state == codexModelsManifestCacheFresh {
		return codexModelsManifestForClient(manifest, ifNoneMatch), nil
	}
	resultCh := s.refreshCachedAPIKeyCodexModelsManifest(cacheKey, request)
	if state == codexModelsManifestCacheStale {
		return codexModelsManifestForClient(manifest, ifNoneMatch), nil
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		manifest, ok := result.Val.(*CodexModelsManifest)
		if !ok || manifest == nil {
			return nil, infraerrors.New(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_REQUEST_FAILED", "invalid shared Codex models manifest result")
		}
		return codexModelsManifestForClient(manifest, ifNoneMatch), nil
	}
}

func (s *OpenAIGatewayService) refreshCachedAPIKeyCodexModelsManifest(cacheKey string, request codexModelsManifestRequest) <-chan singleflight.Result {
	return s.codexModelsManifestCache.refresh.DoChan(cacheKey, func() (any, error) {
		cached, _ := s.codexModelsManifestCache.get(cacheKey, time.Now())
		upstreamETag := ""
		if cached != nil {
			upstreamETag = cached.upstreamETag
		}
		manifest, err := s.fetchCodexModelsManifestUpstream(context.Background(), request, upstreamETag, nil)
		if err != nil {
			return nil, err
		}
		if manifest.NotModified {
			if cached == nil {
				return nil, &codexModelsManifestUpstreamError{
					err:       infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST", "codex models upstream returned 304 without a cached manifest"),
					retryable: true,
				}
			}
			s.codexModelsManifestCache.set(cacheKey, cached, time.Now())
			return cached, nil
		}
		s.codexModelsManifestCache.set(cacheKey, manifest, time.Now())
		return manifest, nil
	})
}

func (s *OpenAIGatewayService) fetchCodexModelsManifestUpstream(ctx context.Context, request codexModelsManifestRequest, ifNoneMatch string, oauthAccount *Account) (*CodexModelsManifest, error) {
	reqCtx, cancel := context.WithTimeout(ctx, codexModelsManifestRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, request.url, nil)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_REQUEST_FAILED", "create codex models request: %v", err)
	}
	req.Header = request.headers.Clone()
	if ifNoneMatch = strings.TrimSpace(ifNoneMatch); ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	var resp *http.Response
	if request.useAPIKeyUpstream {
		if s.httpUpstream == nil {
			return nil, infraerrors.New(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_UPSTREAM_NOT_CONFIGURED", "Codex models upstream HTTP client is not configured")
		}
		resp, err = s.httpUpstream.Do(req, request.proxyURL, request.accountID, request.accountConcurrency)
	} else {
		client, clientErr := httpclient.GetClient(httpclient.Options{
			ProxyURL:              request.proxyURL,
			Timeout:               codexModelsManifestRequestTimeout,
			ResponseHeaderTimeout: 10 * time.Second,
		})
		if clientErr != nil {
			return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_PROXY_INVALID", "invalid proxy configuration: %v", clientErr)
		}
		resp, err = client.Do(req)
	}
	if err != nil {
		return nil, &codexModelsManifestUpstreamError{
			err:       infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "codex models manifest request failed: %v", err),
			retryable: !errors.Is(err, context.Canceled),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return &CodexModelsManifest{ETag: resp.Header.Get("ETag"), NotModified: true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		retryableOAuth401 := resp.StatusCode == http.StatusUnauthorized && !request.useAPIKeyUpstream && oauthAccount != nil
		if retryableOAuth401 {
			s.handleOpenAIAccountUpstreamError(ctx, oauthAccount, resp.StatusCode, resp.Header, body)
		}
		return nil, &codexModelsManifestUpstreamError{
			err:       infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "codex models manifest upstream error %d: %s", resp.StatusCode, message),
			retryable: retryableOAuth401 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError,
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, codexModelsManifestBodyLimit))
	if err != nil {
		return nil, &codexModelsManifestUpstreamError{
			err:       infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "read codex models manifest response: %v", err),
			retryable: !errors.Is(err, context.Canceled),
		}
	}

	upstreamBody := body
	if request.useAPIKeyUpstream {
		body = convertOpenAIModelListToCodexManifest(body)
	}
	if err := validateCodexModelsManifestEnvelope(body); err != nil {
		return nil, &codexModelsManifestUpstreamError{
			err:       infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST", "codex models manifest upstream returned an invalid envelope: %v", err),
			retryable: true,
		}
	}
	if request.useAPIKeyUpstream {
		body, err = adjustAPIKeyCodexModelsManifest(body)
		if err != nil {
			return nil, &codexModelsManifestUpstreamError{
				err:       infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST", "codex models manifest upstream could not be adjusted: %v", err),
				retryable: true,
			}
		}
	}

	etag := resp.Header.Get("ETag")
	manifest := &CodexModelsManifest{Body: body, ETag: etag}
	if request.useAPIKeyUpstream {
		manifest.upstreamETag = etag
		if !bytes.Equal(body, upstreamBody) {
			manifest.ETag = codexModelsManifestBodyETag(body)
		}
	}
	return manifest, nil
}

func convertOpenAIModelListToCodexManifest(body []byte) []byte {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope == nil {
		return body
	}
	if _, ok := envelope["models"]; ok {
		return body
	}
	data, ok := envelope["data"]
	if !ok {
		return body
	}
	var entries []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return body
	}
	type codexModelEntry struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
	}
	models := make([]codexModelEntry, 0, len(entries))
	for _, entry := range entries {
		if id := strings.TrimSpace(entry.ID); id != "" {
			models = append(models, codexModelEntry{Slug: id, DisplayName: id})
		}
	}
	if len(models) == 0 {
		return body
	}
	converted, err := json.Marshal(map[string][]codexModelEntry{"models": models})
	if err != nil {
		return body
	}
	return converted
}

var apiKeyCodexModelsWithoutResponsesLite = map[string]struct{}{
	"gpt-5.6-sol":   {},
	"gpt-5.6-terra": {},
	"gpt-5.6-luna":  {},
}

func adjustAPIKeyCodexModelsManifest(body []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode JSON object: %w", err)
	}
	var models []json.RawMessage
	if err := json.Unmarshal(envelope["models"], &models); err != nil {
		return nil, fmt.Errorf("decode top-level models array: %w", err)
	}

	changed := false
	for i, rawModel := range models {
		var model map[string]json.RawMessage
		if err := json.Unmarshal(rawModel, &model); err != nil || model == nil {
			continue
		}
		var slug string
		if err := json.Unmarshal(model["slug"], &slug); err != nil {
			continue
		}
		if _, targeted := apiKeyCodexModelsWithoutResponsesLite[slug]; !targeted {
			continue
		}
		var useResponsesLite bool
		if err := json.Unmarshal(model["use_responses_lite"], &useResponsesLite); err != nil || !useResponsesLite {
			continue
		}
		model["use_responses_lite"] = json.RawMessage("false")
		adjusted, err := json.Marshal(model)
		if err != nil {
			return nil, fmt.Errorf("encode model %q: %w", slug, err)
		}
		models[i] = adjusted
		changed = true
	}
	if !changed {
		return body, nil
	}

	adjustedModels, err := json.Marshal(models)
	if err != nil {
		return nil, fmt.Errorf("encode top-level models array: %w", err)
	}
	envelope["models"] = adjustedModels
	adjusted, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode JSON object: %w", err)
	}
	return adjusted, nil
}

func validateCodexModelsManifestEnvelope(body []byte) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode JSON object: %w", err)
	}
	if envelope == nil {
		return errors.New("expected a JSON object")
	}
	models, ok := envelope["models"]
	if !ok {
		return errors.New("missing top-level models array")
	}
	models = bytes.TrimSpace(models)
	var entries []json.RawMessage
	if len(models) == 0 || models[0] != '[' {
		return errors.New("top-level models field is not an array")
	}
	if err := json.Unmarshal(models, &entries); err != nil {
		return fmt.Errorf("decode top-level models array: %w", err)
	}
	return nil
}

func codexModelsManifestBodyETag(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf(`"%x"`, sum)
}

func buildCodexModelsManifestCacheKey(request codexModelsManifestRequest) string {
	hasher := sha256.New()
	_, _ = fmt.Fprintf(hasher, "%d\n%s\n%s\n", request.accountID, request.proxyURL, request.url)
	headerNames := make([]string, 0, len(request.headers))
	for name := range request.headers {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	for _, name := range headerNames {
		_, _ = fmt.Fprintf(hasher, "%s\n", strings.ToLower(name))
		for _, value := range request.headers[name] {
			_, _ = fmt.Fprintf(hasher, "%s\n", value)
		}
	}
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

func codexModelsManifestForClient(manifest *CodexModelsManifest, ifNoneMatch string) *CodexModelsManifest {
	if manifest == nil {
		return nil
	}
	if codexModelsManifestETagMatches(ifNoneMatch, manifest.ETag) {
		return &CodexModelsManifest{ETag: manifest.ETag, NotModified: true}
	}
	return manifest
}

func codexModelsManifestETagMatches(ifNoneMatch, etag string) bool {
	etag = strings.TrimSpace(etag)
	if etag == "" {
		return false
	}
	normalize := func(value string) string {
		value = strings.TrimSpace(value)
		if len(value) >= 2 && strings.EqualFold(value[:2], "W/") {
			value = strings.TrimSpace(value[2:])
		}
		return value
	}
	want := normalize(etag)
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || normalize(candidate) == want {
			return true
		}
	}
	return false
}

func isOfficialOpenAIModelsBaseURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	hostname := strings.TrimSuffix(parsed.Hostname(), ".")
	return strings.EqualFold(hostname, "api.openai.com")
}

func buildCodexModelsManifestURL(endpoint string, appendModelsPath bool, clientVersion string) (*url.URL, error) {
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if requestURL.Fragment != "" {
		return nil, errors.New("URL fragments are not supported")
	}

	query := requestURL.Query()
	requestURL.RawQuery = ""
	requestURL.ForceQuery = false
	if appendModelsPath {
		requestURL, err = url.Parse(buildOpenAIModelsURL(requestURL.String()))
		if err != nil {
			return nil, err
		}
	}
	query.Set("client_version", clientVersion)
	requestURL.RawQuery = query.Encode()
	return requestURL, nil
}
