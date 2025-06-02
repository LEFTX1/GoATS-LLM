package storage

import (
	"context"
	"fmt"
	"time"

	"ai-agent-go/internal/config"    // Import the main config package
	"ai-agent-go/internal/constants" // 新增：导入集中的常量包

	"github.com/redis/go-redis/v9"
)

// Redis wraps the Redis client
type Redis struct {
	Client *redis.Client
	config *config.RedisConfig // Use config.RedisConfig
}

// NewRedisAdapter creates a new Redis client connection (renamed from NewRedis)
func NewRedisAdapter(cfg *config.RedisConfig) (*Redis, error) {
	if cfg == nil {
		return nil, fmt.Errorf("Redis config cannot be nil")
	}
	if cfg.Address == "" {
		return nil, fmt.Errorf("Redis address is required")
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
		return fmt.Errorf("Redis client is not initialized")
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
	return r.addMD5WithExpirationInternal(ctx, constants.RawFileMD5SetKey, md5Hex)
}

// AddParsedTextMD5 添加解析后的文本MD5到集合并设置过期时间
func (r *Redis) AddParsedTextMD5(ctx context.Context, md5Hex string) error {
	return r.addMD5WithExpirationInternal(ctx, constants.ParsedTextMD5SetKey, md5Hex)
}

// addMD5WithExpirationInternal 内部辅助函数，用于添加MD5到指定集合并设置过期时间
func (r *Redis) addMD5WithExpirationInternal(ctx context.Context, setKey string, md5Hex string) error {
	if r.Client == nil {
		return fmt.Errorf("Redis client is not initialized")
	}
	pipe := r.Client.Pipeline()
	pipe.SAdd(ctx, setKey, md5Hex)
	pipe.ExpireNX(ctx, setKey, r.GetMD5ExpireDuration())
	_, err := pipe.Exec(ctx)
	return err
}

// CheckRawFileMD5Exists 检查原始文件MD5是否存在于Redis Set中。
func (r *Redis) CheckRawFileMD5Exists(ctx context.Context, md5Hex string) (bool, error) {
	return r.checkMD5ExistsInternal(ctx, constants.RawFileMD5SetKey, md5Hex)
}

// CheckParsedTextMD5Exists 检查解析后的文本MD5是否存在于Redis Set中。
func (r *Redis) CheckParsedTextMD5Exists(ctx context.Context, md5Hex string) (bool, error) {
	return r.checkMD5ExistsInternal(ctx, constants.ParsedTextMD5SetKey, md5Hex)
}

// checkMD5ExistsInternal 内部辅助函数，用于检查MD5是否存在于指定集合
func (r *Redis) checkMD5ExistsInternal(ctx context.Context, setKey string, md5Hex string) (bool, error) {
	if r.Client == nil {
		return false, fmt.Errorf("Redis client is not initialized")
	}
	return r.Client.SIsMember(ctx, setKey, md5Hex).Result()
}

// Deprecated: AddMD5WithExpiration 添加MD5到集合并设置过期时间. 请使用 AddRawFileMD5 或 AddParsedTextMD5 代替。
func (r *Redis) AddMD5WithExpiration(ctx context.Context, key string, md5 string) error {
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
	if r.Client == nil {
		return false, fmt.Errorf("Redis client is not initialized")
	}
	return r.Client.SIsMember(ctx, setKey, md5Hex).Result()
}
