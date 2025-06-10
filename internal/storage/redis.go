package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"ai-agent-go/internal/config"    // Import the main config package
	"ai-agent-go/internal/constants" // 新增：导入集中的常量包
	"ai-agent-go/internal/types"

	"github.com/redis/go-redis/extra/redisotel/v9" // 添加Redis OpenTelemetry钩子包
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

const defaultTenantID = "default_tenant" // 临时的默认租户ID，后续应替换为实际逻辑

// ErrNotFound is returned when a key is not found in Redis.
// It wraps the underlying redis.Nil error for abstraction.
var ErrNotFound = redis.Nil

// 为Redis操作定义专用tracer
var redisTracer = otel.Tracer("ai-agent-go/storage/redis")

// Redis操作前缀采样率配置
var redisKeySamplingRates = map[string]float64{
	"user:":    0.1,  // 用户相关操作采样10%
	"session:": 0.05, // 会话相关操作采样5%
	"counter:": 0.01, // 计数器操作采样1%
	"cache:":   0.01, // 缓存操作采样1%
	"lock:":    0.5,  // 锁操作采样50%
	"job:":     0.25, // 任务相关操作采样25%
	"notify:":  0.5,  // 通知相关采样50%
	"stream:":  0.1,  // 流操作采样10%
}

// 随机数生成器
var (
	rnd      *rand.Rand
	rndMutex sync.Mutex
)

// 初始化随机数生成器
func init() {
	source := rand.NewSource(time.Now().UnixNano())
	rnd = rand.New(source)
}

// shouldSampleRedisOp 根据key前缀决定是否需要创建span
func shouldSampleRedisOp(key string) bool {
	// key为空一定不采样
	if key == "" {
		return false
	}

	// 遍历前缀采样率配置
	for prefix, rate := range redisKeySamplingRates {
		if strings.HasPrefix(key, prefix) {
			// 使用线程安全的随机数
			return randFloat() < rate
		}
	}

	// 默认采样率5%
	return randFloat() < 0.05
}

// 生成0-1之间的随机数
func randFloat() float64 {
	rndMutex.Lock()
	defer rndMutex.Unlock()
	return rnd.Float64()
}

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

	// 添加OpenTelemetry钩子, 记录所有Redis操作
	if err := redisotel.InstrumentTracing(client); err != nil {
		return nil, fmt.Errorf("failed to instrument Redis with OpenTelemetry: %w", err)
	}

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
	// 创建一个命名span
	ctx, span := redisTracer.Start(ctx, "Redis.SetJobVector",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 添加属性
	span.SetAttributes(
		semconv.DBSystemRedis,
		attribute.String("db.redis.database", fmt.Sprintf("%d", r.config.DB)),
		attribute.String("net.peer.name", r.config.Address),
		attribute.String("db.operation", "JSON_SET"),
		attribute.String("job_id", jobID),
		attribute.String("model_version", modelVersion),
		attribute.Int("vector_dimensions", len(vector)),
	)

	if r.Client == nil {
		err := fmt.Errorf("redis client is not initialized")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	key := r.FormatKey(constants.JobVectorCachePrefix, jobID)

	// 将向量和模型版本一起存储，例如在一个JSON对象中
	cachedData := map[string]interface{}{
		"vector":        vector,
		"model_version": modelVersion,
	}
	jsonData, err := json.Marshal(cachedData)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("序列化JD向量缓存数据失败: %w", err)
	}

	// 使用带上下文的命令
	err = r.Client.Set(ctx, key, jsonData, 0).Err()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("缓存JD向量失败: %w", err)
	}

	span.SetStatus(codes.Ok, "")
	return nil
}

// GetJobVector 从缓存获取JD向量和模型版本
func (r *Redis) GetJobVector(ctx context.Context, jobID string) ([]float64, string, error) {
	// 创建一个命名span
	ctx, span := redisTracer.Start(ctx, "Redis.GetJobVector",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 添加属性
	span.SetAttributes(
		semconv.DBSystemRedis,
		attribute.String("db.redis.database", fmt.Sprintf("%d", r.config.DB)),
		attribute.String("net.peer.name", r.config.Address),
		attribute.String("db.operation", "GET"),
		attribute.String("job_id", jobID),
	)

	if r.Client == nil {
		err := fmt.Errorf("redis client is not initialized")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, "", err
	}

	key := r.FormatKey(constants.JobVectorCachePrefix, jobID)

	// 从Redis获取已序列化的数据
	jsonData, err := r.Client.Get(ctx, key).Result()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if err == redis.Nil {
			return nil, "", fmt.Errorf("未找到JD向量缓存，jobID=%s: %w", jobID, err)
		}
		return nil, "", fmt.Errorf("获取JD向量缓存失败: %w", err)
	}

	// 解析JSON数据，使用map和具体类型
	type vectorData struct {
		Vector       []float64 `json:"vector"`
		ModelVersion string    `json:"model_version"`
	}

	var data vectorData
	if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, "", fmt.Errorf("解析JD向量缓存数据失败: %w", err)
	}

	// 回落机制：如果新格式解析失败，尝试旧格式
	if len(data.Vector) == 0 || data.ModelVersion == "" {
		var oldFormat map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &oldFormat); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, "", fmt.Errorf("解析JD向量缓存数据失败(旧格式): %w", err)
		}

		// 尝试提取向量和模型版本
		vectorData, ok := oldFormat["vector"].([]interface{})
		if !ok {
			err := fmt.Errorf("缓存数据中的向量格式无效")
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, "", err
		}

		modelVersion, ok := oldFormat["model_version"].(string)
		if !ok {
			err := fmt.Errorf("缓存数据中的模型版本格式无效")
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, "", err
		}

		// 将[]interface{}转换回[]float64
		vector := make([]float64, len(vectorData))
		for i, v := range vectorData {
			if f, ok := v.(float64); ok {
				vector[i] = f
			} else {
				err := fmt.Errorf("向量元素类型无效，期望float64但得到%T", v)
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				return nil, "", err
			}
		}

		data.Vector = vector
		data.ModelVersion = modelVersion
	}

	span.SetAttributes(
		attribute.Int("vector_dimensions", len(data.Vector)),
		attribute.String("model_version", data.ModelVersion),
	)
	span.SetStatus(codes.Ok, "")

	return data.Vector, data.ModelVersion, nil
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

// CheckAndAddRawFileMD5 检查并添加原始文件MD5到集合，是一个原子操作
func (r *Redis) CheckAndAddRawFileMD5(ctx context.Context, md5Hex string) (exists bool, err error) {
	// 创建一个命名span
	ctx, span := redisTracer.Start(ctx, "Redis.CheckAndAddRawFileMD5",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 添加属性
	span.SetAttributes(
		semconv.DBSystemRedis,
		attribute.String("db.redis.database", fmt.Sprintf("%d", r.config.DB)),
		attribute.String("net.peer.name", r.config.Address),
		attribute.String("db.operation", "EVAL"), // 使用标准操作名，表明这是一个Lua脚本执行
		attribute.String("db.redis.key", constants.RawFileMD5SetKey),
		attribute.String("db.redis.member", md5Hex),
	)

	// 验证客户端是否初始化
	if r.Client == nil {
		err = fmt.Errorf("redis client is not initialized")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return false, err
	}

	// 获取格式化的key
	key := r.FormatKey(constants.RawFileMD5SetKey)

	// 这里使用Redis LUA脚本进行原子检查和添加
	script := `
		local exists = redis.call('SISMEMBER', KEYS[1], ARGV[1])
		redis.call('SADD', KEYS[1], ARGV[1])
		redis.call('EXPIRE', KEYS[1], ARGV[2])
		return exists
	`

	expiry := r.GetMD5ExpireDuration().Seconds()

	// 执行lua脚本
	res, err := r.Client.Eval(ctx, script, []string{key}, md5Hex, expiry).Result()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return false, fmt.Errorf("执行原子检查和添加操作失败: %w", err)
	}

	// Lua脚本返回0表示不存在，1表示存在
	existsVal, ok := res.(int64)
	if !ok {
		err := fmt.Errorf("意外的Redis返回类型: %T", res)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return false, err
	}

	exists = existsVal == 1
	span.SetAttributes(attribute.Bool("already_exists", exists))
	span.SetStatus(codes.Ok, "")

	return exists, nil
}

// CheckAndAddParsedTextMD5 检查并添加解析后文本MD5到集合，是一个原子操作
func (r *Redis) CheckAndAddParsedTextMD5(ctx context.Context, md5Hex string) (exists bool, err error) {
	// 创建一个命名span
	ctx, span := redisTracer.Start(ctx, "Redis.CheckAndAddParsedTextMD5",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 添加属性
	span.SetAttributes(
		semconv.DBSystemRedis,
		attribute.String("db.redis.database", fmt.Sprintf("%d", r.config.DB)),
		attribute.String("net.peer.name", r.config.Address),
		attribute.String("db.operation", "EVAL"), // 使用标准操作名
		attribute.String("db.redis.key", constants.ParsedTextMD5SetKey),
		attribute.String("db.redis.member", md5Hex),
	)

	// 验证客户端是否初始化
	if r.Client == nil {
		err = fmt.Errorf("redis client is not initialized")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return false, err
	}

	// 获取格式化的key
	key := r.FormatKey(constants.ParsedTextMD5SetKey)

	// 这里使用Redis LUA脚本进行原子检查和添加
	script := `
		local exists = redis.call('SISMEMBER', KEYS[1], ARGV[1])
		redis.call('SADD', KEYS[1], ARGV[1])
		redis.call('EXPIRE', KEYS[1], ARGV[2])
		return exists
	`

	expiry := r.GetMD5ExpireDuration().Seconds()

	// 执行lua脚本
	res, err := r.Client.Eval(ctx, script, []string{key}, md5Hex, expiry).Result()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return false, fmt.Errorf("执行原子检查和添加操作失败: %w", err)
	}

	// Lua脚本返回0表示不存在，1表示存在
	existsVal, ok := res.(int64)
	if !ok {
		err := fmt.Errorf("意外的Redis返回类型: %T", res)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return false, err
	}

	exists = existsVal == 1
	span.SetAttributes(attribute.Bool("already_exists", exists))
	span.SetStatus(codes.Ok, "")

	return exists, nil
}

// RemoveRawFileMD5 从集合中移除原始文件MD5
func (r *Redis) RemoveRawFileMD5(ctx context.Context, md5 string) error {
	// 创建一个命名span
	ctx, span := redisTracer.Start(ctx, "Redis.RemoveRawFileMD5",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 添加属性
	span.SetAttributes(
		semconv.DBSystemRedis,
		attribute.String("db.redis.database", fmt.Sprintf("%d", r.config.DB)),
		attribute.String("net.peer.name", r.config.Address),
		attribute.String("db.operation", "SREM"),
		attribute.String("db.redis.key", constants.RawFileMD5SetKey),
		attribute.String("db.redis.member", md5),
	)

	key := r.FormatKey(constants.RawFileMD5SetKey)
	result, err := r.Client.SRem(ctx, key, md5).Result()

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("从集合中移除MD5失败: %w", err)
	}

	span.SetAttributes(attribute.Int64("removed_count", result))
	span.SetStatus(codes.Ok, "")

	return nil
}

// Get 获取键的值
func (r *Redis) Get(ctx context.Context, key string) (string, error) {
	// 检查客户端是否已初始化
	if r.Client == nil {
		return "", fmt.Errorf("redis客户端未初始化")
	}

	var span trace.Span

	// 根据key前缀决定是否创建span
	if shouldSampleRedisOp(key) {
		ctx, span = redisTracer.Start(ctx, "Redis.Get", trace.WithSpanKind(trace.SpanKindClient))
		defer span.End()

		span.SetAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "GET"),
			attribute.String("db.redis.key", key),
			// 设置标志位，表示不要在子span中传播，避免与redisotel hook产生的span重复
			attribute.Bool("otel.propagate_to_child", false),
		)
	}

	// 执行Get操作
	val, err := r.Client.Get(ctx, key).Result()

	// 如果span被创建，则记录结果
	if span != nil {
		if err != nil {
			// 对于key不存在的情况，不应该算作错误
			if err == redis.Nil {
				span.SetStatus(codes.Ok, "key not found")
				span.SetAttributes(attribute.Bool("db.redis.key_exists", false))
			} else {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			return "", err
		}

		span.SetAttributes(
			attribute.Bool("db.redis.key_exists", true),
			attribute.Int("db.redis.value_length", len(val)),
		)
		span.SetStatus(codes.Ok, "")
	}

	return val, nil
}

// Set 设置键的值
func (r *Redis) Set(ctx context.Context, key string, value string, expiration time.Duration) error {
	// 检查客户端是否已初始化
	if r.Client == nil {
		return fmt.Errorf("redis客户端未初始化")
	}

	var span trace.Span

	// 根据key前缀决定是否创建span
	if shouldSampleRedisOp(key) {
		ctx, span = redisTracer.Start(ctx, "Redis.Set", trace.WithSpanKind(trace.SpanKindClient))
		defer span.End()

		span.SetAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "SET"),
			attribute.String("db.redis.key", key),
			attribute.Int("db.redis.value_length", len(value)),
			// 设置标志位，表示不要在子span中传播，避免与redisotel hook产生的span重复
			attribute.Bool("otel.propagate_to_child", false),
		)

		if expiration > 0 {
			span.SetAttributes(attribute.Int64("db.redis.expiration_ms", expiration.Milliseconds()))
		}
	}

	// 执行Set操作
	err := r.Client.Set(ctx, key, value, expiration).Err()

	// 如果span被创建，则记录结果
	if span != nil {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
		span.SetStatus(codes.Ok, "")
	}

	return nil
}

// CacheSearchResults将排序好的"黄金结果集"缓存到Redis的ZSET中。
// searchID是该次搜索的唯一标识，results是排序后的简历列表，ttl是缓存过期时间。
func (r *Redis) CacheSearchResults(ctx context.Context, searchID string, results []types.RankedSubmission, ttl time.Duration) error {
	if r.Client == nil {
		return fmt.Errorf("redis client is not initialized")
	}

	key := r.FormatKey(constants.SearchResultCachePrefix, searchID)

	pipe := r.Client.Pipeline()

	// 准备要添加到ZSET的数据
	members := make([]redis.Z, len(results))
	for i, res := range results {
		members[i] = redis.Z{
			Score:  float64(res.Score),
			Member: res.SubmissionUUID,
		}
	}

	// 使用ZAdd命令一次性添加所有成员
	pipe.ZAdd(ctx, key, members...)
	// 设置过期时间
	pipe.Expire(ctx, key, ttl)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("caching search results failed: %w", err)
	}

	return nil
}

// GetCachedSearchResults从Redis的ZSET中分页获取缓存的搜索结果。
// 它返回UUID列表、总数和错误。
func (r *Redis) GetCachedSearchResults(ctx context.Context, searchID string, cursor, limit int64) (uuids []string, totalCount int64, err error) {
	if r.Client == nil {
		return nil, 0, fmt.Errorf("redis client is not initialized")
	}

	key := r.FormatKey(constants.SearchResultCachePrefix, searchID)

	pipe := r.Client.Pipeline()

	// 使用 ZREVRANGE 获取按分数从高到低排序的UUID
	resCmd := pipe.ZRevRange(ctx, key, cursor, cursor+limit-1)
	// 使用 ZCARD 获取总数
	countCmd := pipe.ZCard(ctx, key)

	_, err = pipe.Exec(ctx)
	if err != nil {
		// 如果key不存在，redis.Nil错误会在这里被捕获
		if err == redis.Nil {
			return nil, 0, nil // 缓存未命中，是正常情况，不返回错误
		}
		return nil, 0, fmt.Errorf("getting cached search results failed: %w", err)
	}

	uuids, err = resCmd.Result()
	if err != nil {
		// 再次检查 redis.Nil，尽管不太可能在Exec成功后发生
		if err == redis.Nil {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("failed to get uuids from cache: %w", err)
	}

	totalCount, err = countCmd.Result()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get total count from cache: %w", err)
	}

	return uuids, totalCount, nil
}

// AcquireLock 尝试获取分布式锁
// 返回值: 锁的值(成功时), 错误(如果有)
// 如果返回空字符串且无错误，表示锁已被其他进程持有
func (r *Redis) AcquireLock(ctx context.Context, lockKey string, expiration time.Duration) (string, error) {
	// 生成随机UUID作为锁的值，用于标识锁的持有者
	lockValue := fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Int())

	// 使用 SET NX 命令尝试获取锁
	// NX表示key不存在时才设置，PX设置过期时间（毫秒）
	ok, err := r.Client.SetNX(ctx, lockKey, lockValue, expiration).Result()
	if err != nil {
		return "", fmt.Errorf("获取锁失败: %w", err)
	}

	if !ok {
		// 锁已被其他进程获取
		return "", nil
	}

	// 成功获取锁，返回锁标识
	return lockValue, nil
}

// ReleaseLock 释放分布式锁
// 使用Lua脚本确保原子性：只释放由当前持有者持有的锁
func (r *Redis) ReleaseLock(ctx context.Context, lockKey string, lockValue string) (bool, error) {
	// Lua脚本：仅当锁存在且值匹配时才删除
	script := `
	if redis.call("GET", KEYS[1]) == ARGV[1] then
		return redis.call("DEL", KEYS[1])
	else
		return 0
	end`

	// 执行Lua脚本
	result, err := r.Client.Eval(ctx, script, []string{lockKey}, lockValue).Result()
	if err != nil {
		return false, fmt.Errorf("释放锁失败: %w", err)
	}

	// 1表示成功删除，0表示锁不存在或已被其他进程获取
	return result.(int64) == 1, nil
}
