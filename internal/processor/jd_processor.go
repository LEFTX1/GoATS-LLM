package processor

import (
	"context"
	"fmt"
	"log"
	"os"
)

// JDProcessor 负责处理与岗位描述 (JD) 相关的任务，主要是文本向量化。
type JDProcessor struct {
	embedder TextEmbedder // 文本向量化接口实例
	logger   *log.Logger
}

// NewJDProcessor 创建一个新的 JDProcessor 实例。
// 它接受一个 TextEmbedder 实现和可选的 JDOption 配置。
func NewJDProcessor(embedder TextEmbedder, options ...JDOption) (*JDProcessor, error) {
	if embedder == nil {
		return nil, fmt.Errorf("TextEmbedder 不能为空")
	}

	p := &JDProcessor{
		embedder: embedder,
		logger:   log.New(os.Stdout, "[JDProcessor] ", log.LstdFlags|log.Lshortfile),
	}

	for _, option := range options {
		option(p)
	}

	// 确保 embedder 的维度与系统期望一致 (如果需要)
	// 例如: if p.embedder.GetDimensions() != expectedDimension { ... }
	// 这里暂时不加维度检查逻辑，依赖调用方传入配置正确的embedder

	p.logger.Printf("JDProcessor 初始化完成，使用 Embedder: %T", embedder)
	return p, nil
}

// GetJobDescriptionVector 将 JD 文本转换为查询向量。
// ctx: 上下文。
// jdText: 岗位描述的纯文本内容。
// 返回生成的向量和可能的错误。
func (p *JDProcessor) GetJobDescriptionVector(ctx context.Context, jdText string) ([]float64, error) {
	if jdText == "" {
		return nil, fmt.Errorf("JD 文本不能为空")
	}

	p.logger.Printf("开始为JD文本生成向量...")

	// 调用 embedder 进行向量化
	// 注意：Eino 的 EmbedStrings 接受 []string，所以我们将单个 JD 文本放入切片中
	vectors, err := p.embedder.EmbedStrings(ctx, []string{jdText})
	if err != nil {
		p.logger.Printf("JD文本向量化失败: %v", err)
		return nil, fmt.Errorf("JD 文本向量化失败: %w", err)
	}

	if len(vectors) == 0 || len(vectors[0]) == 0 {
		p.logger.Printf("JD文本向量化结果为空")
		return nil, fmt.Errorf("JD 文本向量化结果为空")
	}

	p.logger.Printf("JD文本向量生成成功，向量维度: %d", len(vectors[0]))
	return vectors[0], nil
}
