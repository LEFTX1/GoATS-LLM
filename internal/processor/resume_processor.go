package processor

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	parser2 "ai-agent-go/internal/parser"
	"ai-agent-go/internal/storage"
	storage_types "ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"ai-agent-go/pkg/utils"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
)

// Components 聚合所有功能组件依赖，便于集中管理和测试替换
type Components struct {
	// 核心组件接口
	PDFExtractor   PDFExtractor      // PDF文本提取接口
	ResumeChunker  ResumeChunker     // 简历分块接口
	ResumeEmbedder ResumeEmbedder    // 简历嵌入接口
	MatchEvaluator JobMatchEvaluator // 岗位匹配评估接口

	// 存储层依赖
	Storage *storage.Storage // 聚合的存储服务
}

// Settings 纯配置项，不包含任何业务逻辑组件
type Settings struct {
	UseLLM            bool           // 是否使用LLM
	DefaultDimensions int            // 默认向量维度
	Debug             bool           // 是否开启调试模式
	Logger            *log.Logger    // 日志记录器
	TimeLocation      *time.Location // 时区设置
}

// ResumeProcessor 简历处理组件聚合类
// 不再控制处理流程，仅提供组件集合
type ResumeProcessor struct {
	// 核心组件接口
	PDFExtractor   PDFExtractor      // PDF文本提取接口
	ResumeChunker  ResumeChunker     // 简历分块接口
	ResumeEmbedder ResumeEmbedder    // 简历嵌入接口
	MatchEvaluator JobMatchEvaluator // 岗位匹配评估接口

	// 存储层依赖
	Storage *storage.Storage

	// 配置
	Config ComponentConfig
}

// ComponentConfig 组件配置 (保留兼容现有代码)
type ComponentConfig struct {
	UseLLM            bool
	DefaultDimensions int
	Debug             bool
	Logger            *log.Logger
}

// ProcessorOption 是 ResumeProcessor 的配置选项
// type ProcessorOption func(*ResumeProcessor)

// WithStorage 是一个选项，用于设置 ResumeProcessor 的 Storage 依赖
func WithStorage(s *storage.Storage) ProcessorOption {
	return func(rp *ResumeProcessor) {
		rp.Storage = s
	}
}

// WithProcessorLogger sets the logger for the ResumeProcessor.
// This directly sets the logger in the ComponentConfig.
func WithProcessorLogger(logger *log.Logger) ProcessorOption {
	return func(rp *ResumeProcessor) {
		if logger != nil {
			rp.Config.Logger = logger
		} else {
			// Fallback to a discard logger if nil is passed, to prevent panics.
			rp.Config.Logger = log.New(io.Discard, "[ResumeProcessorNilLoggerFallback] ", log.LstdFlags)
		}
	}
}

// NewResumeProcessorV2 创建新的简历处理器，使用明确分离的组件和设置
// 这是推荐的构造方法，可以确保清晰的依赖关系
func NewResumeProcessorV2(comp *Components, set *Settings, opts ...SettingOpt) *ResumeProcessor {
	// 应用额外的设置选项
	for _, opt := range opts {
		opt(set)
	}

	// 确保必要的默认值
	if set.Logger == nil {
		set.Logger = log.New(os.Stdout, "[Processor] ", log.LstdFlags)
	}

	if set.TimeLocation == nil {
		set.TimeLocation = time.Local
	}

	// 创建处理器实例，复制组件和设置到对应字段
	processor := &ResumeProcessor{
		// 从 Components 复制组件
		PDFExtractor:   comp.PDFExtractor,
		ResumeChunker:  comp.ResumeChunker,
		ResumeEmbedder: comp.ResumeEmbedder,
		MatchEvaluator: comp.MatchEvaluator,
		Storage:        comp.Storage,

		// 从 Settings 复制设置到 Config
		Config: ComponentConfig{
			UseLLM:            set.UseLLM,
			DefaultDimensions: set.DefaultDimensions,
			Debug:             set.Debug,
			Logger:            set.Logger,
		},
	}

	// 验证关键组件
	if processor.Storage == nil {
		processor.Config.Logger.Println("警告: ResumeProcessor 的 Storage 依赖未初始化。某些功能可能受限。")
	}

	return processor
}

// NewResumeProcessor 创建新的简历处理器组件集合 (向后兼容的构造函数)
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

	// 确保 Storage 已被正确初始化，如果没有通过Option注入，则警告
	if processor.Storage == nil {
		processor.Config.Logger.Println("警告: ResumeProcessor 的 Storage 依赖未通过选项注入。某些功能可能受限。")
	}

	return processor
}

// LogDebug 记录调试日志
func (p *ResumeProcessor) LogDebug(message string) {
	if p.Config.Debug && p.Config.Logger != nil {
		p.Config.Logger.Println(message)
	}
}

// 示例使用 V2 API：
//
// 在 main.go 或初始化代码中:
//
// func initProcessor(ctx context.Context, cfg *config.Config) (*ResumeProcessor, error) {
//     // 1. 创建存储依赖
//     storage, err := storage.NewStorage(ctx, cfg)
//     if err != nil {
//         return nil, err
//     }
//
//     // 2. 创建组件
//     components := &processor.Components{
//         Storage: storage,
//     }
//
//     // 3. 添加PDF提取器
//     components.PDFExtractor = parser.NewTikaPDFExtractor(cfg.Tika.ServerURL)
//
//     // 4. 配置设置
//     settings := &processor.Settings{
//         UseLLM:            true,
//         DefaultDimensions: cfg.Qdrant.Dimension,
//         Debug:             cfg.Debug,
//         Logger:            log.New(os.Stdout, "[Processor] ", log.LstdFlags),
//     }
//
//     // 5. 创建处理器
//     return processor.NewResumeProcessorV2(components, settings), nil
// }
//
// 或者使用便捷工厂函数:
//
// func initProcessorSimple(ctx context.Context, storage *storage.Storage) (*ResumeProcessor, error) {
//     pdfExtractor := parser.NewTikaPDFExtractor("http://localhost:9998")
//
//     // 使用工厂函数和选项
//     return processor.CreateProcessor(ctx,
//         []processor.ComponentOpt{
//             processor.WithComp_PDFExtractor(pdfExtractor),
//             processor.WithComp_Storage(storage),
//         },
//         []processor.SettingOpt{
//             processor.WithSet_Debug(true),
//             processor.WithSet_DefaultDimensions(1536),
//         },
//     )
// }

// 工厂方法 - 创建处理器的辅助函数

// CreateDefaultProcessor 创建默认配置的简历处理器组件集合
func CreateDefaultProcessor(ctx context.Context, cfg *config.Config, storageManager *storage.Storage) (*ResumeProcessor, error) {
	// 1. 创建组件实例
	components := &Components{
		Storage: storageManager,
	}

	// 2. 创建设置实例
	settings := &Settings{
		UseLLM:            true,
		DefaultDimensions: 1536,
		Debug:             true,
		Logger:            log.New(os.Stdout, "[Processor] ", log.LstdFlags),
		TimeLocation:      time.Local,
	}

	// 3. 创建PDF提取器
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
		components.PDFExtractor = parser2.NewTikaPDFExtractor(cfg.Tika.ServerURL, tikaOptions...)
		log.Printf("默认使用Tika PDF解析器 (模式: %s), Tika Server: %s", cfg.Tika.MetadataMode, cfg.Tika.ServerURL)
	} else {
		// Fallback to Eino if Tika is not configured or type is not tika
		components.PDFExtractor, err = parser2.NewEinoPDFTextExtractor(ctx,
			parser2.WithEinoLogger(log.New(os.Stderr, "[EinoPDFDefault] ", log.LstdFlags)),
		)
		if err != nil {
			return nil, fmt.Errorf("创建Eino PDF提取器失败: %w", err)
		}
		log.Println("默认使用Eino PDF解析器 (Tika未配置或类型不匹配)")
	}

	// 4. 创建mock LLM模型
	mockLLM := &MockLLMModel{}

	// 5. 创建LLM分块器
	discardLogger := log.New(io.Discard, "", 0)
	components.ResumeChunker = parser2.NewLLMResumeChunker(mockLLM, discardLogger)

	// 6. 使用新的构造函数创建处理器
	processor := NewResumeProcessorV2(components, settings)

	return processor, nil
}

// CreateProcessorFromConfig 从配置创建处理器组件集合
func CreateProcessorFromConfig(ctx context.Context, cfg *config.Config, storageManager *storage.Storage) (*ResumeProcessor, error) {
	if cfg == nil {
		return nil, fmt.Errorf("配置不能为空")
	}

	// 1. 创建组件实例
	components := &Components{
		Storage: storageManager,
	}

	// 2. 创建设置实例，并使用配置文件中的值
	settings := &Settings{
		UseLLM:            true, // 默认启用LLM
		DefaultDimensions: cfg.Qdrant.Dimension,
		Debug:             cfg.Logger.Level == "debug",
		Logger:            log.New(os.Stdout, "[Processor] ", log.LstdFlags),
		TimeLocation:      time.Local,
	}

	// 3. 根据配置创建PDF提取器
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
		components.PDFExtractor = parser2.NewTikaPDFExtractor(cfg.Tika.ServerURL, tikaOptions...)
		log.Printf("使用Tika PDF解析器 (模式: %s), Tika Server: %s", cfg.Tika.MetadataMode, cfg.Tika.ServerURL)
	} else if cfg.Tika.Type == "eino" { // Assuming eino can also be configured under Tika.Type for simplicity, or a new PDFParserType field can be added
		components.PDFExtractor, err = parser2.NewEinoPDFTextExtractor(ctx, parser2.WithEinoLogger(log.New(os.Stderr, "[EinoPDFConfig] ", log.LstdFlags)))
		if err != nil {
			return nil, fmt.Errorf("创建Eino PDF提取器失败: %w", err)
		}
		log.Println("使用Eino PDF解析器")
	} else {
		// 默认回退到Eino
		log.Printf("未知的PDF解析器类型 '%s' 或Tika配置不完整, 回退到Eino PDF解析器", cfg.Tika.Type)
		components.PDFExtractor, err = parser2.NewEinoPDFTextExtractor(ctx, parser2.WithEinoLogger(log.New(os.Stderr, "[EinoPDFFallback] ", log.LstdFlags)))
		if err != nil {
			return nil, fmt.Errorf("创建回退Eino PDF提取器失败: %w", err)
		}
	}

	// 4. 创建处理器
	processor := NewResumeProcessorV2(components, settings)

	// 5. 如果配置了API密钥，添加LLM功能
	if cfg.Aliyun.APIKey != "" {
		// 这里可以创建LLM模型、分块器、嵌入器和评估器，使用新的组件设置方式
		processor.Config.Logger.Println("检测到API密钥，准备配置LLM功能")
		// 此处可以增加更多的LLM组件初始化
	}

	return processor, nil
}

// NewProcessorFromConfig 是 CreateProcessorFromConfig 的别名，保持向后兼容性
func NewProcessorFromConfig(ctx context.Context, cfg *config.Config, storageManager *storage.Storage) (*ResumeProcessor, error) {
	return CreateProcessorFromConfig(ctx, cfg, storageManager)
}

// MockLLMModel 是一个模拟的LLM模型，用于测试目的
type MockLLMModel struct{}

// Generate 模拟LLM的响应生成
func (m *MockLLMModel) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	// 默认返回简历分块的 mock JSON
	responseContent := `{
		"basic_info": {
			"name": "Mock User",
			"phone": "12345678900",
			"email": "mock.user@example.com",
			"education_level": "本科",
			"position": "软件工程师"
		},
		"chunks": [
			{
				"chunk_id": 1,
				"resume_identifier": "Mock User_12345678900",
				"type": "BASIC_INFO",
				"title": "基本信息",
				"content": "Mock User, 12345678900, mock.user@example.com, 软件工程师"
			},
			{
				"chunk_id": 2,
				"resume_identifier": "Mock User_12345678900",
				"type": "EDUCATION",
				"title": "教育经历",
				"content": "Mock University, 计算机科学, 本科, 2018-2022"
			}
		],
		"metadata": {
			"is_211": false,
			"is_985": false,
			"is_double_top": false,
			"has_intern": true,
			"highest_education": "本科",
			"years_of_experience": 1.5,
			"resume_score": 75,
			"tags": ["Go", "Python"]
		}
	}`

	// 检查是否为 JD-简历匹配度评估场景
	if len(messages) > 0 {
		for _, msg := range messages {
			if msg.Role == "system" {
				// 使用您建议的关键词检查
				// 另外也加入 "你是一位极其资深的AI招聘专家" 作为补充判断条件，因为这是 job_evaluator.go 中模板的开头
				if strings.Contains(msg.Content, "岗位–简历匹配度评估") || strings.Contains(msg.Content, "你是一位极其资深的AI招聘专家") {
					responseContent = `{
						"match_score": 85,
						"match_highlights": [
							"Go & Python skills align with JD",
							"2 years experience meets requirement"
						],
						"potential_gaps": [],
						"resume_summary_for_jd": "This candidate shows good alignment with the job description, particularly in technical skills (Go, Python) and relevant experience duration."
					}`
					break
				}
			}
		}
	}

	return &schema.Message{
		Role:    "assistant",
		Content: responseContent,
	}, nil
}

// Stream 模拟LLM的流式响应 (当前未实现详细逻辑，可根据需要扩展)
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

// ProcessUploadedResume 接收上传消息，完成文本提取、去重、发送到LLM队列的完整流程
// 使用数据库事务确保所有状态更新的原子性
func (rp *ResumeProcessor) ProcessUploadedResume(ctx context.Context, message storage_types.ResumeUploadMessage, cfg *config.Config) error {
	if rp.Storage == nil {
		return fmt.Errorf("ResumeProcessor: Storage is not initialized")
	}
	if rp.PDFExtractor == nil {
		return fmt.Errorf("ResumeProcessor: PDFExtractor is not initialized")
	}

	// 使用GORM的Transaction回调模式确保事务完整性
	err := rp.Storage.MySQL.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 更新初始状态为 PENDING_PARSING (现在在事务内)
		if err := tx.Model(&models.ResumeSubmission{}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			Update("processing_status", constants.StatusPendingParsing).Error; err != nil {
			rp.LogDebug(fmt.Sprintf("更新简历 %s 状态为 %s 失败: %v", message.SubmissionUUID, constants.StatusPendingParsing, err))
			return NewUpdateError(message.SubmissionUUID, fmt.Sprintf("更新状态为%s失败", constants.StatusPendingParsing))
		}

		// 2. 从MinIO下载原始简历文件
		originalFileBytes, err := rp.Storage.MinIO.GetResumeFile(ctx, message.OriginalFilePathOSS)
		if err != nil {
			rp.LogDebug(fmt.Sprintf("从MinIO下载简历 %s 失败: %v", message.SubmissionUUID, err))
			return NewDownloadError(message.SubmissionUUID, err.Error())
		}
		rp.LogDebug(fmt.Sprintf("简历 %s 从MinIO下载成功，大小: %d bytes", message.SubmissionUUID, len(originalFileBytes)))

		// 3. 使用PDF提取器解析文本
		text, _, err := rp.PDFExtractor.ExtractTextFromReader(
			ctx,
			bytes.NewReader(originalFileBytes),
			message.OriginalFilePathOSS, // 使用OSS路径作为URI
			nil,                         // 假设不需要特定选项
		)
		if err != nil {
			rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 提取简历文本失败 for %s: %v", message.SubmissionUUID, err))
			return NewParseError(message.SubmissionUUID, err.Error())
		}
		rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 成功提取文本 for %s, 长度: %d", message.SubmissionUUID, len(text)))

		// 4. 计算提取文本的MD5并进行去重检查
		textMD5Hex := utils.CalculateMD5([]byte(text))
		rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 计算得到文本MD5 %s for %s", textMD5Hex, message.SubmissionUUID))

		// 使用原子操作检查MD5是否存在并添加（如不存在）
		textExists, err := rp.Storage.Redis.CheckAndAddParsedTextMD5(ctx, textMD5Hex)
		if err != nil {
			rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 使用Redis原子操作检查文本MD5失败 for %s: %v, 将继续处理，但文本去重可能失效", message.SubmissionUUID, err))
			// 如果Redis查询失败，选择继续处理而不是阻塞流程，但文本去重将不起作用
		} else if textExists {
			rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 检测到重复的文本MD5 %s for %s，标记为重复内容", textMD5Hex, message.SubmissionUUID))
			// 重复内容，更新状态并提前结束事务（成功）
			if err := tx.Model(&models.ResumeSubmission{}).
				Where("submission_uuid = ?", message.SubmissionUUID).
				Update("processing_status", constants.StatusContentDuplicateSkipped).Error; err != nil {
				return NewUpdateError(message.SubmissionUUID, "更新重复内容状态失败")
			}
			return nil // 内容重复，处理完成，事务提交
		}
		rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 文本MD5 %s 不存在于Redis, 继续处理 for %s", textMD5Hex, message.SubmissionUUID))

		// 注意: 不再需要单独的AddParsedTextMD5调用，因为CheckAndAddParsedTextMD5已经在MD5不存在时添加了

		// 5. 上传解析后的文本到MinIO
		textObjectKey, err := rp.Storage.MinIO.UploadParsedText(ctx, message.SubmissionUUID, text)
		if err != nil {
			rp.LogDebug(fmt.Sprintf("上传解析后的文本到MinIO失败 (简历 %s): %v", message.SubmissionUUID, err))
			return NewStoreError(message.SubmissionUUID, err.Error())
		}
		rp.LogDebug(fmt.Sprintf("简历 %s 的解析文本已上传到MinIO: %s", message.SubmissionUUID, textObjectKey))

		// 6. 构建并发送到下一个队列的消息 (ResumeProcessingMessage)
		processingMessage := storage_types.ResumeProcessingMessage{
			SubmissionUUID:      message.SubmissionUUID,
			ParsedTextPathOSS:   textObjectKey,
			TargetJobID:         message.TargetJobID,
			ParsedTextObjectKey: textObjectKey, // 兼容旧字段
		}

		err = rp.Storage.RabbitMQ.PublishJSON(
			ctx,
			cfg.RabbitMQ.ProcessingEventsExchange,
			cfg.RabbitMQ.ParsedRoutingKey,
			processingMessage,
			true, // 持久化
		)
		if err != nil {
			rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 发布消息到LLM解析队列失败 for %s: %v", message.SubmissionUUID, err))
			return NewPublishError(message.SubmissionUUID, err.Error())
		}
		rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 成功发布消息到LLM队列 for %s", message.SubmissionUUID))

		// 7. 更新数据库记录，包括解析后的文本路径和MD5，并将状态更新为 QUEUED_FOR_LLM
		// 所有更新在同一事务中执行
		if err := tx.Model(&models.ResumeSubmission{}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			Updates(map[string]interface{}{
				"parsed_text_path_oss": textObjectKey,
				"raw_text_md5":         textMD5Hex,
				"processing_status":    constants.StatusQueuedForLLM,
				"parser_version":       cfg.ActiveParserVersion,
			}).Error; err != nil {
			rp.LogDebug(fmt.Sprintf("更新简历 %s 数据库记录失败: %v", message.SubmissionUUID, err))
			return NewUpdateError(message.SubmissionUUID, "更新数据库失败")
		}

		// 事务回调返回nil，GORM会自动提交事务
		return nil
	})

	// 如果事务执行失败，尝试单独更新状态为失败
	if err != nil {
		// 尝试将状态更新为处理失败，以便后续可以重试
		updateErr := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusUploadProcessingFailed)
		if updateErr != nil {
			rp.LogDebug(fmt.Sprintf("在事务失败后更新状态为失败时出错 (简历 %s): %v", message.SubmissionUUID, updateErr))
			// 记录错误但不覆盖原始错误
		}
		return err // 返回原始错误
	}

	rp.LogDebug(fmt.Sprintf("LLM任务 (简历 %s) 的处理已成功完成。", message.SubmissionUUID))
	return nil
}

// ProcessLLMTasks 接收LLM处理消息，完成分块、评估、存储向量和结果的完整流程
// 使用数据库事务确保所有状态更新的原子性
func (rp *ResumeProcessor) ProcessLLMTasks(ctx context.Context, message storage_types.ResumeProcessingMessage, cfg *config.Config) error {
	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 开始处理LLM任务 for %s, 目标Job ID: %s", message.SubmissionUUID, message.TargetJobID))

	// 0. 从数据库获取最新的 ResumeSubmission 记录
	var submission models.ResumeSubmission
	if err := rp.Storage.MySQL.DB().WithContext(ctx).Where("submission_uuid = ?", message.SubmissionUUID).First(&submission).Error; err != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 获取 ResumeSubmission 记录失败 for %s: %v", message.SubmissionUUID, err)
		return fmt.Errorf("获取 ResumeSubmission 记录失败: %w", err)
	}

	// 检查当前状态，防止重复处理或处理不应处理的任务
	if submission.ProcessingStatus != "QUEUED_FOR_LLM" && submission.ProcessingStatus != "LLM_PROCESSING_FAILED" && submission.ProcessingStatus != "CHUNKING_FAILED" && submission.ProcessingStatus != "VECTORIZATION_FAILED" {
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 跳过处理 for %s, 当前状态为 %s, 不符合LLM处理条件", message.SubmissionUUID, submission.ProcessingStatus))
		return nil // 不是错误，只是当前状态不需要处理
	}

	// 1. 从MinIO下载已解析的文本（无论事务与否都需要先执行）
	parsedText, err := rp.Storage.MinIO.GetParsedText(ctx, message.ParsedTextPathOSS)
	if err != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 从MinIO下载解析文本失败 for %s (path: %s): %v", message.SubmissionUUID, message.ParsedTextPathOSS, err)
		// 不在事务之前使用单独更新，改为记录错误后返回
		return fmt.Errorf("下载解析文本失败: %w", err)
	}
	if parsedText == "" {
		rp.Config.Logger.Printf("ProcessLLMTasks: 下载的解析文本为空 for %s (path: %s)", message.SubmissionUUID, message.ParsedTextPathOSS)
		return fmt.Errorf("下载的解析文本为空")
	}
	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功下载解析文本 for %s. 文本长度: %d", message.SubmissionUUID, len(parsedText)))

	// 2. 使用LLM进行简历分块和基本信息提取（由于LLM调用耗时较长，也放在事务外以降低锁定时间）
	var sections []*types.ResumeSection
	var basicInfo map[string]string
	var chunkErr error

	if rp.ResumeChunker != nil {
		sections, basicInfo, chunkErr = rp.ResumeChunker.ChunkResume(ctx, parsedText)
		if chunkErr != nil {
			rp.Config.Logger.Printf("ProcessLLMTasks: LLM简历分块失败 for %s: %v", message.SubmissionUUID, chunkErr)
			return fmt.Errorf("LLM简历分块失败: %w", chunkErr)
		}
		if len(sections) == 0 {
			rp.Config.Logger.Printf("ProcessLLMTasks: LLM简历分块结果为空 for %s", message.SubmissionUUID)
			return fmt.Errorf("LLM简历分块结果为空")
		}
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] LLM简历分块成功 for %s. 分块数量: %d", message.SubmissionUUID, len(sections)))
	} else {
		rp.Config.Logger.Printf("ProcessLLMTasks: ResumeChunker未初始化 for %s. 跳过分块。", message.SubmissionUUID)
		return fmt.Errorf("ResumeChunker未初始化")
	}

	// 3. 将分块内容嵌入为向量（向量化也可能耗时较长，放在事务外）
	var chunkEmbeddings []*types.ResumeChunkVector
	var embedErr error

	if rp.ResumeEmbedder != nil && sections != nil && len(sections) > 0 {
		chunkEmbeddings, embedErr = rp.ResumeEmbedder.Embed(ctx, sections, basicInfo)
		if embedErr != nil {
			rp.Config.Logger.Printf("ProcessLLMTasks: 向量嵌入失败 for %s: %v", message.SubmissionUUID, embedErr)
			return fmt.Errorf("向量嵌入失败: %w", embedErr)
		}
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 向量嵌入调用完成 for %s. 返回向量组数量: %d (原始分块数量: %d)", message.SubmissionUUID, len(chunkEmbeddings), len(sections)))

		if chunkEmbeddings == nil || len(chunkEmbeddings) != len(sections) {
			rp.Config.Logger.Printf("[ProcessLLMTasks] 向量嵌入数量 (%d) 与原始分块数量 (%d) 不匹配 for %s. 跳过向量存储", len(chunkEmbeddings), len(sections), message.SubmissionUUID)
			return fmt.Errorf("向量嵌入数量与分块数量不匹配: 得到 %d 向量, 期望 %d", len(chunkEmbeddings), len(sections))
		}
	} else if rp.ResumeEmbedder == nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: ResumeEmbedder未初始化 for %s. 跳过向量嵌入。", message.SubmissionUUID)
		return fmt.Errorf("ResumeEmbedder未初始化")
	}

	// 4. 准备向量数据，但暂不存储（等待事务）
	var qdrantChunks []types.ResumeChunk
	var floatEmbeddings [][]float64
	var pointIDs []string

	// 填充向量数据
	if chunkEmbeddings != nil && sections != nil && len(chunkEmbeddings) == len(sections) && len(sections) > 0 {
		qdrantChunks = make([]types.ResumeChunk, len(sections))
		floatEmbeddings = make([][]float64, len(chunkEmbeddings))

		var globalExpYears int
		var globalEduLevel string
		var candidateName, candidatePhone, candidateEmail string

		if basicInfo != nil {
			if expStr, ok := basicInfo["years_of_experience"]; ok {
				if expFloat, err := strconv.ParseFloat(expStr, 64); err == nil {
					globalExpYears = int(expFloat)
				}
			}
			if eduStr, ok := basicInfo["highest_education"]; ok {
				globalEduLevel = eduStr
			}
			candidateName, _ = basicInfo["name"]
			candidatePhone, _ = basicInfo["phone"]
			candidateEmail, _ = basicInfo["email"]
		}

		for i, section := range sections {
			if chunkEmbeddings[i] != nil && chunkEmbeddings[i].Vector != nil {
				floatEmbeddings[i] = chunkEmbeddings[i].Vector
			} else {
				floatEmbeddings[i] = make([]float64, rp.Config.DefaultDimensions)
				rp.Config.Logger.Printf("ProcessLLMTasks: 发现空嵌入向量 at index %d for %s, 使用默认空向量", i, message.SubmissionUUID)
			}

			qdrantChunks[i] = types.ResumeChunk{
				ChunkID:   section.ChunkID,
				ChunkType: string(section.Type),
				Content:   section.Content,
				UniqueIdentifiers: types.Identity{
					Name:  candidateName,
					Phone: candidatePhone,
					Email: candidateEmail,
				},
				Metadata: types.ChunkMetadata{
					ExperienceYears: globalExpYears,
					EducationLevel:  globalEduLevel,
				},
			}
			if chunkEmbeddings[i] != nil && chunkEmbeddings[i].Metadata != nil {
				if specificExp, ok := chunkEmbeddings[i].Metadata["experience_years"].(int); ok {
					qdrantChunks[i].Metadata.ExperienceYears = specificExp
				}
				if specificEdu, ok := chunkEmbeddings[i].Metadata["education_level"].(string); ok {
					qdrantChunks[i].Metadata.EducationLevel = specificEdu
				}
				if score, ok := chunkEmbeddings[i].Metadata["importance_score"].(float32); ok {
					qdrantChunks[i].ImportanceScore = score
				} else if scoreF64, ok := chunkEmbeddings[i].Metadata["importance_score"].(float64); ok {
					qdrantChunks[i].ImportanceScore = float32(scoreF64)
				}
			}
		}
	} else {
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 没有足够的有效向量数据进行存储 for %s.", message.SubmissionUUID))
		return fmt.Errorf("没有足够的有效向量数据")
	}

	// 5. 开始数据库事务 - 使用GORM的Transaction回调模式确保完整性
	err = rp.Storage.MySQL.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 5.1 更新状态为PENDING_LLM (事务中的第一个操作)
		if err := tx.Model(&models.ResumeSubmission{}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			Update("processing_status", "PENDING_LLM").Error; err != nil {
			rp.Config.Logger.Printf("ProcessLLMTasks: 更新状态到 PENDING_LLM 失败 for %s: %v", message.SubmissionUUID, err)
			return err
		}

		// 5.2 存储向量到Qdrant (外部操作，但要在事务内判断成功失败)
		if rp.Storage.Qdrant != nil {
			var storeErr error
			pointIDs, storeErr = rp.Storage.Qdrant.StoreResumeVectors(ctx, message.SubmissionUUID, qdrantChunks, floatEmbeddings)
			if storeErr != nil {
				rp.Config.Logger.Printf("ProcessLLMTasks: 存储向量到Qdrant失败 for %s: %v", message.SubmissionUUID, storeErr)
				return fmt.Errorf("存储向量到Qdrant失败: %w", storeErr)
			}
			rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功存储 %d 个向量到Qdrant for %s", len(pointIDs), message.SubmissionUUID))
		} else {
			rp.Config.Logger.Printf("ProcessLLMTasks: Qdrant存储未初始化 for %s", message.SubmissionUUID)
			return fmt.Errorf("Qdrant存储服务未初始化")
		}

		// 5.3 保存简历分块到MySQL (事务内)
		if err := rp.Storage.MySQL.SaveResumeChunks(tx, message.SubmissionUUID, sections); err != nil {
			rp.Config.Logger.Printf("ProcessLLMTasks: 保存简历分块到MySQL失败 for %s: %v", message.SubmissionUUID, err)
			return fmt.Errorf("保存简历分块到MySQL失败: %w", err)
		}
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功保存 %d 个简历分块信息到MySQL for %s", len(sections), message.SubmissionUUID))

		// 5.4 准备LLM提取的基本信息更新
		updates := make(map[string]interface{})
		if basicInfo != nil {
			// 提取简历标识符 (例如：姓名_电话)
			name, nameOk := basicInfo["name"]
			phone, phoneOk := basicInfo["phone"]
			identifier := ""
			if nameOk && name != "" && phoneOk && phone != "" {
				identifier = fmt.Sprintf("%s_%s", name, phone)
			} else if nameOk && name != "" {
				identifier = name
			} else if phoneOk && phone != "" {
				identifier = phone
			}

			if identifier != "" {
				updates["llm_resume_identifier"] = identifier
			}

			// 将basicInfo转换为JSON字符串存储
			basicInfoJSON, jsonErr := json.Marshal(basicInfo)
			if jsonErr == nil {
				updates["llm_parsed_basic_info"] = basicInfoJSON
			}
		}

		// 5.5 添加状态更新和POINT_ID关联
		updates["processing_status"] = "LLM_PROCESSED" // 最终状态
		if len(pointIDs) > 0 {
			pointIDsJSON, err := json.Marshal(pointIDs)
			if err == nil {
				updates["qdrant_point_ids"] = pointIDsJSON
			}
		}

		// 5.6 一次性更新所有字段（事务内）
		if err := tx.Model(&models.ResumeSubmission{}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			Updates(updates).Error; err != nil {
			rp.Config.Logger.Printf("ProcessLLMTasks: 最终更新数据库失败 for %s: %v", message.SubmissionUUID, err)
			return fmt.Errorf("最终更新数据库失败: %w", err)
		}

		// 5.7 处理与目标岗位匹配（如果有）
		if message.TargetJobID != "" && rp.MatchEvaluator != nil {
			// 从数据库获取JD文本 (未来可以考虑从缓存获取)
			job, err := rp.Storage.MySQL.GetJobByID(rp.Storage.MySQL.DB().WithContext(ctx), message.TargetJobID)
			if err != nil {
				rp.Config.Logger.Printf("ProcessLLMTasks: 获取JD信息失败 for JobID %s, Submission %s: %v", message.TargetJobID, message.SubmissionUUID, err)
				return fmt.Errorf("获取JD信息失败: %w", err)
			} else if job.JobDescriptionText == "" {
				rp.Config.Logger.Printf("ProcessLLMTasks: JD文本为空 for JobID %s, Submission %s", message.TargetJobID, message.SubmissionUUID)
				return fmt.Errorf("JD文本为空")
			} else {
				rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功获取JD文本 for JobID %s. JD长度: %d", message.TargetJobID, len(job.JobDescriptionText)))
				evaluation, evalErr := rp.MatchEvaluator.EvaluateMatch(ctx, job.JobDescriptionText, parsedText) // 使用完整的解析文本进行匹配
				if evalErr != nil {
					rp.Config.Logger.Printf("ProcessLLMTasks: JD匹配评估失败 for JobID %s, Submission %s: %v", message.TargetJobID, message.SubmissionUUID, evalErr)
					return fmt.Errorf("JD匹配评估失败: %w", evalErr)
				} else {
					rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] JD匹配评估成功 for JobID %s, Submission %s. Score: %d", message.TargetJobID, message.SubmissionUUID, evaluation.MatchScore))
					// 保存匹配结果到数据库
					matchHighlightsJSON, _ := json.Marshal(evaluation.MatchHighlights)
					potentialGapsJSON, _ := json.Marshal(evaluation.PotentialGaps)

					matchRecord := models.JobSubmissionMatch{
						SubmissionUUID:         message.SubmissionUUID,
						JobID:                  message.TargetJobID,
						LLMMatchScore:          &evaluation.MatchScore,
						LLMMatchHighlightsJSON: matchHighlightsJSON,
						LLMPotentialGapsJSON:   potentialGapsJSON,
						LLMResumeSummaryForJD:  evaluation.ResumeSummaryForJD,
						EvaluationStatus:       "EVALUATED", // 假设评估成功即为EVALUATED
						EvaluatedAt:            utils.TimePtr(time.Now()),
					}
					if err := rp.Storage.MySQL.CreateJobSubmissionMatch(rp.Storage.MySQL.DB().WithContext(ctx), &matchRecord); err != nil {
						rp.Config.Logger.Printf("ProcessLLMTasks: 保存JD匹配结果失败 for JobID %s, Submission %s: %v", message.TargetJobID, message.SubmissionUUID, err)
						// 匹配结果保存失败，但不一定意味着整个LLM处理失败，可能只是这部分失败
						// 考虑是否需要一个特定的状态，如 MATCH_SAVE_FAILED
						return fmt.Errorf("保存JD匹配结果失败: %w", err)
					} else {
						rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功保存JD匹配结果 for JobID %s, Submission %s", message.TargetJobID, message.SubmissionUUID))
					}
				}
			}
		}

		// 事务成功，返回nil表示提交
		return nil
	})

	// 如果事务执行失败，尝试单独更新状态为失败
	if err != nil {
		// 尝试将状态更新为处理失败，以便后续可以重试
		updateErr := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "LLM_PROCESSING_FAILED")
		if updateErr != nil {
			rp.Config.Logger.Printf("在事务失败后更新状态为失败时出错 (简历 %s): %v", message.SubmissionUUID, updateErr)
			// 记录错误但不覆盖原始错误
		}
		return err // 返回原始错误
	}

	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功完成LLM处理流程 for %s", message.SubmissionUUID))
	return nil
}

// Helper to create processor options easily
// WithPDFExtractor sets the PDFExtractor.
// ... existing code ...

// CreateProcessor 便捷工厂函数，用于创建组件和设置并构造处理器
// 适用于在代码中需要显式地创建特定组件配置的场景
func CreateProcessor(ctx context.Context, compOpts []ComponentOpt, setOpts []SettingOpt) (*ResumeProcessor, error) {
	// 创建默认组件
	components := &Components{}

	// 创建默认设置
	settings := &Settings{
		UseLLM:            true,
		DefaultDimensions: 1536,
		Debug:             false,
		Logger:            log.New(os.Stdout, "[Processor] ", log.LstdFlags),
		TimeLocation:      time.Local,
	}

	// 应用组件选项
	for _, opt := range compOpts {
		opt(components)
	}

	// 应用设置选项
	for _, opt := range setOpts {
		opt(settings)
	}

	// 验证必要组件
	if components.PDFExtractor == nil {
		return nil, fmt.Errorf("必须提供PDF提取器组件")
	}

	// 创建处理器
	return NewResumeProcessorV2(components, settings), nil
}
