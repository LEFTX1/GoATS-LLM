package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"ai-agent-go/pkg/agent"
	"ai-agent-go/pkg/types" // 导入新的 types 包

	"github.com/cloudwego/eino/schema"
)

// ResumeChunk, Identity, ChunkMetadata, ResumeData 结构体已移至 pkg/types

// LLMChunker 使用LLM将简历文本分块并提取元数据
type LLMChunker struct {
	llmClient *agent.AliyunQwenChatModel
}

// NewLLMChunker 创建一个新的LLM分块器实例
func NewLLMChunker(llmClient *agent.AliyunQwenChatModel) *LLMChunker {
	return &LLMChunker{
		llmClient: llmClient,
	}
}

// InitAliyunQwenLLM 初始化阿里云通义千问LLM客户端
// 注意：AliyunQwenChatModel 的创建可能需要 apiKey, modelName, apiURL
// 这里我们假设这些配置会从外部传入或从配置文件读取
func InitAliyunQwenLLM(apiKey string, modelName string, apiURL string) (*agent.AliyunQwenChatModel, error) {
	// 假设 NewAliyunQwenChatModel 是 agent 包中暴露的构造函数
	llm, err := agent.NewAliyunQwenChatModel(apiKey, modelName, apiURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create Aliyun Qwen LLM client: %w", err)
	}
	return llm, nil
}

// ProcessResume 处理简历文本，使用LLM进行分块和元数据提取
// 返回类型修改为 types.ResumeData
func (c *LLMChunker) ProcessResume(ctx context.Context, resumeText string) (*types.ResumeData, error) {
	prompt := buildResumeChunkingPrompt(resumeText)

	// 构建 Eino schema.Message
	messages := []*schema.Message{
		schema.UserMessage(prompt),
	}

	// 调用 AliyunQwenChatModel 的 Generate 方法
	// 注意：AliyunQwenChatModel 的 Generate 方法可能还需要其他 model.Option 参数
	llmResponseMsg, err := c.llmClient.Generate(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("LLM API call failed: %w", err)
	}

	llmResponseContent := llmResponseMsg.Content

	// 提取JSON部分 (这部分逻辑保持不变)
	var resumeData types.ResumeData
	if err := json.Unmarshal([]byte(llmResponseContent), &resumeData); err != nil {
		log.Printf("Failed to parse LLM response as JSON: %v\nResponse was: %s", err, llmResponseContent)

		jsonStr, extracted := extractJSONFromText(llmResponseContent)
		if !extracted {
			return nil, fmt.Errorf("could not extract valid JSON from LLM response")
		}

		if err := json.Unmarshal([]byte(jsonStr), &resumeData); err != nil {
			return nil, fmt.Errorf("extracted JSON is invalid: %w", err)
		}
	}

	return &resumeData, nil
}

// buildResumeChunkingPrompt 构建用于简历分块的prompt
func buildResumeChunkingPrompt(resumeText string) string {
	return fmt.Sprintf(`你是一位专业的简历分析AI。请将以下简历文本分成有意义的区块，并为每个区块添加元数据。
	
输出格式为完全符合JSON语法的结构，应包含以下字段:
{
  "resume_id": "根据姓名和电话生成的唯一标识符",
  "candidate_info": {
    "name": "姓名",
    "phone": "手机号",
    "email": "邮箱"
  },
  "chunks": [
    {
      "chunk_id": 1,
      "chunk_type": "个人信息/教育背景/工作经历/项目经验/技能描述等",
      "content": "原文内容",
      "importance_score": 1-10的重要性评分,
      "unique_identifiers": {
        "name": "姓名(如有)",
        "phone": "电话(如有)",
        "email": "邮箱(如有)"
      },
      "metadata": {
        "skills": ["技能1", "技能2"],
        "experience_years": 经验年限,
        "education_level": "最高学历"
      }
    }
  ],
  "summary": {
    "skills": ["技能1", "技能2"],
    "total_experience": "总经验年限",
    "education": "最高学历"
  }
}

仅返回JSON格式的结果，不要包含其他文本。

简历文本:
%s`, resumeText)
}

// extractJSONFromText 从文本中提取JSON部分
func extractJSONFromText(text string) (string, bool) {
	// 简单实现：查找第一个{和最后一个}
	start := -1
	for i, char := range text {
		if char == '{' {
			start = i
			break
		}
	}

	end := -1
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == '}' {
			end = i + 1
			break
		}
	}

	if start != -1 && end != -1 && start < end {
		return text[start:end], true
	}

	return "", false
}
