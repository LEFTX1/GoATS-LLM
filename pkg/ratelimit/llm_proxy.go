package ratelimit

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// RateLimitedLLMModel 对LLM模型的调用进行限流的代理
type RateLimitedLLMModel struct {
	original    model.ToolCallingChatModel
	rateLimiter *TokenBucket
}

// NewRateLimitedLLMModel 创建一个新的限流LLM模型代理
func NewRateLimitedLLMModel(original model.ToolCallingChatModel, qpm int) *RateLimitedLLMModel {
	return &RateLimitedLLMModel{
		original:    original,
		rateLimiter: NewTokenBucket(qpm, qpm/2), // 容量设为QPM的一半，允许一定的突发流量
	}
}

// WithRetryPolicy 设置重试策略
func (rl *RateLimitedLLMModel) WithRetryPolicy(waitTime time.Duration, maxRetries int) *RateLimitedLLMModel {
	rl.rateLimiter.WithRetryPolicy(waitTime, maxRetries)
	return rl
}

// Generate 代理Generate方法，增加限流和重试逻辑
func (rl *RateLimitedLLMModel) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	var response *schema.Message
	var err error

	err = rl.rateLimiter.RetryWithBackoff(ctx, func() error {
		var genErr error
		response, genErr = rl.original.Generate(ctx, messages, options...)
		return genErr
	})

	return response, err
}

// Stream 代理Stream方法，增加限流和重试逻辑
func (rl *RateLimitedLLMModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	var stream *schema.StreamReader[*schema.Message]
	var err error

	err = rl.rateLimiter.RetryWithBackoff(ctx, func() error {
		var streamErr error
		stream, streamErr = rl.original.Stream(ctx, messages, options...)
		return streamErr
	})

	return stream, err
}

// WithTools 代理WithTools方法
func (rl *RateLimitedLLMModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	newModel, err := rl.original.WithTools(tools)
	if err != nil {
		return nil, err
	}

	// 创建一个新的限流代理，保留原有的限流设置
	return &RateLimitedLLMModel{
		original:    newModel,
		rateLimiter: rl.rateLimiter,
	}, nil
}

// NewLLMWithRateLimit 直接从配置和原始LLM模型创建带限流的LLM模型
// 此方法融合了工厂方法的逻辑，允许直接在源文件中使用构造函数方式创建
func NewLLMWithRateLimit(original model.ToolCallingChatModel, modelName string, cfg map[string]int, customQPM int, maxRetries int, retryWaitTime time.Duration) model.ToolCallingChatModel {
	// 确定最终的QPM值
	qpm := customQPM // 默认使用自定义QPM

	// 如果配置映射不为空，尝试从中获取模型特定的QPM
	if cfg != nil && modelName != "" {
		if modelQPM, ok := cfg[modelName]; ok && modelQPM > 0 {
			// 找到了模型对应的QPM限制，使用该限制值的90%作为安全值
			safeQPM := int(float64(modelQPM) * 0.9)
			qpm = safeQPM
		}
	}

	// 如果QPM仍为0，设置默认值
	if qpm <= 0 {
		qpm = 30 // 默认QPM
	}

	// 设置默认的重试参数（如果未指定）
	if maxRetries <= 0 {
		maxRetries = 3
	}

	// 创建限流模型并设置重试策略
	limitedModel := NewRateLimitedLLMModel(original, qpm)
	limitedModel.WithRetryPolicy(retryWaitTime, maxRetries)

	return limitedModel
}
