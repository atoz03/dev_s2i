package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrSoraS3ProfileNotFound = infraerrors.NotFound("SORA_S3_PROFILE_NOT_FOUND", "sora s3 profile not found")
	ErrSoraS3ProfileExists   = infraerrors.Conflict("SORA_S3_PROFILE_EXISTS", "sora s3 profile already exists")
)

// GetUpstreamUserAgentSettings 返回默认上游 User-Agent 与是否强制统一。
func (s *SettingService) GetUpstreamUserAgentSettings(ctx context.Context) (string, bool) {
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyDefaultUpstreamUserAgent,
		SettingKeyForceUnifiedUpstreamUserAgent,
	})
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(values[SettingKeyDefaultUpstreamUserAgent]), values[SettingKeyForceUnifiedUpstreamUserAgent] == "true"
}

type soraS3ProfilesStore struct {
	ActiveProfileID string                   `json:"active_profile_id"`
	Items           []soraS3ProfileStoreItem `json:"items"`
}

type soraS3ProfileStoreItem struct {
	ProfileID                string `json:"profile_id"`
	Name                     string `json:"name"`
	Enabled                  bool   `json:"enabled"`
	Endpoint                 string `json:"endpoint"`
	Region                   string `json:"region"`
	Bucket                   string `json:"bucket"`
	AccessKeyID              string `json:"access_key_id"`
	SecretAccessKey          string `json:"secret_access_key"`
	Prefix                   string `json:"prefix"`
	ForcePathStyle           bool   `json:"force_path_style"`
	CDNURL                   string `json:"cdn_url"`
	DefaultStorageQuotaBytes int64  `json:"default_storage_quota_bytes"`
	UpdatedAt                string `json:"updated_at"`
}

// GetSoraS3Settings 获取 Sora S3 存储配置（兼容旧单配置语义：返回当前激活配置）
func (s *SettingService) GetSoraS3Settings(ctx context.Context) (*SoraS3Settings, error) {
	profiles, err := s.ListSoraS3Profiles(ctx)
	if err != nil {
		return nil, err
	}

	activeProfile := pickActiveSoraS3Profile(profiles.Items, profiles.ActiveProfileID)
	if activeProfile == nil {
		return &SoraS3Settings{}, nil
	}

	return &SoraS3Settings{
		Enabled:                   activeProfile.Enabled,
		Endpoint:                  activeProfile.Endpoint,
		Region:                    activeProfile.Region,
		Bucket:                    activeProfile.Bucket,
		AccessKeyID:               activeProfile.AccessKeyID,
		SecretAccessKey:           activeProfile.SecretAccessKey,
		SecretAccessKeyConfigured: activeProfile.SecretAccessKeyConfigured,
		Prefix:                    activeProfile.Prefix,
		ForcePathStyle:            activeProfile.ForcePathStyle,
		CDNURL:                    activeProfile.CDNURL,
		DefaultStorageQuotaBytes:  activeProfile.DefaultStorageQuotaBytes,
	}, nil
}

// SetSoraS3Settings 更新 Sora S3 存储配置（兼容旧单配置语义：写入当前激活配置）
func (s *SettingService) SetSoraS3Settings(ctx context.Context, settings *SoraS3Settings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	store, err := s.loadSoraS3ProfilesStore(ctx)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	activeIndex := findSoraS3ProfileIndex(store.Items, store.ActiveProfileID)
	if activeIndex < 0 {
		activeID := "default"
		if hasSoraS3ProfileID(store.Items, activeID) {
			activeID = fmt.Sprintf("default-%d", time.Now().Unix())
		}
		store.Items = append(store.Items, soraS3ProfileStoreItem{
			ProfileID: activeID,
			Name:      "Default",
			UpdatedAt: now,
		})
		store.ActiveProfileID = activeID
		activeIndex = len(store.Items) - 1
	}

	active := store.Items[activeIndex]
	active.Enabled = settings.Enabled
	active.Endpoint = strings.TrimSpace(settings.Endpoint)
	active.Region = strings.TrimSpace(settings.Region)
	active.Bucket = strings.TrimSpace(settings.Bucket)
	active.AccessKeyID = strings.TrimSpace(settings.AccessKeyID)
	active.Prefix = strings.TrimSpace(settings.Prefix)
	active.ForcePathStyle = settings.ForcePathStyle
	active.CDNURL = strings.TrimSpace(settings.CDNURL)
	active.DefaultStorageQuotaBytes = maxInt64(settings.DefaultStorageQuotaBytes, 0)
	if settings.SecretAccessKey != "" {
		active.SecretAccessKey = settings.SecretAccessKey
	}
	active.UpdatedAt = now
	store.Items[activeIndex] = active

	return s.persistSoraS3ProfilesStore(ctx, store)
}

// ListSoraS3Profiles 获取 Sora S3 多配置列表
func (s *SettingService) ListSoraS3Profiles(ctx context.Context) (*SoraS3ProfileList, error) {
	store, err := s.loadSoraS3ProfilesStore(ctx)
	if err != nil {
		return nil, err
	}
	return convertSoraS3ProfilesStore(store), nil
}

// CreateSoraS3Profile 创建 Sora S3 配置
func (s *SettingService) CreateSoraS3Profile(ctx context.Context, profile *SoraS3Profile, setActive bool) (*SoraS3Profile, error) {
	if profile == nil {
		return nil, fmt.Errorf("profile cannot be nil")
	}

	profileID := strings.TrimSpace(profile.ProfileID)
	if profileID == "" {
		return nil, infraerrors.BadRequest("SORA_S3_PROFILE_ID_REQUIRED", "profile_id is required")
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		return nil, infraerrors.BadRequest("SORA_S3_PROFILE_NAME_REQUIRED", "name is required")
	}

	store, err := s.loadSoraS3ProfilesStore(ctx)
	if err != nil {
		return nil, err
	}
	if hasSoraS3ProfileID(store.Items, profileID) {
		return nil, ErrSoraS3ProfileExists
	}

	now := time.Now().UTC().Format(time.RFC3339)
	store.Items = append(store.Items, soraS3ProfileStoreItem{
		ProfileID:                profileID,
		Name:                     name,
		Enabled:                  profile.Enabled,
		Endpoint:                 strings.TrimSpace(profile.Endpoint),
		Region:                   strings.TrimSpace(profile.Region),
		Bucket:                   strings.TrimSpace(profile.Bucket),
		AccessKeyID:              strings.TrimSpace(profile.AccessKeyID),
		SecretAccessKey:          profile.SecretAccessKey,
		Prefix:                   strings.TrimSpace(profile.Prefix),
		ForcePathStyle:           profile.ForcePathStyle,
		CDNURL:                   strings.TrimSpace(profile.CDNURL),
		DefaultStorageQuotaBytes: maxInt64(profile.DefaultStorageQuotaBytes, 0),
		UpdatedAt:                now,
	})

	if setActive || store.ActiveProfileID == "" {
		store.ActiveProfileID = profileID
	}

	if err := s.persistSoraS3ProfilesStore(ctx, store); err != nil {
		return nil, err
	}

	profiles := convertSoraS3ProfilesStore(store)
	created := findSoraS3ProfileByID(profiles.Items, profileID)
	if created == nil {
		return nil, ErrSoraS3ProfileNotFound
	}
	return created, nil
}

// UpdateSoraS3Profile 更新 Sora S3 配置
func (s *SettingService) UpdateSoraS3Profile(ctx context.Context, profileID string, profile *SoraS3Profile) (*SoraS3Profile, error) {
	if profile == nil {
		return nil, fmt.Errorf("profile cannot be nil")
	}

	targetID := strings.TrimSpace(profileID)
	if targetID == "" {
		return nil, infraerrors.BadRequest("SORA_S3_PROFILE_ID_REQUIRED", "profile_id is required")
	}

	store, err := s.loadSoraS3ProfilesStore(ctx)
	if err != nil {
		return nil, err
	}

	targetIndex := findSoraS3ProfileIndex(store.Items, targetID)
	if targetIndex < 0 {
		return nil, ErrSoraS3ProfileNotFound
	}

	target := store.Items[targetIndex]
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		return nil, infraerrors.BadRequest("SORA_S3_PROFILE_NAME_REQUIRED", "name is required")
	}
	target.Name = name
	target.Enabled = profile.Enabled
	target.Endpoint = strings.TrimSpace(profile.Endpoint)
	target.Region = strings.TrimSpace(profile.Region)
	target.Bucket = strings.TrimSpace(profile.Bucket)
	target.AccessKeyID = strings.TrimSpace(profile.AccessKeyID)
	target.Prefix = strings.TrimSpace(profile.Prefix)
	target.ForcePathStyle = profile.ForcePathStyle
	target.CDNURL = strings.TrimSpace(profile.CDNURL)
	target.DefaultStorageQuotaBytes = maxInt64(profile.DefaultStorageQuotaBytes, 0)
	if profile.SecretAccessKey != "" {
		target.SecretAccessKey = profile.SecretAccessKey
	}
	target.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	store.Items[targetIndex] = target

	if err := s.persistSoraS3ProfilesStore(ctx, store); err != nil {
		return nil, err
	}

	profiles := convertSoraS3ProfilesStore(store)
	updated := findSoraS3ProfileByID(profiles.Items, targetID)
	if updated == nil {
		return nil, ErrSoraS3ProfileNotFound
	}
	return updated, nil
}

// DeleteSoraS3Profile 删除 Sora S3 配置
func (s *SettingService) DeleteSoraS3Profile(ctx context.Context, profileID string) error {
	targetID := strings.TrimSpace(profileID)
	if targetID == "" {
		return infraerrors.BadRequest("SORA_S3_PROFILE_ID_REQUIRED", "profile_id is required")
	}

	store, err := s.loadSoraS3ProfilesStore(ctx)
	if err != nil {
		return err
	}

	targetIndex := findSoraS3ProfileIndex(store.Items, targetID)
	if targetIndex < 0 {
		return ErrSoraS3ProfileNotFound
	}

	store.Items = append(store.Items[:targetIndex], store.Items[targetIndex+1:]...)
	if store.ActiveProfileID == targetID {
		store.ActiveProfileID = ""
		if len(store.Items) > 0 {
			store.ActiveProfileID = store.Items[0].ProfileID
		}
	}

	return s.persistSoraS3ProfilesStore(ctx, store)
}

// SetActiveSoraS3Profile 设置激活的 Sora S3 配置
func (s *SettingService) SetActiveSoraS3Profile(ctx context.Context, profileID string) (*SoraS3Profile, error) {
	targetID := strings.TrimSpace(profileID)
	if targetID == "" {
		return nil, infraerrors.BadRequest("SORA_S3_PROFILE_ID_REQUIRED", "profile_id is required")
	}

	store, err := s.loadSoraS3ProfilesStore(ctx)
	if err != nil {
		return nil, err
	}

	targetIndex := findSoraS3ProfileIndex(store.Items, targetID)
	if targetIndex < 0 {
		return nil, ErrSoraS3ProfileNotFound
	}

	store.ActiveProfileID = targetID
	store.Items[targetIndex].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.persistSoraS3ProfilesStore(ctx, store); err != nil {
		return nil, err
	}

	profiles := convertSoraS3ProfilesStore(store)
	active := pickActiveSoraS3Profile(profiles.Items, profiles.ActiveProfileID)
	if active == nil {
		return nil, ErrSoraS3ProfileNotFound
	}
	return active, nil
}

func (s *SettingService) loadSoraS3ProfilesStore(ctx context.Context) (*soraS3ProfilesStore, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeySoraS3Profiles)
	if err == nil {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return &soraS3ProfilesStore{}, nil
		}
		var store soraS3ProfilesStore
		if unmarshalErr := json.Unmarshal([]byte(trimmed), &store); unmarshalErr != nil {
			legacy, legacyErr := s.getLegacySoraS3Settings(ctx)
			if legacyErr != nil {
				return nil, fmt.Errorf("unmarshal sora s3 profiles: %w", unmarshalErr)
			}
			if isEmptyLegacySoraS3Settings(legacy) {
				return &soraS3ProfilesStore{}, nil
			}
			now := time.Now().UTC().Format(time.RFC3339)
			return &soraS3ProfilesStore{
				ActiveProfileID: "default",
				Items: []soraS3ProfileStoreItem{{
					ProfileID:                "default",
					Name:                     "Default",
					Enabled:                  legacy.Enabled,
					Endpoint:                 strings.TrimSpace(legacy.Endpoint),
					Region:                   strings.TrimSpace(legacy.Region),
					Bucket:                   strings.TrimSpace(legacy.Bucket),
					AccessKeyID:              strings.TrimSpace(legacy.AccessKeyID),
					SecretAccessKey:          legacy.SecretAccessKey,
					Prefix:                   strings.TrimSpace(legacy.Prefix),
					ForcePathStyle:           legacy.ForcePathStyle,
					CDNURL:                   strings.TrimSpace(legacy.CDNURL),
					DefaultStorageQuotaBytes: maxInt64(legacy.DefaultStorageQuotaBytes, 0),
					UpdatedAt:                now,
				}},
			}, nil
		}
		normalized := normalizeSoraS3ProfilesStore(store)
		return &normalized, nil
	}

	if !errors.Is(err, ErrSettingNotFound) {
		return nil, fmt.Errorf("get sora s3 profiles: %w", err)
	}

	legacy, legacyErr := s.getLegacySoraS3Settings(ctx)
	if legacyErr != nil {
		return nil, legacyErr
	}
	if isEmptyLegacySoraS3Settings(legacy) {
		return &soraS3ProfilesStore{}, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	return &soraS3ProfilesStore{
		ActiveProfileID: "default",
		Items: []soraS3ProfileStoreItem{{
			ProfileID:                "default",
			Name:                     "Default",
			Enabled:                  legacy.Enabled,
			Endpoint:                 strings.TrimSpace(legacy.Endpoint),
			Region:                   strings.TrimSpace(legacy.Region),
			Bucket:                   strings.TrimSpace(legacy.Bucket),
			AccessKeyID:              strings.TrimSpace(legacy.AccessKeyID),
			SecretAccessKey:          legacy.SecretAccessKey,
			Prefix:                   strings.TrimSpace(legacy.Prefix),
			ForcePathStyle:           legacy.ForcePathStyle,
			CDNURL:                   strings.TrimSpace(legacy.CDNURL),
			DefaultStorageQuotaBytes: maxInt64(legacy.DefaultStorageQuotaBytes, 0),
			UpdatedAt:                now,
		}},
	}, nil
}

func (s *SettingService) persistSoraS3ProfilesStore(ctx context.Context, store *soraS3ProfilesStore) error {
	if store == nil {
		return fmt.Errorf("sora s3 profiles store cannot be nil")
	}

	normalized := normalizeSoraS3ProfilesStore(*store)
	data, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal sora s3 profiles: %w", err)
	}

	updates := map[string]string{SettingKeySoraS3Profiles: string(data)}

	active := pickActiveSoraS3ProfileFromStore(normalized.Items, normalized.ActiveProfileID)
	if active == nil {
		updates[SettingKeySoraS3Enabled] = "false"
		updates[SettingKeySoraS3Endpoint] = ""
		updates[SettingKeySoraS3Region] = ""
		updates[SettingKeySoraS3Bucket] = ""
		updates[SettingKeySoraS3AccessKeyID] = ""
		updates[SettingKeySoraS3Prefix] = ""
		updates[SettingKeySoraS3ForcePathStyle] = "false"
		updates[SettingKeySoraS3CDNURL] = ""
		updates[SettingKeySoraDefaultStorageQuotaBytes] = "0"
		updates[SettingKeySoraS3SecretAccessKey] = ""
	} else {
		updates[SettingKeySoraS3Enabled] = strconv.FormatBool(active.Enabled)
		updates[SettingKeySoraS3Endpoint] = strings.TrimSpace(active.Endpoint)
		updates[SettingKeySoraS3Region] = strings.TrimSpace(active.Region)
		updates[SettingKeySoraS3Bucket] = strings.TrimSpace(active.Bucket)
		updates[SettingKeySoraS3AccessKeyID] = strings.TrimSpace(active.AccessKeyID)
		updates[SettingKeySoraS3Prefix] = strings.TrimSpace(active.Prefix)
		updates[SettingKeySoraS3ForcePathStyle] = strconv.FormatBool(active.ForcePathStyle)
		updates[SettingKeySoraS3CDNURL] = strings.TrimSpace(active.CDNURL)
		updates[SettingKeySoraDefaultStorageQuotaBytes] = strconv.FormatInt(maxInt64(active.DefaultStorageQuotaBytes, 0), 10)
		updates[SettingKeySoraS3SecretAccessKey] = active.SecretAccessKey
	}

	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return err
	}

	if s.onUpdate != nil {
		s.onUpdate()
	}
	return nil
}

func (s *SettingService) getLegacySoraS3Settings(ctx context.Context) (*SoraS3Settings, error) {
	keys := []string{
		SettingKeySoraS3Enabled,
		SettingKeySoraS3Endpoint,
		SettingKeySoraS3Region,
		SettingKeySoraS3Bucket,
		SettingKeySoraS3AccessKeyID,
		SettingKeySoraS3SecretAccessKey,
		SettingKeySoraS3Prefix,
		SettingKeySoraS3ForcePathStyle,
		SettingKeySoraS3CDNURL,
		SettingKeySoraDefaultStorageQuotaBytes,
	}

	values, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("get legacy sora s3 settings: %w", err)
	}

	result := &SoraS3Settings{
		Enabled:                   values[SettingKeySoraS3Enabled] == "true",
		Endpoint:                  values[SettingKeySoraS3Endpoint],
		Region:                    values[SettingKeySoraS3Region],
		Bucket:                    values[SettingKeySoraS3Bucket],
		AccessKeyID:               values[SettingKeySoraS3AccessKeyID],
		SecretAccessKey:           values[SettingKeySoraS3SecretAccessKey],
		SecretAccessKeyConfigured: values[SettingKeySoraS3SecretAccessKey] != "",
		Prefix:                    values[SettingKeySoraS3Prefix],
		ForcePathStyle:            values[SettingKeySoraS3ForcePathStyle] == "true",
		CDNURL:                    values[SettingKeySoraS3CDNURL],
	}
	if v, parseErr := strconv.ParseInt(values[SettingKeySoraDefaultStorageQuotaBytes], 10, 64); parseErr == nil {
		result.DefaultStorageQuotaBytes = v
	}
	return result, nil
}

func normalizeSoraS3ProfilesStore(store soraS3ProfilesStore) soraS3ProfilesStore {
	seen := make(map[string]struct{}, len(store.Items))
	normalized := soraS3ProfilesStore{
		ActiveProfileID: strings.TrimSpace(store.ActiveProfileID),
		Items:           make([]soraS3ProfileStoreItem, 0, len(store.Items)),
	}
	now := time.Now().UTC().Format(time.RFC3339)

	for idx := range store.Items {
		item := store.Items[idx]
		item.ProfileID = strings.TrimSpace(item.ProfileID)
		if item.ProfileID == "" {
			item.ProfileID = fmt.Sprintf("profile-%d", idx+1)
		}
		if _, exists := seen[item.ProfileID]; exists {
			continue
		}
		seen[item.ProfileID] = struct{}{}

		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" {
			item.Name = item.ProfileID
		}
		item.Endpoint = strings.TrimSpace(item.Endpoint)
		item.Region = strings.TrimSpace(item.Region)
		item.Bucket = strings.TrimSpace(item.Bucket)
		item.AccessKeyID = strings.TrimSpace(item.AccessKeyID)
		item.Prefix = strings.TrimSpace(item.Prefix)
		item.CDNURL = strings.TrimSpace(item.CDNURL)
		item.DefaultStorageQuotaBytes = maxInt64(item.DefaultStorageQuotaBytes, 0)
		item.UpdatedAt = strings.TrimSpace(item.UpdatedAt)
		if item.UpdatedAt == "" {
			item.UpdatedAt = now
		}
		normalized.Items = append(normalized.Items, item)
	}

	if len(normalized.Items) == 0 {
		normalized.ActiveProfileID = ""
		return normalized
	}

	if findSoraS3ProfileIndex(normalized.Items, normalized.ActiveProfileID) >= 0 {
		return normalized
	}
	normalized.ActiveProfileID = normalized.Items[0].ProfileID
	return normalized
}

func convertSoraS3ProfilesStore(store *soraS3ProfilesStore) *SoraS3ProfileList {
	if store == nil {
		return &SoraS3ProfileList{}
	}
	items := make([]SoraS3Profile, 0, len(store.Items))
	for idx := range store.Items {
		item := store.Items[idx]
		items = append(items, SoraS3Profile{
			ProfileID:                 item.ProfileID,
			Name:                      item.Name,
			IsActive:                  item.ProfileID == store.ActiveProfileID,
			Enabled:                   item.Enabled,
			Endpoint:                  item.Endpoint,
			Region:                    item.Region,
			Bucket:                    item.Bucket,
			AccessKeyID:               item.AccessKeyID,
			SecretAccessKey:           item.SecretAccessKey,
			SecretAccessKeyConfigured: item.SecretAccessKey != "",
			Prefix:                    item.Prefix,
			ForcePathStyle:            item.ForcePathStyle,
			CDNURL:                    item.CDNURL,
			DefaultStorageQuotaBytes:  item.DefaultStorageQuotaBytes,
			UpdatedAt:                 item.UpdatedAt,
		})
	}
	return &SoraS3ProfileList{ActiveProfileID: store.ActiveProfileID, Items: items}
}

func pickActiveSoraS3Profile(items []SoraS3Profile, activeProfileID string) *SoraS3Profile {
	for idx := range items {
		if items[idx].ProfileID == activeProfileID {
			return &items[idx]
		}
	}
	if len(items) == 0 {
		return nil
	}
	return &items[0]
}

func findSoraS3ProfileByID(items []SoraS3Profile, profileID string) *SoraS3Profile {
	for idx := range items {
		if items[idx].ProfileID == profileID {
			return &items[idx]
		}
	}
	return nil
}

func pickActiveSoraS3ProfileFromStore(items []soraS3ProfileStoreItem, activeProfileID string) *soraS3ProfileStoreItem {
	for idx := range items {
		if items[idx].ProfileID == activeProfileID {
			return &items[idx]
		}
	}
	if len(items) == 0 {
		return nil
	}
	return &items[0]
}

func findSoraS3ProfileIndex(items []soraS3ProfileStoreItem, profileID string) int {
	for idx := range items {
		if items[idx].ProfileID == profileID {
			return idx
		}
	}
	return -1
}

func hasSoraS3ProfileID(items []soraS3ProfileStoreItem, profileID string) bool {
	return findSoraS3ProfileIndex(items, profileID) >= 0
}

func isEmptyLegacySoraS3Settings(settings *SoraS3Settings) bool {
	if settings == nil {
		return true
	}
	if settings.Enabled {
		return false
	}
	if strings.TrimSpace(settings.Endpoint) != "" {
		return false
	}
	if strings.TrimSpace(settings.Region) != "" {
		return false
	}
	if strings.TrimSpace(settings.Bucket) != "" {
		return false
	}
	if strings.TrimSpace(settings.AccessKeyID) != "" {
		return false
	}
	if settings.SecretAccessKey != "" {
		return false
	}
	if strings.TrimSpace(settings.Prefix) != "" {
		return false
	}
	if strings.TrimSpace(settings.CDNURL) != "" {
		return false
	}
	return settings.DefaultStorageQuotaBytes == 0
}

func maxInt64(value int64, min int64) int64 {
	if value < min {
		return min
	}
	return value
}
