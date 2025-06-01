package processor

import (
	"ai-agent-go/internal/config"
	parser2 "ai-agent-go/internal/parser"
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ResumeProcessor 简历处理组件聚合类
// 不再控制处理流程，仅提供组件集合
type ResumeProcessor struct {
	// 核心组件接口
	PDFExtractor   PDFExtractor      // PDF文本提取接口
	ResumeChunker  ResumeChunker     // 简历分块接口
	ResumeEmbedder ResumeEmbedder    // 简历嵌入接口
	MatchEvaluator JobMatchEvaluator // 岗位匹配评估接口

	// 配置
	Config ComponentConfig
}

// ComponentConfig 组件配置
type ComponentConfig struct {
	UseLLM            bool
	DefaultDimensions int
	Debug             bool
	Logger            *log.Logger
}

// NewResumeProcessor 创建新的简历处理器组件集合
func NewResumeProcessor(options ...ProcessorOption) *ResumeProcessor {
	// 创建默认处理器
	processor := &ResumeProcessor{
		Config: ComponentConfig{
			UseLLM:            true,
			DefaultDimensions: 1536,
			Debug:             false,
			Logger:            log.New(os.Stdout, "[Processor] ", log.LstdFlags),
		},
	}

	// 应用选项
	for _, option := range options {
		option(processor)
	}

	return processor
}

// LogDebug 记录调试日志
func (p *ResumeProcessor) LogDebug(message string) {
	if p.Config.Debug && p.Config.Logger != nil {
		p.Config.Logger.Println(message)
	}
}

// 工厂方法 - 创建处理器的辅助函数

// CreateDefaultProcessor 创建默认配置的简历处理器组件集合
func CreateDefaultProcessor(ctx context.Context, cfg *config.Config) (*ResumeProcessor, error) {
	// 创建PDF提取器
	var pdfExtractor PDFExtractor
	var err error

	if cfg != nil && cfg.Tika.ServerURL != "" && cfg.Tika.Type == "tika" {
		var tikaOptions []parser2.TikaOption
		if cfg.Tika.MetadataMode == "full" {
			tikaOptions = append(tikaOptions, parser2.WithFullMetadata(true))
		} else if cfg.Tika.MetadataMode == "none" {
			tikaOptions = append(tikaOptions, parser2.WithMinimalMetadata(false), parser2.WithFullMetadata(false))
		} else { // "minimal" or default
			tikaOptions = append(tikaOptions, parser2.WithMinimalMetadata(true))
		}
		if cfg.Tika.Timeout > 0 {
			tikaOptions = append(tikaOptions, parser2.WithTimeout(time.Duration(cfg.Tika.Timeout)*time.Second))
		}
		tikaOptions = append(tikaOptions, parser2.WithTikaLogger(log.New(os.Stderr, "[TikaPDFDefault] ", log.LstdFlags)))
		pdfExtractor = parser2.NewTikaPDFExtractor(cfg.Tika.ServerURL, tikaOptions...)
		log.Printf("默认使用Tika PDF解析器 (模式: %s), Tika Server: %s", cfg.Tika.MetadataMode, cfg.Tika.ServerURL)
	} else {
		// Fallback to Eino if Tika is not configured or type is not tika
		pdfExtractor, err = parser2.NewEinoPDFTextExtractor(ctx,
			parser2.WithEinoLogger(log.New(os.Stderr, "[EinoPDFDefault] ", log.LstdFlags)),
		)
		if err != nil {
			return nil, fmt.Errorf("创建Eino PDF提取器失败: %w", err)
		}
		log.Println("默认使用Eino PDF解析器 (Tika未配置或类型不匹配)")
	}

	// 创建简单的Mock LLM模型
	mockLLM := &MockLLMModel{}

	// 创建LLM分块器
	llmChunker := parser2.NewLLMResumeChunker(mockLLM)

	// 创建处理器
	processor := NewResumeProcessor(
		WithPDFExtractor(pdfExtractor),
		WithResumeChunker(llmChunker),
		WithDebugMode(true),
	)

	return processor, nil
}

// CreateProcessorFromConfig 从配置创建处理器组件集合
func CreateProcessorFromConfig(ctx context.Context, cfg *config.Config) (*ResumeProcessor, error) {
	if cfg == nil {
		return nil, fmt.Errorf("配置不能为空")
	}

	// 创建PDF提取器
	var pdfExtractor PDFExtractor
	var err error

	// 根据配置选择PDF提取器
	if cfg.Tika.Type == "tika" && cfg.Tika.ServerURL != "" {
		var tikaOptions []parser2.TikaOption
		if cfg.Tika.MetadataMode == "full" {
			tikaOptions = append(tikaOptions, parser2.WithFullMetadata(true))
		} else if cfg.Tika.MetadataMode == "none" {
			tikaOptions = append(tikaOptions, parser2.WithMinimalMetadata(false), parser2.WithFullMetadata(false))
		} else { // "minimal" or default
			tikaOptions = append(tikaOptions, parser2.WithMinimalMetadata(true))
		}
		if cfg.Tika.Timeout > 0 {
			tikaOptions = append(tikaOptions, parser2.WithTimeout(time.Duration(cfg.Tika.Timeout)*time.Second))
		}
		tikaOptions = append(tikaOptions, parser2.WithTikaLogger(log.New(os.Stderr, "[TikaPDFConfig] ", log.LstdFlags)))
		pdfExtractor = parser2.NewTikaPDFExtractor(cfg.Tika.ServerURL, tikaOptions...)
		log.Printf("使用Tika PDF解析器 (模式: %s), Tika Server: %s", cfg.Tika.MetadataMode, cfg.Tika.ServerURL)
	} else if cfg.Tika.Type == "eino" { // Assuming eino can also be configured under Tika.Type for simplicity, or a new PDFParserType field can be added
		pdfExtractor, err = parser2.NewEinoPDFTextExtractor(ctx, parser2.WithEinoLogger(log.New(os.Stderr, "[EinoPDFConfig] ", log.LstdFlags)))
		if err != nil {
			return nil, fmt.Errorf("创建Eino PDF提取器失败: %w", err)
		}
		log.Println("使用Eino PDF解析器")
	} else {
		// 默认回退到Eino
		log.Printf("未知的PDF解析器类型 '%s' 或Tika配置不完整, 回退到Eino PDF解析器", cfg.Tika.Type)
		pdfExtractor, err = parser2.NewEinoPDFTextExtractor(ctx, parser2.WithEinoLogger(log.New(os.Stderr, "[EinoPDFFallback] ", log.LstdFlags)))
		if err != nil {
			return nil, fmt.Errorf("创建回退Eino PDF提取器失败: %w", err)
		}
	}

	// 创建处理器
	processor := NewResumeProcessor(
		WithPDFExtractor(pdfExtractor),
		WithDefaultDimensions(cfg.Qdrant.Dimension),
	)

	// 如果配置了API密钥，添加LLM功能
	if cfg.Aliyun.APIKey != "" {
		// 这里可以创建LLM模型、分块器、嵌入器和评估器
		processor.Config.Logger.Println("检测到API密钥，准备配置LLM功能")
	}

	return processor, nil
}

// NewProcessorFromConfig 是 CreateProcessorFromConfig 的别名，保持向后兼容性
func NewProcessorFromConfig(ctx context.Context, cfg *config.Config) (*ResumeProcessor, error) {
	return CreateProcessorFromConfig(ctx, cfg)
}

// MockLLMModel 是一个简单的LLM模型模拟实现，用于测试
type MockLLMModel struct{}

// Generate 实现 model.ChatModel 接口
func (m *MockLLMModel) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	return &schema.Message{
		Role:    "assistant",
		Content: `{"basic_info":{},"chunks":[],"metadata":{"is_211":false,"is_985":false,"is_double_top":false,"has_intern":false,"highest_education":"","years_of_experience":0,"resume_score":0,"tags":[]}}`,
	}, nil
}

// Stream 实现 model.ChatModel 接口
func (m *MockLLMModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("stream not implemented in mock")
}

// WithTools 实现 model.ToolCallingChatModel 接口
func (m *MockLLMModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

// BindTools 实现 model.ChatModel 接口
func (m *MockLLMModel) BindTools(tools []*schema.ToolInfo) error {
	return nil
}
