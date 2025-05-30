package processor

import (
	"context"

	"ai-agent-go/pkg/parser"
)

// ProcessOptions 处理选项
type ProcessOptions struct {
	// 输入文件路径
	InputFile string

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
	Sections []*parser.ResumeSection

	// 基本信息
	BasicInfo map[string]string

	// 向量表示
	Vectors []*parser.ResumeChunkVector

	// 处理用时
	Stats map[string]interface{}
}

// JobMatchEvaluation 岗位匹配评估结果
type JobMatchEvaluation struct {
	// 匹配分数 (0-100)
	MatchScore int `json:"match_score"`

	// 匹配亮点
	MatchHighlights []string `json:"match_highlights"`

	// 潜在不足
	PotentialGaps []string `json:"potential_gaps"`

	// 针对岗位的简历摘要
	ResumeSummaryForJD string `json:"resume_summary_for_jd"`

	// 评估时间
	EvaluatedAt int64 `json:"evaluated_at"`

	// 评估ID
	EvaluationID string `json:"evaluation_id,omitempty"`
}

// PDFExtractor PDF提取器接口
type PDFExtractor interface {
	// 从PDF文件提取文本
	ExtractFromFile(ctx context.Context, filePath string) (string, map[string]interface{}, error)
}

// ResumeChunker 简历分块器接口
type ResumeChunker interface {
	// 将简历文本分块
	ChunkResume(ctx context.Context, text string) ([]*parser.ResumeSection, map[string]string, error)
}

// JobMatchEvaluator 岗位匹配评估器接口
type JobMatchEvaluator interface {
	// 评估简历与岗位的匹配度
	EvaluateMatch(ctx context.Context, jobDescription string, resumeText string) (*JobMatchEvaluation, error)
}

// Embedder 向量嵌入器接口
type Embedder interface {
	// 将简历分块转换为向量表示
	Embed(ctx context.Context, sections []*parser.ResumeSection, basicInfo map[string]string) ([]*parser.ResumeChunkVector, error)
}

// VectorStore 向量存储接口
type VectorStore interface {
	// 存储向量
	StoreVectors(ctx context.Context, vectors []*parser.ResumeChunkVector) error
}

// Processor 处理器接口，整合所有步骤
type Processor interface {
	// 处理简历
	Process(ctx context.Context, options ProcessOptions) (*ProcessResult, error)
}
