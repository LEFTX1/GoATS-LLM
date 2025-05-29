package processor

import (
	"context"
	"testing"
	"time"

	"ai-agent-go/pkg/parser"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 模拟PDF提取器
type mockPDFExtractor struct {
	text     string
	metadata map[string]interface{}
	err      error
}

func (m *mockPDFExtractor) ExtractFromFile(ctx context.Context, filePath string) (string, map[string]interface{}, error) {
	return m.text, m.metadata, m.err
}

// 模拟分块器
type mockChunker struct {
	sections  []*parser.ResumeSection
	basicInfo map[string]string
	err       error
}

func (m *mockChunker) ChunkResume(ctx context.Context, text string) ([]*parser.ResumeSection, map[string]string, error) {
	return m.sections, m.basicInfo, m.err
}

// 模拟嵌入器
type mockEmbedder struct {
	vectors []*parser.ResumeChunkVector
	err     error
}

func (m *mockEmbedder) Embed(ctx context.Context, sections []*parser.ResumeSection, basicInfo map[string]string) ([]*parser.ResumeChunkVector, error) {
	return m.vectors, m.err
}

// 模拟向量存储
type mockVectorStore struct {
	err error
}

func (m *mockVectorStore) StoreVectors(ctx context.Context, vectors []*parser.ResumeChunkVector) error {
	return m.err
}

func TestStandardResumeProcessor_Process(t *testing.T) {
	// 创建上下文
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 创建模拟组件
	mockExtractor := &mockPDFExtractor{
		text: "这是一个测试简历文本",
		metadata: map[string]interface{}{
			"source_file_path": "test.pdf",
		},
	}

	mockRuleChunker := &mockChunker{
		sections: []*parser.ResumeSection{
			{
				Type:    parser.SectionBasicInfo,
				Title:   "基本信息",
				Content: "姓名：测试",
			},
			{
				Type:    parser.SectionEducation,
				Title:   "教育经历",
				Content: "大学：测试大学",
			},
		},
		basicInfo: map[string]string{
			"name": "测试",
		},
	}

	mockLLMChunker := &mockChunker{
		sections: []*parser.ResumeSection{
			{
				Type:    parser.SectionBasicInfo,
				Title:   "基本信息",
				Content: "姓名：测试 LLM",
			},
			{
				Type:    parser.SectionEducation,
				Title:   "教育经历",
				Content: "大学：测试大学 LLM",
			},
		},
		basicInfo: map[string]string{
			"name": "测试 LLM",
		},
	}

	// 创建处理器
	processor := NewStandardResumeProcessor(
		WithPDFExtractor(mockExtractor),
		WithChunker("rule", mockRuleChunker),
		WithChunker("llm", mockLLMChunker),
	)

	// 测试基本处理流程
	t.Run("BasicProcess", func(t *testing.T) {
		options := ProcessOptions{
			InputFile: "test.pdf",
			UseLLM:    false,
		}

		result, err := processor.Process(ctx, options)
		require.NoError(t, err)

		assert.Equal(t, "这是一个测试简历文本", result.Text)
		assert.Equal(t, "test.pdf", result.Metadata["source_file_path"])
		assert.Equal(t, 2, len(result.Sections))
		assert.Equal(t, "测试", result.BasicInfo["name"])
	})

	// 测试LLM处理流程
	t.Run("LLMProcess", func(t *testing.T) {
		options := ProcessOptions{
			InputFile: "test.pdf",
			UseLLM:    true,
		}

		result, err := processor.Process(ctx, options)
		require.NoError(t, err)

		assert.Equal(t, "这是一个测试简历文本", result.Text)
		assert.Equal(t, 2, len(result.Sections))
		assert.Equal(t, "测试 LLM", result.BasicInfo["name"])
	})

	// 测试嵌入和存储
	t.Run("EmbedAndStore", func(t *testing.T) {
		mockEmbed := &mockEmbedder{
			vectors: []*parser.ResumeChunkVector{
				{
					Section: mockRuleChunker.sections[0],
					Vector:  []float64{0.1, 0.2, 0.3},
				},
			},
		}

		mockStore := &mockVectorStore{}

		processorWithEmbed := NewStandardResumeProcessor(
			WithPDFExtractor(mockExtractor),
			WithChunker("rule", mockRuleChunker),
			WithEmbedder(mockEmbed),
			WithVectorStore(mockStore),
		)

		options := ProcessOptions{
			InputFile:  "test.pdf",
			UseLLM:     false,
			Dimensions: 3,
		}

		result, err := processorWithEmbed.Process(ctx, options)
		require.NoError(t, err)

		assert.Equal(t, 1, len(result.Vectors))
		assert.Equal(t, 3, len(result.Vectors[0].Vector))
	})
}
