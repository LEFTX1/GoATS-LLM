package processor

import (
	"ai-agent-go/internal/storage"
	"context"
	"fmt"
	"log"
	"os"
)

// JDProcessor 负责处理与岗位描述 (JD) 相关的任务，主要是文本向量化。
type JDProcessor struct {
	embedder       TextEmbedder // 文本向量化接口实例
	storage        *storage.Storage
	embeddingModel string
	logger         *log.Logger
}

// NewJDProcessor 创建一个新的 JDProcessor 实例。
// 它接受一个 TextEmbedder 实现和可选的 JDOption 配置。
func NewJDProcessor(embedder TextEmbedder, storage *storage.Storage, embeddingModel string, options ...JDOption) (*JDProcessor, error) {
	if embedder == nil {
		return nil, fmt.Errorf("TextEmbedder 不能为空")
	}
	if storage == nil {
		return nil, fmt.Errorf("Storage 不能为空")
	}
	if embeddingModel == "" {
		return nil, fmt.Errorf("embeddingModel 不能为空")
	}

	p := &JDProcessor{
		embedder:       embedder,
		storage:        storage,
		embeddingModel: embeddingModel,
		logger:         log.New(os.Stdout, "[JDProcessor] ", log.LstdFlags|log.Lshortfile),
	}

	for _, option := range options {
		option(p)
	}

	// 确保 embedder 的维度与系统期望一致 (如果需要)
	// 例如: if p.embedder.GetDimensions() != expectedDimension { ... }
	// 这里暂时不加维度检查逻辑，依赖调用方传入配置正确的embedder

	p.logger.Printf("JDProcessor 初始化完成，使用 Embedder: %T, Model: %s", embedder, embeddingModel)
	return p, nil
}

// GetJobDescriptionVector 将 JD 文本转换为查询向量。
// 它会先尝试从 Redis 缓存获取，如果未命中，则进行计算并存入缓存。
// ctx: 上下文。
// jobID: 岗位ID，用于缓存键。
// jdText: 岗位描述的纯文本内容。
// 返回生成的向量和可能的错误。
func (p *JDProcessor) GetJobDescriptionVector(ctx context.Context, jobID string, jdText string) ([]float64, error) {
	if jobID == "" {
		return nil, fmt.Errorf("jobID 不能为空")
	}
	if jdText == "" {
		return nil, fmt.Errorf("JD 文本不能为空")
	}

	// 1. 尝试从 Redis 缓存获取
	cachedVector, modelVersion, err := p.storage.Redis.GetJobVector(ctx, jobID)
	if err == nil && len(cachedVector) > 0 {
		// 检查模型版本是否匹配
		if modelVersion == p.embeddingModel {
			p.logger.Printf("从 Redis 缓存命中 JD 向量 for JobID: %s, Model: %s", jobID, modelVersion)
			return cachedVector, nil
		}
		p.logger.Printf("Redis 缓存中的 JD 向量模型版本不匹配 (缓存: %s, 当前: %s)，将重新生成", modelVersion, p.embeddingModel)
	}
	if err != nil {
		// Redis `GET` 命令本身出错，记录日志但继续执行，因为向量生成是核心路径
		p.logger.Printf("从 Redis 获取 JD 向量失败 for JobID: %s, Error: %v. 将继续生成新向量", jobID, err)
	}

	p.logger.Printf("开始为 JobID: %s 的 JD 文本生成新向量...", jobID)

	// 2. 缓存未命中，调用 embedder 进行向量化
	vectors, err := p.embedder.EmbedStrings(ctx, []string{jdText})
	if err != nil {
		p.logger.Printf("JD 文本向量化失败 for JobID: %s: %v", jobID, err)
		return nil, fmt.Errorf("JD 文本向量化失败: %w", err)
	}

	if len(vectors) == 0 || len(vectors[0]) == 0 {
		p.logger.Printf("JD 文本向量化结果为空 for JobID: %s", jobID)
		return nil, fmt.Errorf("JD 文本向量化结果为空")
	}

	newVector := vectors[0]
	p.logger.Printf("JD 文本向量生成成功 for JobID: %s，向量维度: %d", jobID, len(newVector))

	// 3. 将新生成的向量存入 Redis 缓存
	err = p.storage.Redis.SetJobVector(ctx, jobID, newVector, p.embeddingModel)
	if err != nil {
		// 缓存失败不应阻塞主流程，但需要记录日志
		p.logger.Printf("将 JD 向量存入 Redis 失败 for JobID: %s: %v", jobID, err)
	} else {
		p.logger.Printf("成功将新生成的 JD 向量存入 Redis for JobID: %s", jobID)
	}

	return newVector, nil
}
