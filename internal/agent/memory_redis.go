package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time" // For potential TTLs, though not in initial spec

	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

// RedisChatMemory 实现了 ChatMemory 接口，使用 Redis 作为持久化存储。
type RedisChatMemory struct {
	redisClient *redis.Client
	keyPrefix   string        // 例如 "chatmemory:"，以避免键冲突
	ttl         time.Duration // 可选：为聊天记录设置过期时间
}

// NewRedisChatMemory 创建一个新的 RedisChatMemory 实例。
// redisClient: 一个已连接和配置好的 go-redis 客户端实例。
// keyPrefix: 用于所有 Redis 键的前缀，例如 "chatmemory:"。
// ttl: 聊天记录在 Redis 中的可选过期时间。如果为0，则不过期。
func NewRedisChatMemory(redisClient *redis.Client, keyPrefix string, ttl time.Duration) (*RedisChatMemory, error) {
	if redisClient == nil {
		return nil, fmt.Errorf("redis client cannot be nil")
	}
	if keyPrefix == "" {
		// Provide a default prefix if none is given, or return an error
		// For now, let's default it.
		keyPrefix = "chatmemory:"
		// return nil, fmt.Errorf("key prefix cannot be empty")
	}

	// Ping Redis to ensure connectivity (optional, but good practice)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping redis: %w", err)
	}

	return &RedisChatMemory{
		redisClient: redisClient,
		keyPrefix:   keyPrefix,
		ttl:         ttl,
	}, nil
}

// buildKey 为给定的 sessionId 构建 Redis 键。
func (rcm *RedisChatMemory) buildKey(sessionId string) string {
	return rcm.keyPrefix + sessionId
}

// GetHistory 实现 ChatMemory 接口
func (rcm *RedisChatMemory) GetHistory(sessionId string) ([]*schema.Message, error) {
	key := rcm.buildKey(sessionId)
	ctx := context.Background() // Or use a context passed from the caller

	// 获取 List 中的所有元素 (JSON 字符串)
	serializedMessages, err := rcm.redisClient.LRange(ctx, key, 0, -1).Result()
	if errors.Is(err, redis.Nil) {
		return []*schema.Message{}, nil // Key 不存在，返回空历史
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get messages from redis for session %s: %w", sessionId, err)
	}

	messages := make([]*schema.Message, 0, len(serializedMessages))
	for _, sm := range serializedMessages {
		var msg schema.Message
		if err := json.Unmarshal([]byte(sm), &msg); err != nil {
			// Consider how to handle unmarshalling errors: skip, error out, log?
			// For now, let's return an error if any message fails to unmarshal.
			return nil, fmt.Errorf("failed to unmarshal message for session %s: %w. Corrupted data: %s", sessionId, err, sm)
		}
		messages = append(messages, &msg)
	}
	return messages, nil
}

// AddMessage 实现 ChatMemory 接口
func (rcm *RedisChatMemory) AddMessage(sessionId string, message *schema.Message) error {
	if message == nil {
		return fmt.Errorf("cannot add nil message to chat history for session %s", sessionId)
	}
	key := rcm.buildKey(sessionId)
	ctx := context.Background()

	serializedMessage, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message for session %s: %w", sessionId, err)
	}

	// 使用事务或 Pipeline 来确保原子性（特别是当有 TTL 时）
	pipe := rcm.redisClient.TxPipeline()
	pipe.RPush(ctx, key, serializedMessage)
	if rcm.ttl > 0 {
		pipe.Expire(ctx, key, rcm.ttl)
	}
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to add message to redis for session %s: %w", sessionId, err)
	}
	return nil
}

// AddMessages 实现 ChatMemory 接口
func (rcm *RedisChatMemory) AddMessages(sessionId string, messages []*schema.Message) error {
	if len(messages) == 0 {
		return nil // 没有消息要添加
	}
	key := rcm.buildKey(sessionId)
	ctx := context.Background()

	pipe := rcm.redisClient.TxPipeline()
	for _, message := range messages {
		if message == nil {
			// Rollback or simply skip? For now, let's error out if any message is nil.
			// If Exec() is called on a pipeline with errors, it won't commit.
			// However, individual command errors might not stop others unless using a transaction.
			// For simplicity, ensure messages are valid before starting pipeline.
			return fmt.Errorf("cannot add nil message in a batch to chat history for session %s", sessionId)
		}
		serializedMessage, err := json.Marshal(message)
		if err != nil {
			return fmt.Errorf("failed to marshal message in batch for session %s: %w", sessionId, err)
		}
		pipe.RPush(ctx, key, serializedMessage)
	}

	if rcm.ttl > 0 {
		pipe.Expire(ctx, key, rcm.ttl)
	}

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to add messages in batch to redis for session %s: %w", sessionId, err)
	}
	return nil
}

// ClearHistory 实现 ChatMemory 接口
func (rcm *RedisChatMemory) ClearHistory(sessionId string) error {
	key := rcm.buildKey(sessionId)
	ctx := context.Background()

	if err := rcm.redisClient.Del(ctx, key).Err(); err != nil {
		// If key does not exist, Del returns 0, err is nil. So, no need to check for redis.Nil.
		return fmt.Errorf("failed to clear chat history from redis for session %s: %w", sessionId, err)
	}
	return nil
}
