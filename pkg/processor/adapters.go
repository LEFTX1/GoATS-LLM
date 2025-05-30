package processor

import (
	"context"
	"time"

	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/parser"
	"ai-agent-go/pkg/storage"
	"ai-agent-go/pkg/storage/singleton"
	"ai-agent-go/pkg/types"
)

// EinoPDFAdapter 适配Eino PDF提取器到PDFExtractor接口
type EinoPDFAdapter struct {
	extractor *parser.EinoPDFTextExtractor
}

// NewEinoPDFAdapter 创建新的Eino PDF适配器
func NewEinoPDFAdapter(ctx context.Context) (*EinoPDFAdapter, error) {
	extractor, err := parser.NewEinoPDFTextExtractor(ctx)
	if err != nil {
		return nil, err
	}
	return &EinoPDFAdapter{extractor: extractor}, nil
}

// ExtractFromFile 实现PDFExtractor接口
func (a *EinoPDFAdapter) ExtractFromFile(ctx context.Context, filePath string) (string, map[string]interface{}, error) {
	return a.extractor.ExtractFullTextFromPDFFile(ctx, filePath)
}

// LLMChunkerAdapter 适配LLM分块器到ResumeChunker接口
type LLMChunkerAdapter struct {
	chunker *parser.LLMResumeChunker
}

// NewLLMChunkerAdapter 创建新的LLM分块器适配器
func NewLLMChunkerAdapter(chunker *parser.LLMResumeChunker) *LLMChunkerAdapter {
	return &LLMChunkerAdapter{chunker: chunker}
}

// ChunkResume 实现ResumeChunker接口
func (a *LLMChunkerAdapter) ChunkResume(ctx context.Context, text string) ([]*parser.ResumeSection, map[string]string, error) {
	return a.chunker.ChunkResume(ctx, text)
}

// RuleChunkerAdapter 适配规则分块器到ResumeChunker接口
type RuleChunkerAdapter struct {
	chunker *parser.ResumeChunker
}

// NewRuleChunkerAdapter 创建新的规则分块器适配器
func NewRuleChunkerAdapter(config parser.ChunkerConfig) (*RuleChunkerAdapter, error) {
	chunker, err := parser.NewResumeChunker(config)
	if err != nil {
		return nil, err
	}
	return &RuleChunkerAdapter{chunker: chunker}, nil
}

// ChunkResume 实现ResumeChunker接口
func (a *RuleChunkerAdapter) ChunkResume(ctx context.Context, text string) ([]*parser.ResumeSection, map[string]string, error) {
	sections, err := a.chunker.ChunkResume(ctx, text)
	if err != nil {
		return nil, nil, err
	}

	// 规则分块器不提供基本信息，需要手动提取
	basicInfo := make(map[string]string)
	for _, section := range sections {
		if section.Type == parser.SectionBasicInfo {
			// 简单处理：将基本信息章节的内容作为一个整体
			basicInfo["basic_info"] = section.Content
			break
		}
	}

	return sections, basicInfo, nil
}

// EmbedderAdapter 适配向量嵌入器到Embedder接口
type EmbedderAdapter struct {
	embedder *parser.StandardResumeEmbedder
}

// NewEmbedderAdapter 创建新的向量嵌入器适配器
func NewEmbedderAdapter(embedder *parser.StandardResumeEmbedder) *EmbedderAdapter {
	return &EmbedderAdapter{embedder: embedder}
}

// Embed 实现Embedder接口
func (a *EmbedderAdapter) Embed(ctx context.Context, sections []*parser.ResumeSection, basicInfo map[string]string) ([]*parser.ResumeChunkVector, error) {
	return a.embedder.EmbedResumeChunks(ctx, sections, basicInfo)
}

// VectorStoreAdapter 适配向量存储到VectorStore接口
type VectorStoreAdapter struct {
	store *storage.ResumeVectorStore
}

// NewVectorStoreAdapter 创建新的向量存储适配器
func NewVectorStoreAdapter(cfg *config.Config) (*VectorStoreAdapter, error) {
	store, err := singleton.GetResumeVectorStore(cfg)
	if err != nil {
		return nil, err
	}
	return &VectorStoreAdapter{store: store}, nil
}

// StoreVectors 实现VectorStore接口
func (a *VectorStoreAdapter) StoreVectors(ctx context.Context, vectors []*parser.ResumeChunkVector) error {
	if len(vectors) == 0 {
		return nil
	}

	// 提取resumeID
	resumeID := ""
	if id, ok := vectors[0].Metadata["resume_id"].(string); ok {
		resumeID = id
	} else {
		// 如果没有resume_id，生成一个临时ID，使用section title或其他信息
		if vectors[0].Section != nil && vectors[0].Section.Title != "" {
			resumeID = "resume_" + vectors[0].Section.Title
		} else {
			resumeID = "resume_" + time.Now().Format("20060102150405")
		}
	}

	// 转换数据结构
	chunks := make([]types.ResumeChunk, len(vectors))
	embeddings := make([][]float32, len(vectors))

	for i, vector := range vectors {
		// 转换为float32向量
		float32Vector := make([]float32, len(vector.Vector))
		for j, val := range vector.Vector {
			float32Vector[j] = float32(val)
		}
		embeddings[i] = float32Vector

		// 创建Chunk
		chunkType := "UNKNOWN"
		content := ""
		if vector.Section != nil {
			chunkType = string(vector.Section.Type)
			content = vector.Section.Content
		}

		chunks[i] = types.ResumeChunk{
			ChunkID:         i,
			Content:         content,
			ChunkType:       chunkType,
			ImportanceScore: 1, // 默认整数值
			Metadata:        types.ChunkMetadata{},
		}

		// 填充元数据
		if skills, ok := vector.Metadata["skills"].([]string); ok {
			chunks[i].Metadata.Skills = skills
		}
		if exp, ok := vector.Metadata["experience_years"].(float64); ok {
			// 转换为整数
			chunks[i].Metadata.ExperienceYears = int(exp)
		}
		if edu, ok := vector.Metadata["education_level"].(string); ok {
			chunks[i].Metadata.EducationLevel = edu
		}
	}

	// 存储到向量数据库
	_, err := a.store.StoreResumeVectors(ctx, resumeID, chunks, embeddings)
	return err
}
