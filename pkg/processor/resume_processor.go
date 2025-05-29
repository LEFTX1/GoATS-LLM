package processor

import (
	"context"
	"fmt"
	"time"
)

// StandardResumeProcessor 标准简历处理器
type StandardResumeProcessor struct {
	extractor PDFExtractor
	chunkers  map[string]ResumeChunker
	embedder  Embedder
	store     VectorStore
}

// ProcessorOption 处理器配置选项
type ProcessorOption func(*StandardResumeProcessor)

// WithPDFExtractor 设置PDF提取器
func WithPDFExtractor(extractor PDFExtractor) ProcessorOption {
	return func(p *StandardResumeProcessor) {
		p.extractor = extractor
	}
}

// WithChunker 添加分块器
func WithChunker(name string, chunker ResumeChunker) ProcessorOption {
	return func(p *StandardResumeProcessor) {
		if p.chunkers == nil {
			p.chunkers = make(map[string]ResumeChunker)
		}
		p.chunkers[name] = chunker
	}
}

// WithEmbedder 设置向量嵌入器
func WithEmbedder(embedder Embedder) ProcessorOption {
	return func(p *StandardResumeProcessor) {
		p.embedder = embedder
	}
}

// WithVectorStore 设置向量存储
func WithVectorStore(store VectorStore) ProcessorOption {
	return func(p *StandardResumeProcessor) {
		p.store = store
	}
}

// NewStandardResumeProcessor 创建新的简历处理器
func NewStandardResumeProcessor(options ...ProcessorOption) *StandardResumeProcessor {
	processor := &StandardResumeProcessor{
		chunkers: make(map[string]ResumeChunker),
	}

	for _, option := range options {
		option(processor)
	}

	return processor
}

// Process 实现Processor接口
func (p *StandardResumeProcessor) Process(ctx context.Context, options ProcessOptions) (*ProcessResult, error) {
	result := &ProcessResult{
		Stats: make(map[string]interface{}),
	}

	// 1. 提取PDF文本
	if p.extractor == nil {
		return nil, fmt.Errorf("未配置PDF提取器")
	}

	startTime := time.Now()
	text, metadata, err := p.extractor.ExtractFromFile(ctx, options.InputFile)
	if err != nil {
		return nil, fmt.Errorf("提取PDF文本失败: %w", err)
	}
	extractTime := time.Since(startTime)

	result.Text = text
	result.Metadata = metadata
	result.Stats["extract_time"] = extractTime.String()

	// 2. 分块文本
	var chunker ResumeChunker
	if options.UseLLM {
		chunker = p.chunkers["llm"]
		if chunker == nil {
			return nil, fmt.Errorf("请求使用LLM分块但未配置LLM分块器")
		}
	} else {
		chunker = p.chunkers["rule"]
		if chunker == nil {
			return nil, fmt.Errorf("未配置规则分块器")
		}
	}

	startTime = time.Now()
	sections, basicInfo, err := chunker.ChunkResume(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("分块文本失败: %w", err)
	}
	chunkTime := time.Since(startTime)

	result.Sections = sections
	result.BasicInfo = basicInfo
	result.Stats["chunk_time"] = chunkTime.String()

	// 3. 向量嵌入（如果配置了嵌入器）
	if p.embedder != nil {
		startTime = time.Now()
		vectors, err := p.embedder.Embed(ctx, sections, basicInfo)
		if err != nil {
			return nil, fmt.Errorf("向量嵌入失败: %w", err)
		}
		embedTime := time.Since(startTime)

		result.Vectors = vectors
		result.Stats["embed_time"] = embedTime.String()

		// 4. 存储向量（如果配置了存储）
		if p.store != nil && len(vectors) > 0 {
			startTime = time.Now()
			err = p.store.StoreVectors(ctx, vectors)
			if err != nil {
				return nil, fmt.Errorf("存储向量失败: %w", err)
			}
			storeTime := time.Since(startTime)
			result.Stats["store_time"] = storeTime.String()
		}
	}

	// 计算总处理时间
	totalTime := time.Duration(0)
	for _, stat := range result.Stats {
		if duration, ok := stat.(time.Duration); ok {
			totalTime += duration
		}
	}
	result.Stats["total_time"] = totalTime.String()

	return result, nil
}
