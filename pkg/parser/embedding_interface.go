/*
此文件实现了简历向量嵌入功能，允许将简历文本转换为向量表示。
使用自定义接口而非依赖eino-ext，便于集成任何向量模型。
*/

package parser

import (
	"context"
	"fmt"
)

// TextEmbedder 文本向量化接口
type TextEmbedder interface {
	// EmbedStrings 将文本转换为向量表示
	EmbedStrings(ctx context.Context, texts []string) ([][]float64, error)
}

// ResumeChunkVector 表示简历分块的向量表示
type ResumeChunkVector struct {
	// 源内容
	Section *ResumeSection

	// 向量表示
	Vector []float64

	// 元数据
	Metadata map[string]interface{}
}

// ResumeEmbedder 简历块嵌入器接口
type ResumeEmbedder interface {
	// EmbedResumeChunks 将简历分块转换为向量表示
	EmbedResumeChunks(ctx context.Context, sections []*ResumeSection, metadata map[string]string) ([]*ResumeChunkVector, error)
}

// StandardResumeEmbedder 使用TextEmbedder实现简历嵌入
type StandardResumeEmbedder struct {
	// 嵌入模型
	embedder TextEmbedder

	// 元数据处理选项
	includeContent   bool     // 是否在元数据中包含原始内容
	includeChunkType bool     // 是否在元数据中包含块类型
	includeBasicInfo bool     // 是否在元数据中包含基本信息
	additionalFields []string // 额外包含的元数据字段

	// 嵌入模型配置
	modelName string
	batchSize int
}

// EmbedderOption 嵌入器配置选项
type EmbedderOption func(*StandardResumeEmbedder)

// WithIncludeContent 设置是否在元数据中包含原始内容
func WithIncludeContent(include bool) EmbedderOption {
	return func(e *StandardResumeEmbedder) {
		e.includeContent = include
	}
}

// WithIncludeChunkType 设置是否在元数据中包含块类型
func WithIncludeChunkType(include bool) EmbedderOption {
	return func(e *StandardResumeEmbedder) {
		e.includeChunkType = include
	}
}

// WithIncludeBasicInfo 设置是否在元数据中包含基本信息
func WithIncludeBasicInfo(include bool) EmbedderOption {
	return func(e *StandardResumeEmbedder) {
		e.includeBasicInfo = include
	}
}

// WithAdditionalFields 设置额外包含的元数据字段
func WithAdditionalFields(fields []string) EmbedderOption {
	return func(e *StandardResumeEmbedder) {
		e.additionalFields = fields
	}
}

// WithModelName 设置嵌入模型名称
func WithModelName(model string) EmbedderOption {
	return func(e *StandardResumeEmbedder) {
		e.modelName = model
	}
}

// WithBatchSize 设置批处理大小
func WithBatchSize(size int) EmbedderOption {
	return func(e *StandardResumeEmbedder) {
		e.batchSize = size
	}
}

// NewStandardResumeEmbedder 创建新的标准简历嵌入器
func NewStandardResumeEmbedder(embedder TextEmbedder, options ...EmbedderOption) *StandardResumeEmbedder {
	embedderImpl := &StandardResumeEmbedder{
		embedder:         embedder,
		includeContent:   false, // 默认不包含原始内容（太大）
		includeChunkType: true,  // 默认包含块类型
		includeBasicInfo: true,  // 默认包含基本信息
		batchSize:        32,    // 默认批处理大小
	}

	// 应用选项
	for _, opt := range options {
		opt(embedderImpl)
	}

	return embedderImpl
}

// EmbedResumeChunks 将简历分块转换为向量表示
func (e *StandardResumeEmbedder) EmbedResumeChunks(
	ctx context.Context,
	sections []*ResumeSection,
	basicInfo map[string]string,
) ([]*ResumeChunkVector, error) {
	var chunks []*ResumeChunkVector
	var texts []string

	// 准备文本和临时存储分块信息
	for _, section := range sections {
		// 跳过空内容的部分
		if section.Content == "" {
			continue
		}

		// 添加到待嵌入文本列表
		texts = append(texts, section.Content)
	}

	// 批量嵌入
	if len(texts) == 0 {
		return nil, fmt.Errorf("没有有效的简历内容可以嵌入")
	}

	// 调用嵌入模型
	vectors, err := e.embedder.EmbedStrings(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("嵌入向量失败: %w", err)
	}

	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("嵌入向量数量(%d)与文本数量(%d)不匹配", len(vectors), len(texts))
	}

	// 组装结果
	sectionsWithContent := 0
	for _, section := range sections {
		if section.Content != "" {
			sectionsWithContent++
		}
	}

	vectorIndex := 0
	for _, section := range sections {
		// 跳过空内容的部分
		if section.Content == "" {
			continue
		}

		// 构建元数据
		metadata := make(map[string]interface{})

		// 添加块类型
		if e.includeChunkType {
			metadata["section_type"] = string(section.Type)
			metadata["section_title"] = section.Title
		}

		// 添加原始内容
		if e.includeContent {
			metadata["content"] = section.Content
		}

		// 添加基本信息
		if e.includeBasicInfo && basicInfo != nil {
			for k, v := range basicInfo {
				metadata["basic_"+k] = v
			}
		}

		// 添加额外字段
		for _, field := range e.additionalFields {
			if value, exists := basicInfo[field]; exists {
				metadata[field] = value
			}
		}

		// 创建向量表示
		if vectorIndex >= len(vectors) {
			// 这种情况不应该发生，但为了健壮性添加检查
			continue
		}

		chunk := &ResumeChunkVector{
			Section:  section,
			Vector:   vectors[vectorIndex],
			Metadata: metadata,
		}

		chunks = append(chunks, chunk)
		vectorIndex++
	}

	return chunks, nil
}
