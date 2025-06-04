package processor

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/types"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockPDFExtractor 模拟PDF提取器
type MockPDFExtractor struct {
	text     string
	metadata map[string]interface{}
	err      error
}

func (m *MockPDFExtractor) ExtractFromFile(ctx context.Context, filePath string) (string, map[string]interface{}, error) {
	return m.text, m.metadata, m.err
}

func (m *MockPDFExtractor) ExtractTextFromReader(ctx context.Context, reader io.Reader, uri string, options interface{}) (string, map[string]interface{}, error) {
	return m.text, m.metadata, m.err
}

func (m *MockPDFExtractor) ExtractTextFromBytes(ctx context.Context, data []byte, uri string, options interface{}) (string, map[string]interface{}, error) {
	return m.text, m.metadata, m.err
}

func (m *MockPDFExtractor) ExtractStructuredContent(ctx context.Context, reader io.Reader, uri string, options interface{}) (map[string]interface{}, error) {
	return m.metadata, m.err
}

// MockResumeChunker 模拟简历分块器
type MockResumeChunker struct {
	sections  []*types.ResumeSection
	basicInfo map[string]string
	err       error
}

func (m *MockResumeChunker) ChunkResume(ctx context.Context, text string) ([]*types.ResumeSection, map[string]string, error) {
	return m.sections, m.basicInfo, m.err
}

// MockResumeEmbedder 模拟简历嵌入器
type MockResumeEmbedder struct {
	vectors []*types.ResumeChunkVector
	err     error
}

func (m *MockResumeEmbedder) EmbedResumeChunks(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error) {
	return m.vectors, m.err
}

func (m *MockResumeEmbedder) Embed(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error) {
	return m.vectors, m.err
}

// MockJobMatchEvaluator 模拟岗位匹配评估器
type MockJobMatchEvaluator struct {
	evaluation *types.JobMatchEvaluation
	err        error
}

func (m *MockJobMatchEvaluator) EvaluateMatch(ctx context.Context, jobDescription string, resumeText string) (*types.JobMatchEvaluation, error) {
	return m.evaluation, m.err
}

// MockVectorStore 模拟向量存储
type MockVectorStore struct {
	err error
}

func (m *MockVectorStore) StoreVectors(ctx context.Context, vectors []*types.ResumeChunkVector) error {
	return m.err
}

// TestResumeProcessor 测试简历处理器的组件聚合能力
func TestResumeProcessor(t *testing.T) {
	// 创建上下文
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 创建模拟组件
	mockExtractor := &MockPDFExtractor{
		text: "这是一个测试简历文本",
		metadata: map[string]interface{}{
			"source_file_path": "test.pdf",
		},
	}

	mockChunker := &MockResumeChunker{
		sections: []*types.ResumeSection{
			{
				Type:    types.SectionBasicInfo,
				Title:   "基本信息",
				Content: "姓名：测试",
			},
			{
				Type:    types.SectionEducation,
				Title:   "教育经历",
				Content: "大学：测试大学",
			},
		},
		basicInfo: map[string]string{
			"name": "测试",
		},
	}

	mockEmbedder := &MockResumeEmbedder{
		vectors: []*types.ResumeChunkVector{
			{
				Section: mockChunker.sections[0],
				Vector:  []float64{0.1, 0.2, 0.3},
			},
		},
	}

	mockEvaluator := &MockJobMatchEvaluator{
		evaluation: &types.JobMatchEvaluation{
			MatchScore:      75,
			MatchHighlights: []string{"技能匹配", "教育背景相符"},
			PotentialGaps:   []string{"经验不足"},
			EvaluatedAt:     time.Now().Unix(),
		},
	}

	// 测试组件配置
	t.Run("ComponentConfiguration", func(t *testing.T) {
		processor := NewResumeProcessor(
			WithPDFExtractor(mockExtractor),
			WithResumeChunker(mockChunker),
			WithResumeEmbedder(mockEmbedder),
			WithJobMatchEvaluator(mockEvaluator),
			WithDebugMode(true),
		)

		// 验证组件是否正确配置
		assert.Equal(t, mockExtractor, processor.PDFExtractor)
		assert.Equal(t, mockChunker, processor.ResumeChunker)
		assert.Equal(t, mockEmbedder, processor.ResumeEmbedder)
		assert.Equal(t, mockEvaluator, processor.MatchEvaluator)
		assert.True(t, processor.Config.Debug)
	})

	// 测试默认处理器创建
	t.Run("DefaultProcessorCreation", func(t *testing.T) {
		// 为 CreateDefaultProcessor 提供必要的参数
		var mockStorageManager *storage.Storage // 可以是nil，如果函数允许
		var testConfig config.Config            // 使用一个空的config实例，而不是nil

		processor, err := CreateDefaultProcessor(ctx, &testConfig, mockStorageManager)
		require.NoError(t, err)

		// 验证默认组件
		assert.NotNil(t, processor.PDFExtractor)
		assert.NotNil(t, processor.ResumeChunker)
		assert.Nil(t, processor.ResumeEmbedder)
		assert.Nil(t, processor.MatchEvaluator)
		assert.True(t, processor.Config.Debug)
	})

	// 测试日志功能
	t.Run("LoggingFunctionality", func(t *testing.T) {
		processor := NewResumeProcessor(
			WithDebugMode(true),
		)

		// 由于日志输出到标准输出，这里我们只是确认没有发生异常
		processor.LogDebug("测试日志消息")
	})
}
