package processor

import (
	"ai-agent-go/internal/types"
	"context"
	"io"

	"github.com/cloudwego/eino/components/embedding"
)

// ProcessorConfig 组件配置选项
type ProcessorConfig struct {
	// 是否使用LLM进行分块
	UseLLM bool

	// 是否保留格式
	PreserveFormat bool

	// 向量维度
	Dimensions int

	// 额外的处理选项
	ExtraOptions map[string]interface{}
}

// ProcessResult 处理结果
type ProcessResult struct {
	// 提取的文本
	Text string

	// 提取的元数据
	Metadata map[string]interface{}

	// 分块结果
	Sections []*types.ResumeSection

	// 基本信息
	BasicInfo map[string]string

	// 向量表示
	Vectors []*types.ResumeChunkVector

	// 岗位匹配评估结果
	Evaluation *types.JobMatchEvaluation
}

//
// PDF解析相关接口
//

// PDFExtractor PDF提取器接口
type PDFExtractor interface {
	// ExtractFromFile 从PDF文件提取文本和元数据
	ExtractFromFile(ctx context.Context, filePath string) (string, map[string]interface{}, error)

	// ExtractTextFromReader 从io.Reader提取文本和元数据
	// 参数：
	// - ctx: 上下文
	// - reader: PDF文件内容的读取器
	// - uri: 资源标识符（可选，用于日志或元数据）
	// - options: 可选的解析配置
	// 返回：
	// - 提取的文本
	// - 附加的元数据（如页数、版本等）
	// - 错误信息
	ExtractTextFromReader(ctx context.Context, reader io.Reader, uri string, options interface{}) (string, map[string]interface{}, error)

	// ExtractTextFromBytes 从字节数组提取文本和元数据
	ExtractTextFromBytes(ctx context.Context, data []byte, uri string, options interface{}) (string, map[string]interface{}, error)

	// ExtractStructuredContent 提取结构化内容（如段落、表格等）
	// 返回值包含了结构化的内容信息
	ExtractStructuredContent(ctx context.Context, reader io.Reader, uri string, options interface{}) (map[string]interface{}, error)
}

//
// 简历分块相关接口
//

// ResumeChunker 简历分块器接口
type ResumeChunker interface {
	// ChunkResume 将简历文本分块，并提取基本信息
	ChunkResume(ctx context.Context, text string) ([]*types.ResumeSection, map[string]string, error)
}

// LLMJobMatchEvaluation 定义LLM评估结果的结构体
type LLMJobMatchEvaluation struct {
	MatchScore         int      `json:"match_score"`
	MatchHighlights    []string `json:"match_highlights"`
	PotentialGaps      []string `json:"potential_gaps"`
	ResumeSummaryForJD string   `json:"resume_summary_for_jd"`
}

//
// 向量嵌入相关接口
//

// TextEmbedder 文本向量化接口 (符合 cloudwego/eino 规范)
type TextEmbedder interface {
	// EmbedStrings 将文本转换为向量表示
	EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error)

	// GetDimensions 返回嵌入向量的维度
	GetDimensions() int
}

// ResumeEmbedder 简历嵌入器接口
type ResumeEmbedder interface {
	// EmbedResumeChunks 将简历分块转换为向量表示
	EmbedResumeChunks(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error)

	// Embed 实现处理器嵌入方法
	Embed(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error)
}

//
// 岗位匹配评估相关接口
//

// JobMatchEvaluator 岗位匹配评估器接口
type JobMatchEvaluator interface {
	// EvaluateMatch 评估简历与岗位的匹配度
	EvaluateMatch(ctx context.Context, jobDescription string, resumeText string) (*types.JobMatchEvaluation, error)
}

// EmbeddingGenerator 嵌入向量生成器接口
type EmbeddingGenerator interface {
	// CreateEmbeddings 为一批文本生成嵌入向量
	CreateEmbeddings(ctx context.Context, texts []string) ([][]float32, error)
}

//
// 存储相关接口
//

// VectorStore 向量存储接口
type VectorStore interface {
	// StoreVectors 存储向量
	StoreVectors(ctx context.Context, vectors []*types.ResumeChunkVector) error
}

// FileOperator 文件操作接口
type FileOperator interface {
	// ReadFile 从文件系统读取文件
	ReadFile(ctx context.Context, filePath string) ([]byte, error)

	// WriteFile 将内容写入文件系统
	WriteFile(ctx context.Context, filePath string, content []byte) error
}

// CacheManager 缓存管理接口
type CacheManager interface {
	// GetCachedResult 获取缓存的处理结果
	GetCachedResult(ctx context.Context, key string) (*ProcessResult, error)

	// CacheResult 缓存处理结果
	CacheResult(ctx context.Context, key string, result *ProcessResult) error
}
