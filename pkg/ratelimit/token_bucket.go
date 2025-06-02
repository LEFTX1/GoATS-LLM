package ratelimit

import (
	"context"
	"strings"
	"sync"
	"time"
)

// TokenBucket 实现令牌桶算法的限流器
type TokenBucket struct {
	rate           float64       // 每秒生成的令牌数
	capacity       float64       // 桶的容量
	tokens         float64       // 当前令牌数
	lastRefillTime time.Time     // 上次填充令牌的时间
	mutex          sync.Mutex    // 互斥锁，保证并发安全
	retryWaitTime  time.Duration // 重试等待时间
	maxRetries     int           // 最大重试次数
}

// NewTokenBucket 创建一个新的令牌桶限流器
func NewTokenBucket(qpm int, capacity int) *TokenBucket {
	// 如果未指定容量，设置为QPM的一半
	if capacity <= 0 {
		capacity = qpm / 2
		if capacity <= 0 {
			capacity = 1
		}
	}

	return &TokenBucket{
		rate:           float64(qpm) / 60.0, // 转换为每秒速率
		capacity:       float64(capacity),
		tokens:         float64(capacity), // 初始填满
		lastRefillTime: time.Now(),
		retryWaitTime:  1 * time.Second, // 默认重试等待1秒
		maxRetries:     3,               // 默认最大重试3次
	}
}

// WithRetryPolicy 设置重试策略
func (tb *TokenBucket) WithRetryPolicy(waitTime time.Duration, maxRetries int) *TokenBucket {
	tb.retryWaitTime = waitTime
	tb.maxRetries = maxRetries
	return tb
}

// refill 根据经过的时间填充令牌
func (tb *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastRefillTime).Seconds()
	tb.lastRefillTime = now

	// 计算新增令牌数
	newTokens := elapsed * tb.rate

	// 更新当前令牌数，不超过容量
	if tb.tokens+newTokens > tb.capacity {
		tb.tokens = tb.capacity
	} else {
		tb.tokens += newTokens
	}
}

// Allow 判断是否允许通过一个请求，消耗一个令牌
func (tb *TokenBucket) Allow() bool {
	tb.mutex.Lock()
	defer tb.mutex.Unlock()

	tb.refill()
	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		return true
	}
	return false
}

// Wait 等待直到有令牌可用
func (tb *TokenBucket) Wait(ctx context.Context) error {
	for {
		tb.mutex.Lock()
		tb.refill()

		if tb.tokens >= 1.0 {
			tb.tokens -= 1.0
			tb.mutex.Unlock()
			return nil
		}

		// 计算需要等待的时间
		waitTime := time.Duration((1.0 - tb.tokens) / tb.rate * float64(time.Second))
		tb.mutex.Unlock()

		// 使用定时器或上下文等待
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitTime):
			// 继续尝试获取令牌
		}
	}
}

// RetryWithBackoff 使用退避策略执行函数并在需要时重试
func (tb *TokenBucket) RetryWithBackoff(ctx context.Context, fn func() error) error {
	var err error

	for retry := 0; retry <= tb.maxRetries; retry++ {
		// 等待获取令牌
		if err = tb.Wait(ctx); err != nil {
			return err
		}

		// 执行函数
		err = fn()
		if err == nil {
			return nil // 成功执行
		}

		// 判断是否需要重试
		if !isRetryableError(err) || retry >= tb.maxRetries {
			return err
		}

		// 计算退避时间
		backoffTime := tb.retryWaitTime * time.Duration(1<<uint(retry))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoffTime):
			// 继续重试
		}
	}

	return err
}

// isRetryableError 判断错误是否可重试
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// 根据错误消息判断是否可重试
	errStr := err.Error()
	return contains(errStr, []string{
		"timeout",
		"deadline exceeded",
		"connection reset",
		"EOF",
		"connection refused",
		"429 Too Many Requests",
		"rate limit",
		"no such host",
		"服务器繁忙",
		"请求超过限额",
		"QPS限制",
	})
}

// contains 检查字符串是否包含列表中的任何一个子串
func contains(s string, substrs []string) bool {
	for _, substr := range substrs {
		if substr != "" && strings.Contains(s, substr) {
			return true
		}
	}
	return false
}
