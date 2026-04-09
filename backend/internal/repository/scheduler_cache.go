package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	schedulerBucketSetKey       = "sched:buckets"
	schedulerOutboxWatermarkKey = "sched:outbox:watermark"
	schedulerAccountPrefix      = "sched:acc:"
	schedulerAccountFullPrefix  = "sched:acc:full:"
	schedulerActivePrefix       = "sched:active:"
	schedulerReadyPrefix        = "sched:ready:"
	schedulerVersionPrefix      = "sched:ver:"
	schedulerSnapshotPrefix     = "sched:"
	schedulerLockPrefix         = "sched:lock:"
	schedulerSnapshotStringMax  = 512
)

type schedulerCache struct {
	rdb *redis.Client
}

var schedulerSnapshotCredentialAllowlist = map[string]struct{}{
	"api_key":         {},
	"project_id":      {},
	"oauth_type":      {},
	"model_mapping":   {},
	"tier_id":         {},
	"organization_id": {},
	"base_url":        {},
	"endpoint":        {},
	"api_version":     {},
	"deployment_name": {},
	"deployment_id":   {},
	"resource_name":   {},
	"region":          {},
}

func NewSchedulerCache(rdb *redis.Client) service.SchedulerCache {
	return &schedulerCache{rdb: rdb}
}

func (c *schedulerCache) GetSnapshot(ctx context.Context, bucket service.SchedulerBucket) ([]*service.Account, bool, error) {
	readyKey := schedulerBucketKey(schedulerReadyPrefix, bucket)
	readyVal, err := c.rdb.Get(ctx, readyKey).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if readyVal != "1" {
		return nil, false, nil
	}

	activeKey := schedulerBucketKey(schedulerActivePrefix, bucket)
	activeVal, err := c.rdb.Get(ctx, activeKey).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	snapshotKey := schedulerSnapshotKey(bucket, activeVal)
	ids, err := c.rdb.ZRange(ctx, snapshotKey, 0, -1).Result()
	if err != nil {
		return nil, false, err
	}
	if len(ids) == 0 {
		// 空快照视为缓存未命中，触发数据库回退查询
		// 这解决了新分组创建后立即绑定账号时的竞态条件问题
		return nil, false, nil
	}

	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, schedulerAccountKey(id))
	}
	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, false, err
	}

	accounts := make([]*service.Account, 0, len(values))
	for _, val := range values {
		if val == nil {
			return nil, false, nil
		}
		account, err := decodeCachedAccount(val)
		if err != nil {
			return nil, false, err
		}
		accounts = append(accounts, account)
	}

	return accounts, true, nil
}

func (c *schedulerCache) SetSnapshot(ctx context.Context, bucket service.SchedulerBucket, accounts []service.Account) error {
	activeKey := schedulerBucketKey(schedulerActivePrefix, bucket)
	oldActive, _ := c.rdb.Get(ctx, activeKey).Result()

	versionKey := schedulerBucketKey(schedulerVersionPrefix, bucket)
	version, err := c.rdb.Incr(ctx, versionKey).Result()
	if err != nil {
		return err
	}

	versionStr := strconv.FormatInt(version, 10)
	snapshotKey := schedulerSnapshotKey(bucket, versionStr)

	pipe := c.rdb.Pipeline()
	for _, account := range accounts {
		slimPayload, fullPayload, err := marshalSchedulerCachedAccounts(account)
		if err != nil {
			return err
		}
		accountID := strconv.FormatInt(account.ID, 10)
		pipe.Set(ctx, schedulerAccountKey(accountID), slimPayload, 0)
		pipe.Set(ctx, schedulerAccountFullKey(accountID), fullPayload, 0)
	}
	if len(accounts) > 0 {
		// 使用序号作为 score，保持数据库返回的排序语义。
		members := make([]redis.Z, 0, len(accounts))
		for idx, account := range accounts {
			members = append(members, redis.Z{
				Score:  float64(idx),
				Member: strconv.FormatInt(account.ID, 10),
			})
		}
		pipe.ZAdd(ctx, snapshotKey, members...)
	} else {
		pipe.Del(ctx, snapshotKey)
	}
	pipe.Set(ctx, activeKey, versionStr, 0)
	pipe.Set(ctx, schedulerBucketKey(schedulerReadyPrefix, bucket), "1", 0)
	pipe.SAdd(ctx, schedulerBucketSetKey, bucket.String())
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}

	if oldActive != "" && oldActive != versionStr {
		_ = c.rdb.Del(ctx, schedulerSnapshotKey(bucket, oldActive)).Err()
	}

	return nil
}

func (c *schedulerCache) GetAccount(ctx context.Context, accountID int64) (*service.Account, error) {
	id := strconv.FormatInt(accountID, 10)
	fullKey := schedulerAccountFullKey(id)
	val, err := c.rdb.Get(ctx, fullKey).Result()
	switch {
	case err == nil:
		return decodeCachedAccount(val)
	case err != redis.Nil:
		return nil, err
	}

	legacyKey := schedulerAccountKey(id)
	val, err = c.rdb.Get(ctx, legacyKey).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeCachedAccount(val)
}

func (c *schedulerCache) SetAccount(ctx context.Context, account *service.Account) error {
	if account == nil || account.ID <= 0 {
		return nil
	}
	slimPayload, fullPayload, err := marshalSchedulerCachedAccounts(*account)
	if err != nil {
		return err
	}
	id := strconv.FormatInt(account.ID, 10)
	pipe := c.rdb.Pipeline()
	pipe.Set(ctx, schedulerAccountKey(id), slimPayload, 0)
	pipe.Set(ctx, schedulerAccountFullKey(id), fullPayload, 0)
	_, err = pipe.Exec(ctx)
	return err
}

func (c *schedulerCache) DeleteAccount(ctx context.Context, accountID int64) error {
	if accountID <= 0 {
		return nil
	}
	id := strconv.FormatInt(accountID, 10)
	return c.rdb.Del(ctx, schedulerAccountKey(id), schedulerAccountFullKey(id)).Err()
}

func (c *schedulerCache) UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	if len(updates) == 0 {
		return nil
	}

	slimKeys := make([]string, 0, len(updates))
	fullKeys := make([]string, 0, len(updates))
	ids := make([]int64, 0, len(updates))
	for id := range updates {
		accountID := strconv.FormatInt(id, 10)
		slimKeys = append(slimKeys, schedulerAccountKey(accountID))
		fullKeys = append(fullKeys, schedulerAccountFullKey(accountID))
		ids = append(ids, id)
	}

	fullValues, err := c.rdb.MGet(ctx, fullKeys...).Result()
	if err != nil {
		return err
	}
	slimValues, err := c.rdb.MGet(ctx, slimKeys...).Result()
	if err != nil {
		return err
	}

	pipe := c.rdb.Pipeline()
	for i, val := range fullValues {
		if val == nil {
			val = slimValues[i]
		}
		if val == nil {
			continue
		}
		account, err := decodeCachedAccount(val)
		if err != nil {
			return err
		}
		account.LastUsedAt = ptrTime(updates[ids[i]])
		slimPayload, fullPayload, err := marshalSchedulerCachedAccounts(*account)
		if err != nil {
			return err
		}
		pipe.Set(ctx, slimKeys[i], slimPayload, 0)
		pipe.Set(ctx, fullKeys[i], fullPayload, 0)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (c *schedulerCache) TryLockBucket(ctx context.Context, bucket service.SchedulerBucket, ttl time.Duration) (bool, error) {
	key := schedulerBucketKey(schedulerLockPrefix, bucket)
	return c.rdb.SetNX(ctx, key, time.Now().UnixNano(), ttl).Result()
}

func (c *schedulerCache) ListBuckets(ctx context.Context) ([]service.SchedulerBucket, error) {
	raw, err := c.rdb.SMembers(ctx, schedulerBucketSetKey).Result()
	if err != nil {
		return nil, err
	}
	out := make([]service.SchedulerBucket, 0, len(raw))
	for _, entry := range raw {
		bucket, ok := service.ParseSchedulerBucket(entry)
		if !ok {
			continue
		}
		out = append(out, bucket)
	}
	return out, nil
}

func (c *schedulerCache) GetOutboxWatermark(ctx context.Context) (int64, error) {
	val, err := c.rdb.Get(ctx, schedulerOutboxWatermarkKey).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (c *schedulerCache) SetOutboxWatermark(ctx context.Context, id int64) error {
	return c.rdb.Set(ctx, schedulerOutboxWatermarkKey, strconv.FormatInt(id, 10), 0).Err()
}

func schedulerBucketKey(prefix string, bucket service.SchedulerBucket) string {
	return fmt.Sprintf("%s%d:%s:%s", prefix, bucket.GroupID, bucket.Platform, bucket.Mode)
}

func schedulerSnapshotKey(bucket service.SchedulerBucket, version string) string {
	return fmt.Sprintf("%s%d:%s:%s:v%s", schedulerSnapshotPrefix, bucket.GroupID, bucket.Platform, bucket.Mode, version)
}

func schedulerAccountKey(id string) string {
	return schedulerAccountPrefix + id
}

func schedulerAccountFullKey(id string) string {
	return schedulerAccountFullPrefix + id
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func decodeCachedAccount(val any) (*service.Account, error) {
	var payload []byte
	switch raw := val.(type) {
	case string:
		payload = []byte(raw)
	case []byte:
		payload = raw
	default:
		return nil, fmt.Errorf("unexpected account cache type: %T", val)
	}
	var account service.Account
	if err := json.Unmarshal(payload, &account); err != nil {
		return nil, err
	}
	return &account, nil
}

func marshalSchedulerCachedAccounts(account service.Account) ([]byte, []byte, error) {
	slimPayload, err := json.Marshal(buildSchedulerSlimAccount(account))
	if err != nil {
		return nil, nil, err
	}
	fullPayload, err := json.Marshal(account)
	if err != nil {
		return nil, nil, err
	}
	return slimPayload, fullPayload, nil
}

func buildSchedulerSlimAccount(account service.Account) service.Account {
	return service.Account{
		ID:                      account.ID,
		Name:                    account.Name,
		Notes:                   account.Notes,
		Platform:                account.Platform,
		Type:                    account.Type,
		Credentials:             filterSchedulerSnapshotCredentials(account.Credentials),
		Extra:                   filterSchedulerSnapshotExtra(account.Extra),
		ProxyID:                 account.ProxyID,
		Concurrency:             account.Concurrency,
		Priority:                account.Priority,
		RateMultiplier:          account.RateMultiplier,
		LoadFactor:              account.LoadFactor,
		Status:                  account.Status,
		ErrorMessage:            account.ErrorMessage,
		LastUsedAt:              account.LastUsedAt,
		ExpiresAt:               account.ExpiresAt,
		AutoPauseOnExpired:      account.AutoPauseOnExpired,
		CreatedAt:               account.CreatedAt,
		UpdatedAt:               account.UpdatedAt,
		Schedulable:             account.Schedulable,
		RateLimitedAt:           account.RateLimitedAt,
		RateLimitResetAt:        account.RateLimitResetAt,
		OverloadUntil:           account.OverloadUntil,
		TempUnschedulableUntil:  account.TempUnschedulableUntil,
		TempUnschedulableReason: account.TempUnschedulableReason,
		SessionWindowStart:      account.SessionWindowStart,
		SessionWindowEnd:        account.SessionWindowEnd,
		SessionWindowStatus:     account.SessionWindowStatus,
		GroupIDs:                append([]int64(nil), account.GroupIDs...),
	}
}

func filterSchedulerSnapshotCredentials(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any)
	for key, value := range src {
		if _, ok := schedulerSnapshotCredentialAllowlist[key]; !ok {
			continue
		}
		if key == "model_mapping" {
			if copied := copySchedulerModelMapping(value); len(copied) > 0 {
				out[key] = copied
			}
			continue
		}
		if copied, ok := copySchedulerScalarValue(value); ok {
			out[key] = copied
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func filterSchedulerSnapshotExtra(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any)
	for key, value := range src {
		if copied, ok := copySchedulerScalarValue(value); ok {
			out[key] = copied
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func copySchedulerModelMapping(value any) map[string]any {
	switch raw := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(raw))
		for k, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				out[k] = s
			}
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(raw))
		for k, v := range raw {
			if v != "" {
				out[k] = v
			}
		}
		return out
	default:
		return nil
	}
}

func copySchedulerScalarValue(value any) (any, bool) {
	switch v := value.(type) {
	case nil:
		return nil, false
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return v, true
	case string:
		if v == "" || len(v) > schedulerSnapshotStringMax {
			return nil, false
		}
		return v, true
	default:
		return nil, false
	}
}
