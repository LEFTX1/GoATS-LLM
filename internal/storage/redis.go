package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-agent-go/internal/config"    // Import the main config package
	"ai-agent-go/internal/constants" // 新增：导入集中的常量包

	"github.com/redis/go-redis/v9"
)

const defaultTenantID = "default_tenant" // 临时的默认租户ID，后续应替换为实际逻辑

// Redis wraps the Redis client
type Redis struct {
	Client *redis.Client
	config *config.RedisConfig // Use config.RedisConfig
}

// FormatKey 是一个辅助函数，用于格式化包含租户ID和其他部分的Redis键。
// tenantID: 当前操作的租户ID。
// keyConstant: 来自 constants 包的键常量，其中包含 TenantPlaceholder。
// parts: 附加到键的动态部分，例如具体的 job_id, md5 等。
func (r *Redis) FormatKey(keyConstant string, parts ...string) string {
	// 实际应用中，tenantID 可能来自 context 或 r.config (如果租户特定)
	tenantID := defaultTenantID // 使用临时的默认值
	base := strings.Replace(keyConstant, constants.TenantPlaceholder, tenantID, 1)
	if len(parts) > 0 {
		return base + strings.Join(parts, ":")
	}
	return base
}

// NewRedisAdapter creates a new Redis client connection (renamed from NewRedis)
func NewRedisAdapter(cfg *config.RedisConfig) (*Redis, error) {
	if cfg == nil {
		return nil, fmt.Errorf("redis config cannot be nil")
	}
	if cfg.Address == "" {
		return nil, fmt.Errorf("redis address is required")
	}

	// 使用扩展的配置选项
	opt := &redis.Options{
		Addr:     cfg.Address,
		Password: cfg.Password,
		DB:       cfg.DB,

		// 连接池设置
		PoolSize:     cfg.PoolSize,     // 默认10
		MinIdleConns: cfg.MinIdleConns, // 默认2

		// 超时设置
		DialTimeout:  time.Duration(cfg.DialTimeoutSeconds) * time.Second,  // 默认5秒
		ReadTimeout:  time.Duration(cfg.ReadTimeoutSeconds) * time.Second,  // 默认3秒
		WriteTimeout: time.Duration(cfg.WriteTimeoutSeconds) * time.Second, // 默认3秒

		// 重试设置
		MaxRetries:      cfg.MaxRetries,                                          // 默认3次
		MinRetryBackoff: time.Duration(cfg.MinRetryBackoffMS) * time.Millisecond, // 默认8毫秒
		MaxRetryBackoff: time.Duration(cfg.MaxRetryBackoffMS) * time.Millisecond, // 默认512毫秒

		// 连接生命周期
		ConnMaxLifetime: time.Duration(cfg.ConnMaxLifetimeMinutes) * time.Minute, // 默认60分钟
		ConnMaxIdleTime: time.Duration(cfg.ConnMaxIdleTimeMinutes) * time.Minute, // 默认30分钟
	}

	client := redis.NewClient(opt)

	// Ping to check connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.Ping(ctx).Result(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis at %s: %w", cfg.Address, err)
	}

	return &Redis{
		Client: client,
		config: cfg,
	}, nil
}

// Close closes the Redis client connection
func (r *Redis) Close() error {
	if r.Client != nil {
		return r.Client.Close()
	}
	return nil
}

// Ping checks the Redis connection
func (r *Redis) Ping(ctx context.Context) error {
	if r.Client == nil {
		return fmt.Errorf("redis client is not initialized")
	}
	return r.Client.Ping(ctx).Err()
}

// GetMD5ExpireDuration 返回配置的MD5记录过期时间
func (r *Redis) GetMD5ExpireDuration() time.Duration {
	days := r.config.MD5RecordExpireDays
	if days <= 0 {
		days = 365 // 默认1年
	}
	return time.Duration(days) * 24 * time.Hour
}

// AddRawFileMD5 添加原始文件MD5到集合并设置过期时间
func (r *Redis) AddRawFileMD5(ctx context.Context, md5Hex string) error {
	key := r.FormatKey(constants.RawFileMD5SetKey)
	return r.addMD5WithExpirationInternal(ctx, key, md5Hex)
}

// AddParsedTextMD5 添加解析后的文本MD5到集合并设置过期时间
func (r *Redis) AddParsedTextMD5(ctx context.Context, md5Hex string) error {
	key := r.FormatKey(constants.ParsedTextMD5SetKey)
	return r.addMD5WithExpirationInternal(ctx, key, md5Hex)
}

// addMD5WithExpirationInternal 内部辅助函数，用于添加MD5到指定集合并设置过期时间
func (r *Redis) addMD5WithExpirationInternal(ctx context.Context, setKey string, md5Hex string) error {
	if r.Client == nil {
		return fmt.Errorf("redis client is not initialized")
	}
	pipe := r.Client.Pipeline()
	pipe.SAdd(ctx, setKey, md5Hex)
	pipe.ExpireNX(ctx, setKey, r.GetMD5ExpireDuration()) // ExpireNX: Set expiry only if it does not already exist.
	_, err := pipe.Exec(ctx)
	return err
}

// CheckRawFileMD5Exists 检查原始文件MD5是否存在于Redis Set中。
func (r *Redis) CheckRawFileMD5Exists(ctx context.Context, md5Hex string) (bool, error) {
	key := r.FormatKey(constants.RawFileMD5SetKey)
	return r.checkMD5ExistsInternal(ctx, key, md5Hex)
}

// CheckParsedTextMD5Exists 检查解析后的文本MD5是否存在于Redis Set中。
func (r *Redis) CheckParsedTextMD5Exists(ctx context.Context, md5Hex string) (bool, error) {
	key := r.FormatKey(constants.ParsedTextMD5SetKey)
	return r.checkMD5ExistsInternal(ctx, key, md5Hex)
}

// checkMD5ExistsInternal 内部辅助函数，用于检查MD5是否存在于指定集合
func (r *Redis) checkMD5ExistsInternal(ctx context.Context, setKey string, md5Hex string) (bool, error) {
	if r.Client == nil {
		return false, fmt.Errorf("redis client is not initialized")
	}
	return r.Client.SIsMember(ctx, setKey, md5Hex).Result()
}

// Deprecated: AddMD5WithExpiration 添加MD5到集合并设置过期时间. 请使用 AddRawFileMD5 或 AddParsedTextMD5 代替。
func (r *Redis) AddMD5WithExpiration(ctx context.Context, key string, md5 string) error {
	// This method is deprecated. The 'key' parameter here was the full key including tenant.
	// New methods FormatKey internally. For direct full key usage, it's better to use client directly
	// or ensure key formation is consistent.
	pipe := r.Client.Pipeline()

	// 添加MD5到Set
	pipe.SAdd(ctx, key, md5)

	// 确保key有过期时间（不覆盖已有的过期时间）
	pipe.ExpireNX(ctx, key, r.GetMD5ExpireDuration())

	_, err := pipe.Exec(ctx)
	return err
}

// Deprecated: CheckMD5Exists 检查给定的 MD5 是否存在于指定的 Redis Set 中。请使用 CheckRawFileMD5Exists 或 CheckParsedTextMD5Exists 代替。
func (r *Redis) CheckMD5Exists(ctx context.Context, setKey string, md5Hex string) (bool, error) {
	// This method is deprecated. Similar to AddMD5WithExpiration.
	if r.Client == nil {
		return false, fmt.Errorf("redis client is not initialized")
	}
	return r.Client.SIsMember(ctx, setKey, md5Hex).Result()
}

// SetJobVector 缓存JD向量
// vector: float64 类型的向量，将进行JSON序列化存储
// modelVersion: 生成该向量的嵌入模型版本
func (r *Redis) SetJobVector(ctx context.Context, jobID string, vector []float64, modelVersion string) error {
	if r.Client == nil {
		return fmt.Errorf("redis client is not initialized")
	}
	key := r.FormatKey(constants.JobVectorCachePrefix, jobID)

	// 将向量和模型版本一起存储，例如在一个JSON对象中
	cachedData := map[string]interface{}{
		"vector":        vector,
		"model_version": modelVersion,
	}
	jsonData, err := json.Marshal(cachedData)
	if err != nil {
		return fmt.Errorf("序列化JD向量缓存数据失败: %w", err)
	}

	return r.Client.Set(ctx, key, jsonData, constants.JDCacheDuration).Err() // 使用常量中定义的过期时间
}

// GetJobVector 从缓存获取JD向量和模型版本
func (r *Redis) GetJobVector(ctx context.Context, jobID string) ([]float64, string, error) {
	if r.Client == nil {
		return nil, "", fmt.Errorf("redis client is not initialized")
	}
	key := r.FormatKey(constants.JobVectorCachePrefix, jobID)

	val, err := r.Client.Get(ctx, key).Result()
	if err != nil {
		return nil, "", err // 包括 redis.Nil 错误
	}

	var cachedData struct {
		Vector       []float64 `json:"vector"`
		ModelVersion string    `json:"model_version"`
	}

	if err := json.Unmarshal([]byte(val), &cachedData); err != nil {
		return nil, "", fmt.Errorf("反序列化JD向量缓存数据失败: %w", err)
	}

	return cachedData.Vector, cachedData.ModelVersion, nil
}

// SetJobKeywords 缓存JD关键词
// keywordsJSON: 已经是JSON字符串的关键词列表
func (r *Redis) SetJobKeywords(ctx context.Context, jobID string, keywordsJSON string) error {
	if r.Client == nil {
		return fmt.Errorf("redis client is not initialized")
	}
	key := r.FormatKey(constants.JobKeywordsCachePrefix, jobID)
	return r.Client.Set(ctx, key, keywordsJSON, constants.JDCacheDuration).Err()
}

// GetJobKeywords 从缓存获取JD关键词 (JSON字符串形式)
func (r *Redis) GetJobKeywords(ctx context.Context, jobID string) (string, error) {
	if r.Client == nil {
		return "", fmt.Errorf("redis client is not initialized")
	}
	key := r.FormatKey(constants.JobKeywordsCachePrefix, jobID)
	val, err := r.Client.Get(ctx, key).Result()
	if err != nil {
		return "", err // 包括 redis.Nil 错误
	}
	return val, nil
}

// CheckAndAddRawFileMD5 使用Lua脚本原子地检查MD5是否存在并添加（如不存在）
// 返回值: exists - 如果MD5已存在则为true，error - 操作错误
func (r *Redis) CheckAndAddRawFileMD5(ctx context.Context, md5Hex string) (exists bool, err error) {
	script := redis.NewScript(`
	local exists = redis.call('SISMEMBER', KEYS[1], ARGV[1])
	if exists == 0 then
		redis.call('SADD', KEYS[1], ARGV[1])
		-- 设置过期时间（如果配置了且key没有过期时间）
		local ttl = redis.call('TTL', KEYS[1])
		if ttl < 0 then
			redis.call('EXPIRE', KEYS[1], ARGV[2])
		end
		return 0
	else
		return 1
	end
	`)

	expireSeconds := int(r.GetMD5ExpireDuration().Seconds())
	setKey := r.FormatKey(constants.RawFileMD5SetKey)

	result, err := script.Run(ctx, r.Client, []string{setKey}, md5Hex, expireSeconds).Int()
	if err != nil {
		return false, fmt.Errorf("执行Redis Lua脚本失败: %w", err)
	}

	return result == 1, nil // 1表示已存在，0表示新添加
}

// CheckAndAddParsedTextMD5 使用Lua脚本原子地检查解析文本MD5是否存在并添加（如不存在）
// 返回值: exists - 如果MD5已存在则为true，error - 操作错误
func (r *Redis) CheckAndAddParsedTextMD5(ctx context.Context, md5Hex string) (exists bool, err error) {
	script := redis.NewScript(`
	local exists = redis.call('SISMEMBER', KEYS[1], ARGV[1])
	if exists == 0 then
		redis.call('SADD', KEYS[1], ARGV[1])
		-- 设置过期时间（如果配置了且key没有过期时间）
		local ttl = redis.call('TTL', KEYS[1])
		if ttl < 0 then
			redis.call('EXPIRE', KEYS[1], ARGV[2])
		end
		return 0
	else
		return 1
	end
	`)

	expireSeconds := int(r.GetMD5ExpireDuration().Seconds())
	setKey := r.FormatKey(constants.ParsedTextMD5SetKey)

	result, err := script.Run(ctx, r.Client, []string{setKey}, md5Hex, expireSeconds).Int()
	if err != nil {
		return false, fmt.Errorf("执行Redis Lua脚本失败: %w", err)
	}

	return result == 1, nil // 1表示已存在，0表示新添加
}

// RemoveRawFileMD5 从原始文件MD5集合中移除一个MD5（用于事务补偿）
func (r *Redis) RemoveRawFileMD5(ctx context.Context, md5 string) error {
	key := r.FormatKey(constants.RawFileMD5SetKey)
	if err := r.Client.SRem(ctx, key, md5).Err(); err != nil {
		return fmt.Errorf("从Redis集合 %s 移除MD5 %s 失败: %w", key, md5, err)
	}
	return nil
}
