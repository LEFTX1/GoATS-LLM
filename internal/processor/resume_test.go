package processor

import (
	"ai-agent-go/internal/types"
	"log"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPrepareQdrantData_FilterBasicInfo(t *testing.T) {
	// 创建一个简单的处理器实例
	processor := &ResumeProcessor{
		Config: ComponentConfig{
			Debug:             true,
			DefaultDimensions: 1536,
			Logger:            log.New(os.Stdout, "[TestProcessor] ", log.LstdFlags),
		},
	}

	// 准备测试数据
	submissionUUID := "test-uuid"

	// 创建不同类型的ResumeSection
	sections := []*types.ResumeSection{
		{
			Type:    types.SectionBasicInfo,
			Title:   "基本信息",
			Content: "姓名: 测试用户\n电话: 12345678901\n邮箱: test@example.com",
			ChunkID: 1,
		},
		{
			Type:    types.SectionEducation,
			Title:   "教育经历",
			Content: "某大学 计算机科学 2018-2022",
			ChunkID: 2,
		},
		{
			Type:    types.SectionWorkExperience,
			Title:   "工作经历",
			Content: "某公司 软件工程师 2022-2023",
			ChunkID: 3,
		},
	}

	// 创建基本信息
	basicInfo := map[string]string{
		"name":  "测试用户",
		"phone": "12345678901",
		"email": "test@example.com",
	}

	// 创建模拟的向量数据
	chunkEmbeddings := []*types.ResumeChunkVector{
		{
			Section: sections[0], // BASIC_INFO
			Vector:  []float64{0.1, 0.2, 0.3},
			Metadata: map[string]interface{}{
				"importance_score": float64(1.0),
			},
		},
		{
			Section: sections[1], // EDUCATION
			Vector:  []float64{0.4, 0.5, 0.6},
			Metadata: map[string]interface{}{
				"importance_score": float64(1.0),
			},
		},
		{
			Section: sections[2], // WORK_EXPERIENCE
			Vector:  []float64{0.7, 0.8, 0.9},
			Metadata: map[string]interface{}{
				"importance_score": float64(1.0),
			},
		},
	}

	// 调用被测试函数
	chunks, embeddings, err := processor._prepareQdrantData(submissionUUID, sections, basicInfo, chunkEmbeddings)

	// 验证结果
	assert.NoError(t, err)
	assert.Equal(t, 2, len(chunks), "应该只有2个分块（过滤掉了BASIC_INFO）")
	assert.Equal(t, 2, len(embeddings), "应该只有2个向量（过滤掉了BASIC_INFO）")

	// 验证没有BASIC_INFO类型的分块
	for _, chunk := range chunks {
		assert.NotEqual(t, string(types.SectionBasicInfo), chunk.ChunkType, "不应包含BASIC_INFO类型的分块")
	}

	// 验证保留了正确的分块类型
	expectedTypes := []string{string(types.SectionEducation), string(types.SectionWorkExperience)}
	actualTypes := []string{chunks[0].ChunkType, chunks[1].ChunkType}
	assert.ElementsMatch(t, expectedTypes, actualTypes, "应包含正确的分块类型")
}
