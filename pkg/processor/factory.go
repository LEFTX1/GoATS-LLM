package processor

import (
	"context"
	"fmt"
	"os"

	"ai-agent-go/pkg/agent"
	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/parser"
)

var appConfig *config.Config

// 初始化配置
func init() {
	var err error
	appConfig, err = config.LoadConfig("")
	if err != nil {
		// 如果配置文件不存在，则不会初始化配置
		// 此时程序将回退到使用环境变量或其他默认值
		fmt.Printf("警告: 加载配置文件失败: %v\n", err)
	}
}

// CreateDefaultProcessor 创建默认配置的简历处理器
func CreateDefaultProcessor(ctx context.Context) (Processor, error) {
	// 1. 创建PDF提取器
	pdfExtractor, err := NewEinoPDFAdapter(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建PDF提取器失败: %w", err)
	}

	// 2. 创建规则分块器
	ruleChunker, err := NewRuleChunkerAdapter(parser.ChunkerConfig{
		PreserveFormat: false,
		MinChunkSize:   0,
	})
	if err != nil {
		return nil, fmt.Errorf("创建规则分块器失败: %w", err)
	}

	// 创建处理器
	processor := NewStandardResumeProcessor(
		WithPDFExtractor(pdfExtractor),
		WithChunker("rule", ruleChunker),
	)

	return processor, nil
}

// CreateFullProcessor 创建完整功能的简历处理器（包含LLM、嵌入和存储）
func CreateFullProcessor(
	ctx context.Context,
	apiKey string,
	qdrantEndpoint string,
	qdrantCollection string,
	dimensions int,
) (Processor, error) {
	// 1. 创建基础处理器
	processor, err := CreateDefaultProcessor(ctx)
	if err != nil {
		return nil, err
	}

	standardProcessor, ok := processor.(*StandardResumeProcessor)
	if !ok {
		return nil, fmt.Errorf("处理器类型错误")
	}

	// 2. 获取API密钥，优先顺序：
	// 1) 参数传入 2) 配置文件 3) 环境变量
	if apiKey == "" && appConfig != nil && appConfig.Aliyun.APIKey != "" {
		apiKey = appConfig.Aliyun.APIKey
	}
	if apiKey == "" {
		apiKey = os.Getenv("ALIYUN_API_KEY")
	}

	// 如果有API密钥，创建LLM模型
	if apiKey != "" {
		// 获取API URL，优先顺序：
		// 1) 配置文件 2) 环境变量 3) 默认值
		apiURL := ""
		if appConfig != nil && appConfig.Aliyun.APIURL != "" {
			apiURL = appConfig.Aliyun.APIURL
		} else {
			apiURL = os.Getenv("ALIYUN_API_URL")
		}
		if apiURL == "" {
			apiURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
		}

		// 获取模型名称
		modelName := ""
		if appConfig != nil && appConfig.Aliyun.Model != "" {
			modelName = appConfig.Aliyun.Model
		} else {
			modelName = os.Getenv("ALIYUN_MODEL")
		}
		if modelName == "" {
			modelName = "qwen-turbo"
		}

		// 创建LLM模型
		llmModel, err := agent.NewAliyunQwenChatModel(apiKey, modelName, apiURL)
		if err != nil {
			return nil, fmt.Errorf("创建LLM模型失败: %w", err)
		}

		// 创建LLM分块器
		llmChunker := parser.NewLLMResumeChunker(llmModel)
		llmChunkerAdapter := NewLLMChunkerAdapter(llmChunker)

		// 添加到处理器
		standardProcessor.chunkers["llm"] = llmChunkerAdapter
	}

	// 3. 获取Qdrant配置，优先级与上面类似
	if qdrantEndpoint == "" && appConfig != nil && appConfig.Qdrant.Endpoint != "" {
		qdrantEndpoint = appConfig.Qdrant.Endpoint
	}

	if qdrantCollection == "" && appConfig != nil && appConfig.Qdrant.Collection != "" {
		qdrantCollection = appConfig.Qdrant.Collection
	}

	if dimensions <= 0 && appConfig != nil && appConfig.Qdrant.Dimension > 0 {
		dimensions = appConfig.Qdrant.Dimension
	}

	// 3. 创建嵌入器（如果提供了API密钥）
	if apiKey != "" {
		aliyunEmbedder, err := parser.NewAliyunEmbedder(apiKey,
			parser.WithAliyunDimensions(dimensions),
			parser.WithAliyunModel("text-embedding-v3"))
		if err != nil {
			return nil, fmt.Errorf("创建嵌入模型失败: %w", err)
		}

		resumeEmbedder := parser.NewStandardResumeEmbedder(aliyunEmbedder,
			parser.WithIncludeBasicInfo(true),
			parser.WithIncludeContent(false),
			parser.WithIncludeChunkType(true))

		embedderAdapter := NewEmbedderAdapter(resumeEmbedder)
		standardProcessor.embedder = embedderAdapter
	}

	// 4. 创建向量存储（如果提供了Qdrant配置）
	if qdrantEndpoint != "" && qdrantCollection != "" && dimensions > 0 {
		// 创建临时配置
		cfg := &config.Config{}

		// 填充Qdrant配置
		cfg.Qdrant.Endpoint = qdrantEndpoint
		cfg.Qdrant.Collection = qdrantCollection
		cfg.Qdrant.Dimension = dimensions

		vectorStore, err := NewVectorStoreAdapter(cfg)
		if err != nil {
			return nil, fmt.Errorf("创建向量存储失败: %w", err)
		}

		standardProcessor.store = vectorStore
	}

	return standardProcessor, nil
}
