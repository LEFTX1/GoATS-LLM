package processor

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	parser2 "ai-agent-go/internal/parser"
	"ai-agent-go/internal/storage"
	storagetypes "ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"ai-agent-go/pkg/utils"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"gorm.io/gorm/clause"
)

var ErrDuplicateContent = errors.New("duplicate content detected")

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
func (rp *ResumeProcessor) LogDebug(message string) {
	if rp.Config.Debug && rp.Config.Logger != nil {
		rp.Config.Logger.Println(message)
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
func (rp *ResumeProcessor) ProcessUploadedResume(ctx context.Context, message storagetypes.ResumeUploadMessage, cfg *config.Config) error {
	if rp.Storage == nil {
		return fmt.Errorf("ResumeProcessor: Storage is not initialized")
	}
	if rp.PDFExtractor == nil {
		return fmt.Errorf("ResumeProcessor: PDFExtractor is not initialized")
	}

	err := rp.Storage.MySQL.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 更新初始状态为 PENDING_PARSING
		if err := tx.Model(&models.ResumeSubmission{}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			Update("processing_status", constants.StatusPendingParsing).Error; err != nil {
			rp.LogDebug(fmt.Sprintf("更新简历 %s 状态为 %s 失败: %v", message.SubmissionUUID, constants.StatusPendingParsing, err))
			return NewUpdateError(message.SubmissionUUID, fmt.Sprintf("更新状态为%s失败", constants.StatusPendingParsing))
		}

		// 2. 解析并去重
		text, textMD5Hex, err := rp._parseAndDeduplicateResume(ctx, tx, message)
		if err != nil {
			if errors.Is(err, ErrDuplicateContent) {
				return nil // 内容重复是正常流程，提交状态更新
			}
			return err // 其他错误则回滚
		}

		// 3. 上传解析后的文本到MinIO
		textObjectKey, err := rp.Storage.MinIO.UploadParsedText(ctx, message.SubmissionUUID, text)
		if err != nil {
			rp.LogDebug(fmt.Sprintf("上传解析后的文本到MinIO失败 (简历 %s): %v", message.SubmissionUUID, err))
			return NewStoreError(message.SubmissionUUID, err.Error())
		}
		rp.LogDebug(fmt.Sprintf("简历 %s 的解析文本已上传到MinIO: %s", message.SubmissionUUID, textObjectKey))

		// 4. 构建下一个队列的消息
		processingMessage := storagetypes.ResumeProcessingMessage{
			SubmissionUUID:      message.SubmissionUUID,
			ParsedTextPathOSS:   textObjectKey,
			TargetJobID:         message.TargetJobID,
			ParsedTextObjectKey: textObjectKey, // 兼容旧字段
		}

		// 5. [Outbox] 将消息写入 Outbox 表，而不是直接发布
		payloadBytes, err := json.Marshal(processingMessage)
		if err != nil {
			rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 序列化 outbox payload 失败 for %s: %v", message.SubmissionUUID, err))
			return NewUpdateError(message.SubmissionUUID, "序列化 outbox payload 失败")
		}

		outboxEntry := models.OutboxMessage{
			AggregateID:      message.SubmissionUUID,
			EventType:        "resume.parsed",
			Payload:          string(payloadBytes),
			TargetExchange:   cfg.RabbitMQ.ProcessingEventsExchange,
			TargetRoutingKey: cfg.RabbitMQ.ParsedRoutingKey,
		}

		if err := tx.Create(&outboxEntry).Error; err != nil {
			rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 插入 outbox 记录失败 for %s: %v", message.SubmissionUUID, err))
			return NewUpdateError(message.SubmissionUUID, "插入 outbox 记录失败")
		}
		rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 成功为 %s 创建 outbox 记录", message.SubmissionUUID))

		// 6. 更新数据库记录
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

		return nil
	})

	if err != nil {
		updateErr := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusUploadProcessingFailed)
		if updateErr != nil {
			rp.LogDebug(fmt.Sprintf("在事务失败后更新状态为失败时出错 (简历 %s): %v", message.SubmissionUUID, updateErr))
		}
		return err
	}

	rp.LogDebug(fmt.Sprintf("上传任务 (简历 %s) 的处理已成功完成。", message.SubmissionUUID))
	return nil
}

// _parseAndDeduplicateResume an internal helper to extract text, check for duplicates, and return the text and its MD5.
// It returns a special ErrDuplicateContent if the text content is a duplicate.
func (rp *ResumeProcessor) _parseAndDeduplicateResume(ctx context.Context, tx *gorm.DB, message storagetypes.ResumeUploadMessage) (string, string, error) {
	originalFileBytes, err := rp.Storage.MinIO.GetResumeFile(ctx, message.OriginalFilePathOSS)
	if err != nil {
		rp.LogDebug(fmt.Sprintf("从MinIO下载简历 %s 失败: %v", message.SubmissionUUID, err))
		return "", "", NewDownloadError(message.SubmissionUUID, err.Error())
	}
	rp.LogDebug(fmt.Sprintf("简历 %s 从MinIO下载成功，大小: %d bytes", message.SubmissionUUID, len(originalFileBytes)))

	text, _, err := rp.PDFExtractor.ExtractTextFromReader(ctx, bytes.NewReader(originalFileBytes), message.OriginalFilePathOSS, nil)
	if err != nil {
		rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 提取简历文本失败 for %s: %v", message.SubmissionUUID, err))
		return "", "", NewParseError(message.SubmissionUUID, err.Error())
	}
	rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 成功提取文本 for %s, 长度: %d", message.SubmissionUUID, len(text)))

	textMD5Hex := utils.CalculateMD5([]byte(text))
	rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 计算得到文本MD5 %s for %s", textMD5Hex, message.SubmissionUUID))

	textExists, err := rp.Storage.Redis.CheckAndAddParsedTextMD5(ctx, textMD5Hex)
	if err != nil {
		rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 使用Redis原子操作检查文本MD5失败 for %s: %v, 将继续处理，但文本去重可能失效", message.SubmissionUUID, err))
	} else if textExists {
		rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 检测到重复的文本MD5 %s for %s，标记为重复内容", textMD5Hex, message.SubmissionUUID))
		if err := tx.Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", message.SubmissionUUID).Update("processing_status", constants.StatusContentDuplicateSkipped).Error; err != nil {
			return "", "", NewUpdateError(message.SubmissionUUID, "更新重复内容状态失败")
		}
		return "", "", ErrDuplicateContent
	}
	rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 文本MD5 %s 不存在于Redis, 继续处理 for %s", textMD5Hex, message.SubmissionUUID))
	return text, textMD5Hex, nil
}

// ProcessLLMTasks 接收LLM处理消息，完成分块、评估、存储向量和结果的完整流程
// 使用数据库事务确保所有状态更新的原子性
func (rp *ResumeProcessor) ProcessLLMTasks(ctx context.Context, message storagetypes.ResumeProcessingMessage, cfg *config.Config) error {
	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 开始处理LLM任务 for %s, 目标Job ID: %s", message.SubmissionUUID, message.TargetJobID))

	// 使用事务来保证读取-更新的原子性和幂等性
	err := rp.Storage.MySQL.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 获取最新的 ResumeSubmission 记录并锁定，防止并发处理
		var submission models.ResumeSubmission
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			First(&submission).Error; err != nil {

			if errors.Is(err, gorm.ErrRecordNotFound) {
				rp.Config.Logger.Printf("ProcessLLMTasks: ResumeSubmission 记录未找到 for %s: %v", message.SubmissionUUID, err)
				// 记录不存在，可能已被删除，直接确认消息
				return nil
			}
			rp.Config.Logger.Printf("ProcessLLMTasks: 获取 ResumeSubmission 记录失败 for %s: %v", message.SubmissionUUID, err)
			return fmt.Errorf("获取 ResumeSubmission 记录失败: %w", err)
		}

		// 2. 关键的幂等性检查
		// 只有处于这些状态才应该被处理
		allowedStatuses := map[string]bool{
			constants.StatusQueuedForLLM:           true,
			constants.StatusLLMProcessingFailed:    true,
			constants.StatusChunkingFailed:         true,
			constants.StatusVectorizationFailed:    true,
			constants.StatusQdrantStoreFailed:      true, // 新增可重试状态
			constants.StatusUploadProcessingFailed: true, // 也可能是入口状态
		}
		if !allowedStatuses[submission.ProcessingStatus] {
			rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 跳过重复/无效状态的消息 for %s, 当前状态: %s", message.SubmissionUUID, submission.ProcessingStatus))
			return nil // 状态不匹配，说明是重复消息或已处理，直接确认并返回
		}

		// 3. 更新状态为 PENDING_LLM，表示开始处理
		if err := tx.Model(&submission).Update("processing_status", constants.StatusPendingLLM).Error; err != nil {
			rp.Config.Logger.Printf("ProcessLLMTasks: 更新状态到 PENDING_LLM 失败 for %s: %v", message.SubmissionUUID, err)
			return err
		}

		// ---- 以下为非事务性操作 ----
		// 下载、分块、向量化等CPU密集或I/O密集操作在事务外执行，以减少锁持有时间
		// 这里的设计做一个调整：将这些操作放在事务之前，如果失败了，就不开启事务

		// --- 事务外执行 ---
		parsedText, sections, basicInfo, err := rp._downloadAndChunkResume(ctx, message)
		if err != nil {
			// 如果这些步骤失败，需要更新数据库状态为失败
			updateErr := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusChunkingFailed)
			if updateErr != nil {
				rp.Config.Logger.Printf("更新状态为 CHUNKING_FAILED 时出错 for %s: %v", message.SubmissionUUID, updateErr)
			}
			return err // 返回错误，消息将被nack
		}

		chunkEmbeddings, err := rp._embedChunks(ctx, sections, basicInfo)
		if err != nil {
			updateErr := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusVectorizationFailed)
			if updateErr != nil {
				rp.Config.Logger.Printf("更新状态为 VECTORIZATION_FAILED 时出错 for %s: %v", message.SubmissionUUID, updateErr)
			}
			return err // 返回错误，消息将被nack
		}

		qdrantChunks, floatEmbeddings, err := rp._prepareQdrantData(message.SubmissionUUID, sections, basicInfo, chunkEmbeddings)
		if err != nil {
			return err
		}

		// --- 重新进入事务，执行数据库写操作 ---
		return rp._executeLLMProcessingTransaction(ctx, tx, message, qdrantChunks, floatEmbeddings, sections, basicInfo, parsedText)
	})

	if err != nil {
		// 事务外的失败或事务提交失败
		// 最终失败状态由具体的失败点决定，这里只记录通用失败
		rp.Config.Logger.Printf("[ProcessLLMTasks] LLM处理流程最终失败 for %s: %v", message.SubmissionUUID, err)
		// 最终的失败状态更新，如果之前的步骤没有成功更新的话
		finalErrUpdate := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusLLMProcessingFailed)
		if finalErrUpdate != nil {
			rp.Config.Logger.Printf("在最终更新状态为 LLM_PROCESSING_FAILED 时出错 for %s: %v", message.SubmissionUUID, finalErrUpdate)
		}
		return err
	}

	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功完成LLM处理流程 for %s", message.SubmissionUUID))
	return nil
}

func (rp *ResumeProcessor) _downloadAndChunkResume(ctx context.Context, message storagetypes.ResumeProcessingMessage) (string, []*types.ResumeSection, map[string]string, error) {
	parsedText, err := rp.Storage.MinIO.GetParsedText(ctx, message.ParsedTextPathOSS)
	if err != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 从MinIO下载解析文本失败 for %s (path: %s): %v", message.SubmissionUUID, message.ParsedTextPathOSS, err)
		return "", nil, nil, fmt.Errorf("下载解析文本失败: %w", err)
	}
	if parsedText == "" {
		rp.Config.Logger.Printf("ProcessLLMTasks: 下载的解析文本为空 for %s (path: %s)", message.SubmissionUUID, message.ParsedTextPathOSS)
		return "", nil, nil, fmt.Errorf("下载的解析文本为空")
	}
	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功下载解析文本 for %s. 文本长度: %d", message.SubmissionUUID, len(parsedText)))

	if rp.ResumeChunker == nil {
		return "", nil, nil, fmt.Errorf("ResumeChunker未初始化")
	}
	sections, basicInfo, chunkErr := rp.ResumeChunker.ChunkResume(ctx, parsedText)
	if chunkErr != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: LLM简历分块失败 for %s: %v", message.SubmissionUUID, chunkErr)
		return "", nil, nil, fmt.Errorf("LLM简历分块失败: %w", chunkErr)
	}
	if len(sections) == 0 {
		rp.Config.Logger.Printf("ProcessLLMTasks: LLM简历分块结果为空 for %s", message.SubmissionUUID)
		return "", nil, nil, fmt.Errorf("LLM简历分块结果为空")
	}
	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] LLM简历分块成功 for %s. 分块数量: %d", message.SubmissionUUID, len(sections)))
	return parsedText, sections, basicInfo, nil
}

func (rp *ResumeProcessor) _embedChunks(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error) {
	if rp.ResumeEmbedder == nil {
		return nil, fmt.Errorf("ResumeEmbedder未初始化")
	}
	chunkEmbeddings, embedErr := rp.ResumeEmbedder.Embed(ctx, sections, basicInfo)
	if embedErr != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 向量嵌入失败: %v", embedErr)
		return nil, fmt.Errorf("向量嵌入失败: %w", embedErr)
	}
	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 向量嵌入调用完成. 返回向量组数量: %d", len(chunkEmbeddings)))
	if len(chunkEmbeddings) != len(sections) {
		return nil, fmt.Errorf("向量嵌入数量(%d)与分块数量(%d)不匹配", len(chunkEmbeddings), len(sections))
	}
	return chunkEmbeddings, nil
}

func (rp *ResumeProcessor) _executeLLMProcessingTransaction(ctx context.Context, tx *gorm.DB, message storagetypes.ResumeProcessingMessage, qdrantChunks []types.ResumeChunk, floatEmbeddings [][]float64, sections []*types.ResumeSection, basicInfo map[string]string, parsedText string) error {
	// 事务开始时，状态已经被更新为 PENDING_LLM，这里不再重复更新
	// 1. 存储向量到Qdrant (这是一个外部I/O，理想情况下应该在事务之外，但如果失败需要回滚数据库状态，放在事务内简化处理)
	var pointIDs []string
	if rp.Storage.Qdrant != nil {
		var storeErr error
		pointIDs, storeErr = rp.Storage.Qdrant.StoreResumeVectors(ctx, message.SubmissionUUID, qdrantChunks, floatEmbeddings)
		if storeErr != nil {
			rp.Config.Logger.Printf("ProcessLLMTasks: 存储向量到Qdrant失败 for %s: %v", message.SubmissionUUID, storeErr)
			// 更新状态为失败
			tx.Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", message.SubmissionUUID).Update("processing_status", constants.StatusQdrantStoreFailed)
			return fmt.Errorf("存储向量到Qdrant失败: %w", storeErr)
		}
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功存储 %d 个向量到Qdrant for %s", len(pointIDs), message.SubmissionUUID))
	} else {
		return fmt.Errorf("qdrant存储服务未初始化")
	}

	// 2. 保存简历分块到MySQL
	if err := rp.Storage.MySQL.SaveResumeChunks(tx, message.SubmissionUUID, sections, pointIDs); err != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 保存简历分块到MySQL失败 for %s: %v", message.SubmissionUUID, err)
		return fmt.Errorf("保存简历分块到MySQL失败: %w", err)
	}
	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功保存 %d 个简历分块信息到MySQL for %s", len(sections), message.SubmissionUUID))

	// 3. 准备并执行最终的数据库更新
	updates := rp._prepareFinalDBUpdates(basicInfo, pointIDs)
	if err := tx.Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", message.SubmissionUUID).Updates(updates).Error; err != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 最终更新数据库失败 for %s: %v", message.SubmissionUUID, err)
		return fmt.Errorf("最终更新数据库失败: %w", err)
	}

	// 4. 处理与目标岗位匹配
	err := rp._saveMatchEvaluation(ctx, tx, message, parsedText)
	if err != nil {
		// 匹配评估失败，更新状态并返回错误
		tx.Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", message.SubmissionUUID).Update("processing_status", constants.StatusMatchFailed)
		return err
	}

	// 5. 所有步骤成功，更新为最终成功状态
	if err := tx.Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", message.SubmissionUUID).Update("processing_status", constants.StatusProcessingCompleted).Error; err != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 更新最终成功状态失败 for %s: %v", message.SubmissionUUID, err)
		return fmt.Errorf("更新最终成功状态失败: %w", err)
	}

	return nil
}

func (rp *ResumeProcessor) _prepareFinalDBUpdates(basicInfo map[string]string, pointIDs []string) map[string]interface{} {
	updates := make(map[string]interface{})
	if basicInfo != nil {
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
		basicInfoJSON, jsonErr := json.Marshal(basicInfo)
		if jsonErr == nil {
			updates["llm_parsed_basic_info"] = basicInfoJSON
		}
	}
	// 不在这里更新状态，由 _executeLLMProcessingTransaction 控制
	// updates["processing_status"] = "LLM_PROCESSED"
	if len(pointIDs) > 0 {
		pointIDsJSON, err := json.Marshal(pointIDs)
		if err == nil {
			updates["qdrant_point_ids"] = pointIDsJSON
		}
	}
	return updates
}

// _prepareQdrantData prepares the data structures needed for Qdrant storage.
func (rp *ResumeProcessor) _prepareQdrantData(submissionUUID string, sections []*types.ResumeSection, basicInfo map[string]string, chunkEmbeddings []*types.ResumeChunkVector) ([]types.ResumeChunk, [][]float64, error) {
	if len(chunkEmbeddings) == 0 {
		return nil, nil, fmt.Errorf("没有有效的向量数据")
	}

	qdrantChunks := make([]types.ResumeChunk, len(sections))
	floatEmbeddings := make([][]float64, len(chunkEmbeddings))

	var globalExpYears int
	var globalEduLevel, candidateName, candidatePhone, candidateEmail string

	if basicInfo != nil {
		if expStr, ok := basicInfo["years_of_experience"]; ok {
			if expFloat, err := strconv.ParseFloat(expStr, 64); err == nil {
				globalExpYears = int(expFloat)
			}
		}
		globalEduLevel, _ = basicInfo["highest_education"]
		candidateName, _ = basicInfo["name"]
		candidatePhone, _ = basicInfo["phone"]
		candidateEmail, _ = basicInfo["email"]
	}

	for i, section := range sections {
		if i < len(chunkEmbeddings) && chunkEmbeddings[i] != nil && chunkEmbeddings[i].Vector != nil {
			floatEmbeddings[i] = chunkEmbeddings[i].Vector
		} else {
			floatEmbeddings[i] = make([]float64, rp.Config.DefaultDimensions)
			rp.Config.Logger.Printf("ProcessLLMTasks: 发现空嵌入向量 at index %d for %s, 使用默认空向量", i, submissionUUID)
		}

		qdrantChunks[i] = types.ResumeChunk{
			ChunkID:           section.ChunkID,
			ChunkType:         string(section.Type),
			Content:           section.Content,
			UniqueIdentifiers: types.Identity{Name: candidateName, Phone: candidatePhone, Email: candidateEmail},
			Metadata:          types.ChunkMetadata{ExperienceYears: globalExpYears, EducationLevel: globalEduLevel},
		}

		if i < len(chunkEmbeddings) && chunkEmbeddings[i] != nil && chunkEmbeddings[i].Metadata != nil {
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
	return qdrantChunks, floatEmbeddings, nil
}

// _saveMatchEvaluation handles the logic for fetching JD, evaluating match, and saving the result.
func (rp *ResumeProcessor) _saveMatchEvaluation(ctx context.Context, tx *gorm.DB, message storagetypes.ResumeProcessingMessage, parsedText string) error {
	if message.TargetJobID == "" || rp.MatchEvaluator == nil {
		return nil // Nothing to do if there's no target job or evaluator.
	}

	job, err := rp.Storage.MySQL.GetJobByID(tx.WithContext(ctx), message.TargetJobID)
	if err != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 获取JD信息失败 for JobID %s, Submission %s: %v", message.TargetJobID, message.SubmissionUUID, err)
		return fmt.Errorf("获取JD信息失败: %w", err)
	}
	if job.JobDescriptionText == "" {
		rp.Config.Logger.Printf("ProcessLLMTasks: JD文本为空 for JobID %s, Submission %s", message.TargetJobID, message.SubmissionUUID)
		return fmt.Errorf("JD文本为空")
	}

	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功获取JD文本 for JobID %s. JD长度: %d", message.TargetJobID, len(job.JobDescriptionText)))
	evaluation, evalErr := rp.MatchEvaluator.EvaluateMatch(ctx, job.JobDescriptionText, parsedText)
	if evalErr != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: JD匹配评估失败 for JobID %s, Submission %s: %v", message.TargetJobID, message.SubmissionUUID, evalErr)
		return fmt.Errorf("JD匹配评估失败: %w", evalErr)
	}

	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] JD匹配评估成功 for JobID %s, Submission %s. Score: %d", message.TargetJobID, message.SubmissionUUID, evaluation.MatchScore))
	matchHighlightsJSON, _ := json.Marshal(evaluation.MatchHighlights)
	potentialGapsJSON, _ := json.Marshal(evaluation.PotentialGaps)

	matchRecord := models.JobSubmissionMatch{
		SubmissionUUID:         message.SubmissionUUID,
		JobID:                  message.TargetJobID,
		LLMMatchScore:          &evaluation.MatchScore,
		LLMMatchHighlightsJSON: matchHighlightsJSON,
		LLMPotentialGapsJSON:   potentialGapsJSON,
		LLMResumeSummaryForJD:  evaluation.ResumeSummaryForJD,
		EvaluationStatus:       "EVALUATED",
		EvaluatedAt:            utils.TimePtr(time.Now()),
	}
	if err := rp.Storage.MySQL.CreateJobSubmissionMatch(tx.WithContext(ctx), &matchRecord); err != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 保存JD匹配结果失败 for JobID %s, Submission %s: %v", message.TargetJobID, message.SubmissionUUID, err)
		return fmt.Errorf("保存JD匹配结果失败: %w", err)
	}

	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功保存JD匹配结果 for JobID %s, Submission %s", message.TargetJobID, message.SubmissionUUID))
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
