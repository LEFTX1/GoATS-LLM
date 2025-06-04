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
)

// ResumeProcessor 简历处理组件聚合类
// 不再控制处理流程，仅提供组件集合
type ResumeProcessor struct {
	// 核心组件接口
	PDFExtractor   PDFExtractor      // PDF文本提取接口
	ResumeChunker  ResumeChunker     // 简历分块接口
	ResumeEmbedder ResumeEmbedder    // 简历嵌入接口
	MatchEvaluator JobMatchEvaluator // 岗位匹配评估接口

	// 新增: 存储层依赖
	Storage *storage.Storage

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

// 工厂方法 - 创建处理器的辅助函数

// CreateDefaultProcessor 创建默认配置的简历处理器组件集合
func CreateDefaultProcessor(ctx context.Context, cfg *config.Config, storageManager *storage.Storage) (*ResumeProcessor, error) {
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

	// 创建mock LLM模型
	mockLLM := &MockLLMModel{}

	// 创建LLM分块器
	// 为CreateDefaultProcessor中的组件创建丢弃型 logger
	discardLogger := log.New(io.Discard, "", 0)
	llmChunker := parser2.NewLLMResumeChunker(mockLLM, discardLogger) // 传递 discardLogger

	// 创建处理器
	processor := NewResumeProcessor(
		WithPDFExtractor(pdfExtractor),
		WithResumeChunker(llmChunker),
		WithDebugMode(true),
		WithStorage(storageManager),
	)

	return processor, nil
}

// CreateProcessorFromConfig 从配置创建处理器组件集合
func CreateProcessorFromConfig(ctx context.Context, cfg *config.Config, storageManager *storage.Storage) (*ResumeProcessor, error) {
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
		WithStorage(storageManager),
	)

	// 如果配置了API密钥，添加LLM功能
	if cfg.Aliyun.APIKey != "" {
		// 这里可以创建LLM模型、分块器、嵌入器和评估器
		processor.Config.Logger.Println("检测到API密钥，准备配置LLM功能")
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
func (rp *ResumeProcessor) ProcessUploadedResume(ctx context.Context, message storage_types.ResumeUploadMessage, cfg *config.Config) error {
	if rp.Storage == nil {
		return fmt.Errorf("ResumeProcessor: Storage is not initialized")
	}
	if rp.PDFExtractor == nil {
		return fmt.Errorf("ResumeProcessor: PDFExtractor is not initialized")
	}

	// 1. 更新初始状态为 PENDING_PARSING
	err := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusPendingParsing)
	if err != nil {
		rp.LogDebug(fmt.Sprintf("更新简历 %s 状态为 %s 失败: %v", message.SubmissionUUID, constants.StatusPendingParsing, err))
		// 即使状态更新失败，也尝试继续处理，但这是一个潜在问题点
	}

	// 2. 从MinIO下载原始简历文件
	originalFileBytes, err := rp.Storage.MinIO.GetResumeFile(ctx, message.OriginalFilePathOSS)
	if err != nil {
		rp.LogDebug(fmt.Sprintf("从MinIO下载简历 %s 失败: %v", message.SubmissionUUID, err))
		rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusUploadProcessingFailed)
		return fmt.Errorf("下载简历失败: %w", err)
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
		rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusUploadProcessingFailed)
		return fmt.Errorf("提取简历文本失败: %w", err)
	}
	rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 成功提取文本 for %s, 长度: %d", message.SubmissionUUID, len(text)))

	// 4. 计算提取文本的MD5并进行去重检查
	textMD5Hex := utils.CalculateMD5([]byte(text))
	rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 计算得到文本MD5 %s for %s", textMD5Hex, message.SubmissionUUID))

	textExists, err := rp.Storage.Redis.CheckParsedTextMD5Exists(ctx, textMD5Hex)
	if err != nil {
		rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 查询Redis文本MD5 Set失败 for %s: %v, 将继续处理，但文本去重可能失效", message.SubmissionUUID, err))
		// 如果Redis查询失败，选择继续处理而不是阻塞流程，但文本去重将不起作用
	} else if textExists {
		rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 检测到重复的文本MD5 %s for %s，跳过后续LLM处理", textMD5Hex, message.SubmissionUUID))
		if err := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusContentDuplicateSkipped); err != nil {
			rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 更新状态为CONTENT_DUPLICATE_SKIPPED失败 for %s: %v", message.SubmissionUUID, err))
		}
		return nil // 内容重复，处理完成
	}
	rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 文本MD5 %s 不存在于Redis, 继续处理 for %s", textMD5Hex, message.SubmissionUUID))

	// 5. 如果不重复，则将新的文本MD5添加到Redis集合
	if err := rp.Storage.Redis.AddParsedTextMD5(ctx, textMD5Hex); err != nil {
		rp.LogDebug(fmt.Sprintf("添加文本MD5到Redis失败 (简历 %s): %v", message.SubmissionUUID, err))
		// 非致命错误，继续处理
	}

	// 6. 上传解析后的文本到MinIO
	textObjectKey, err := rp.Storage.MinIO.UploadParsedText(ctx, message.SubmissionUUID, text)
	if err != nil {
		rp.LogDebug(fmt.Sprintf("上传解析后的文本到MinIO失败 (简历 %s): %v", message.SubmissionUUID, err))
		rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusUploadProcessingFailed)
		return fmt.Errorf("上传解析文本失败: %w", err)
	}
	rp.LogDebug(fmt.Sprintf("简历 %s 的解析文本已上传到MinIO: %s", message.SubmissionUUID, textObjectKey))

	// 7. 构建并发送到下一个队列的消息 (ResumeProcessingMessage)
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
		return fmt.Errorf("发布消息到LLM解析队列失败: %w", err)
	}
	rp.LogDebug(fmt.Sprintf("ProcessUploadedResume: 成功发布消息到LLM队列 for %s", message.SubmissionUUID))

	// 8. 更新数据库记录，包括解析后的文本路径和MD5，并将状态更新为 QUEUED_FOR_LLM
	updates := map[string]interface{}{
		"parsed_text_path_oss": textObjectKey, // Corrected: Use the object key from MinIO upload
		"raw_text_md5":         textMD5Hex,
		"processing_status":    constants.StatusQueuedForLLM,
		"parser_version":       cfg.ActiveParserVersion,
	}
	if err := rp.Storage.MySQL.UpdateResumeSubmissionFields(rp.Storage.MySQL.DB().WithContext(ctx), message.SubmissionUUID, updates); err != nil {
		rp.LogDebug(fmt.Sprintf("更新简历 %s 数据库记录失败: %v", message.SubmissionUUID, err))
		// 状态更新失败，但文件已处理，这是一个需要关注的问题
		// 尝试回滚事务或采取其他补偿措施可能是必要的，但当前仅记录
		return fmt.Errorf("更新数据库失败: %w", err)
	}

	// 提交事务
	if err := rp.Storage.MySQL.DB().WithContext(ctx).Commit().Error; err != nil {
		rp.LogDebug(fmt.Sprintf("提交数据库事务失败 (简历 %s): %v", message.SubmissionUUID, err))
		rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusUploadProcessingFailed)
		return fmt.Errorf("提交事务失败: %w", err)
	}

	rp.LogDebug(fmt.Sprintf("LLM任务 (简历 %s) 的goroutine已成功完成处理。", message.SubmissionUUID))
	return nil
}

// ProcessLLMTasks 接收LLM处理消息，完成分块、评估、存储向量和结果的完整流程
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

	// 更新状态为 PENDING_LLM
	if err := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "PENDING_LLM"); err != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 更新状态到 PENDING_LLM 失败 for %s: %v", message.SubmissionUUID, err)
		// 继续处理，但记录错误
	}

	// 1. 从MinIO下载已解析的文本
	parsedText, err := rp.Storage.MinIO.GetParsedText(ctx, message.ParsedTextPathOSS)
	if err != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 从MinIO下载解析文本失败 for %s (path: %s): %v", message.SubmissionUUID, message.ParsedTextPathOSS, err)
		rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "LLM_PROCESSING_FAILED")
		return fmt.Errorf("下载解析文本失败: %w", err)
	}
	if parsedText == "" {
		rp.Config.Logger.Printf("ProcessLLMTasks: 下载的解析文本为空 for %s (path: %s)", message.SubmissionUUID, message.ParsedTextPathOSS)
		rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "LLM_PROCESSING_FAILED")
		return fmt.Errorf("下载的解析文本为空")
	}
	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功下载解析文本 for %s. 文本长度: %d", message.SubmissionUUID, len(parsedText)))

	// 2. 使用LLM进行简历分块和基本信息提取
	var sections []*types.ResumeSection
	var basicInfo map[string]string
	var chunkErr error

	if rp.ResumeChunker != nil {
		sections, basicInfo, chunkErr = rp.ResumeChunker.ChunkResume(ctx, parsedText)
		if chunkErr != nil {
			rp.Config.Logger.Printf("ProcessLLMTasks: LLM简历分块失败 for %s: %v", message.SubmissionUUID, chunkErr)
			rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "CHUNKING_FAILED")
			return fmt.Errorf("LLM简历分块失败: %w", chunkErr)
		}
		if len(sections) == 0 {
			rp.Config.Logger.Printf("ProcessLLMTasks: LLM简历分块结果为空 for %s", message.SubmissionUUID)
			// 即使分块为空，也可能提取出了一些basicInfo，可以尝试继续处理或标记为特定错误
			// 暂时标记为分块失败，因为没有内容块无法进行后续处理
			rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "CHUNKING_FAILED")
			return fmt.Errorf("LLM简历分块结果为空")
		}
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] LLM简历分块成功 for %s. 分块数量: %d", message.SubmissionUUID, len(sections)))

		// 将LLM提取的基本信息和标识符保存到数据库
		updates := make(map[string]interface{})
		if basicInfo != nil {
			// 提取简历标识符 (例如：姓名_电话)
			// 确保 basicInfo 中的键存在且不为空
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
				rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 生成的LLM简历标识符 for %s: %s", message.SubmissionUUID, identifier))
			}

			// 将basicInfo转换为JSON字符串存储
			basicInfoJSON, jsonErr := json.Marshal(basicInfo)
			if jsonErr != nil {
				rp.Config.Logger.Printf("ProcessLLMTasks: 序列化basicInfo到JSON失败 for %s: %v", message.SubmissionUUID, jsonErr)
				// 非致命错误，可以继续，但记录问题
			} else {
				updates["llm_parsed_basic_info"] = basicInfoJSON
			}
		}

		if len(updates) > 0 {
			if err := rp.Storage.MySQL.UpdateResumeSubmissionFields(rp.Storage.MySQL.DB().WithContext(ctx), message.SubmissionUUID, updates); err != nil {
				rp.Config.Logger.Printf("ProcessLLMTasks: 更新LLM提取信息到数据库失败 for %s: %v", message.SubmissionUUID, err)
				// 非致命错误，继续处理
			} else {
				rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功更新LLM提取信息到数据库 for %s", message.SubmissionUUID))
			}
		}

	} else {
		rp.Config.Logger.Printf("ProcessLLMTasks: ResumeChunker未初始化 for %s. 跳过分块。", message.SubmissionUUID)
		// 如果没有分块器，无法进行后续步骤，标记为错误
		rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "CHUNKING_FAILED")
		return fmt.Errorf("ResumeChunker未初始化")
	}

	// 3. 将分块内容嵌入为向量
	var chunkEmbeddings []*types.ResumeChunkVector
	var embedErr error

	if rp.ResumeEmbedder != nil && sections != nil && len(sections) > 0 {
		chunkEmbeddings, embedErr = rp.ResumeEmbedder.Embed(ctx, sections, basicInfo)
		if embedErr != nil {
			rp.Config.Logger.Printf("ProcessLLMTasks: 向量嵌入失败 for %s: %v", message.SubmissionUUID, embedErr)
			rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "VECTORIZATION_FAILED")
			return fmt.Errorf("向量嵌入失败: %w", embedErr)
		}
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 向量嵌入调用完成 for %s. 返回向量组数量: %d (原始分块数量: %d)", message.SubmissionUUID, len(chunkEmbeddings), len(sections)))

		// 现在期望 chunkEmbeddings 的数量与 sections 的数量严格一致
		if chunkEmbeddings == nil || len(chunkEmbeddings) != len(sections) {
			rp.Config.Logger.Printf("[ProcessLLMTasks] 向量嵌入数量 (%d) 与原始分块数量 (%d) 不匹配 for %s. 跳过向量存储", len(chunkEmbeddings), len(sections), message.SubmissionUUID)
			// 这是一个严重问题，可能意味着嵌入过程内部逻辑错误或部分失败
			rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "VECTORIZATION_FAILED")
			return fmt.Errorf("向量嵌入数量与分块数量不匹配: 得到 %d 向量, 期望 %d", len(chunkEmbeddings), len(sections))
		}

	} else if rp.ResumeEmbedder == nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: ResumeEmbedder未初始化 for %s. 跳过向量嵌入。", message.SubmissionUUID)
		rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "VECTORIZATION_FAILED")
		return fmt.Errorf("ResumeEmbedder未初始化")
	} else { // sections is nil or empty
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 没有有效的简历分块进行向量嵌入 for %s.", message.SubmissionUUID))
		// 这种情况通常在分块阶段就会失败并返回，但作为防御性检查
		// 如果sections为空，chunkEmbeddings也应该是空的或nil，上面 len(sections) > 0 的检查会处理
		// 如果因为某种原因sections为空但仍运行到这里，后续的Qdrant存储逻辑会安全处理空chunks
	}

	// 4. 将向量存储到Qdrant
	var storedPointIDs []string
	var storeErr error

	// 使用 chunkEmbeddings (因为数量已验证与sections一致)
	if chunkEmbeddings != nil && sections != nil && len(chunkEmbeddings) == len(sections) && len(sections) > 0 {
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 准备向量存储到Qdrant for %s. 向量数: %d", message.SubmissionUUID, len(chunkEmbeddings)))
		if rp.Storage.Qdrant != nil {
			qdrantChunks := make([]types.ResumeChunk, len(sections))
			floatEmbeddings := make([][]float64, len(chunkEmbeddings))

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

			for i, section := range sections { // Iterate over original sections
				if chunkEmbeddings[i] != nil && chunkEmbeddings[i].Vector != nil {
					floatEmbeddings[i] = chunkEmbeddings[i].Vector
				} else {
					floatEmbeddings[i] = make([]float64, rp.Config.DefaultDimensions) // Use default dimension for empty vector
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
					// Populate ImportanceScore from embedding metadata if present
					if score, ok := chunkEmbeddings[i].Metadata["importance_score"].(float32); ok {
						qdrantChunks[i].ImportanceScore = score
					} else if scoreF64, ok := chunkEmbeddings[i].Metadata["importance_score"].(float64); ok {
						qdrantChunks[i].ImportanceScore = float32(scoreF64)
					}
				}
			}

			storedPointIDs, storeErr = rp.Storage.Qdrant.StoreResumeVectors(ctx, message.SubmissionUUID, qdrantChunks, floatEmbeddings)
			if storeErr != nil {
				rp.Config.Logger.Printf("ProcessLLMTasks: 存储向量到Qdrant失败 for %s: %v", message.SubmissionUUID, storeErr)
				rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "VECTORIZATION_FAILED") // Or a more specific QDRANT_STORE_FAILED
				return fmt.Errorf("存储向量到Qdrant失败: %w", storeErr)                                                   // Make this a hard failure
			} else {
				rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功存储 %d 个向量到Qdrant for %s. Point IDs: %v", len(storedPointIDs), message.SubmissionUUID, storedPointIDs))
				if err := rp.Storage.MySQL.SaveResumeChunks(rp.Storage.MySQL.DB().WithContext(ctx), message.SubmissionUUID, sections); err != nil {
					rp.Config.Logger.Printf("ProcessLLMTasks: 保存简历分块到MySQL失败 for %s: %v", message.SubmissionUUID, err)
				} else {
					rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功保存 %d 个简历分块信息到MySQL for %s", len(sections), message.SubmissionUUID))
				}
			}
		} else {
			rp.Config.Logger.Printf("ProcessLLMTasks: Qdrant存储未初始化 for %s. 跳过向量存储。", message.SubmissionUUID)
			// If Qdrant is nil, it's a setup issue, should be a hard fail as vectorization is core.
			rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "VECTORIZATION_FAILED")
			return fmt.Errorf("Qdrant存储服务未初始化")
		}
	} else if sections != nil && len(sections) > 0 { // This means chunkEmbeddings might be nil or mismatched after all checks
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] (最终检查)向量嵌入结果与分块不匹配或为空 for %s. 跳过向量存储. Embeddings: %d, Sections: %d", message.SubmissionUUID, len(chunkEmbeddings), len(sections)))
		// This case implies a failure in embedding if sections were present.
		rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "VECTORIZATION_FAILED")
		return fmt.Errorf("最终检查发现向量与分块不匹配或嵌入结果为空")
	} else { // sections is nil or empty, or chunkEmbeddings is nil from start
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] (最终检查)没有有效的分块或向量进行存储 for %s.", message.SubmissionUUID))
		// If sections were empty, chunking likely failed, and that should have returned an error earlier.
		// If we reach here with no sections, it's an unexpected state, but processing might be considered complete if no errors prior.
		// However, to be safe, if there were no sections from LLM chunker, it should have been an error.
		// If sections were nil/empty from the start of this LLM task, it implies no content to process for vectors.
		// Let's assume if sections are empty here, it's ok, as no vectors to store.
	}

	// 5. 如果有TargetJobID，进行JD匹配评估
	finalStatus := "PROCESSING_COMPLETED" // 默认最终状态

	if message.TargetJobID != "" && rp.MatchEvaluator != nil {
		rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 检测到TargetJobID: %s for %s. 开始JD匹配评估。", message.TargetJobID, message.SubmissionUUID))
		// 从数据库获取JD文本 (未来可以考虑从缓存获取)
		job, err := rp.Storage.MySQL.GetJobByID(rp.Storage.MySQL.DB().WithContext(ctx), message.TargetJobID)
		if err != nil {
			rp.Config.Logger.Printf("ProcessLLMTasks: 获取JD信息失败 for JobID %s, Submission %s: %v", message.TargetJobID, message.SubmissionUUID, err)
			finalStatus = "MATCH_FAILED" // JD获取失败，影响匹配
		} else if job.JobDescriptionText == "" {
			rp.Config.Logger.Printf("ProcessLLMTasks: JD文本为空 for JobID %s, Submission %s", message.TargetJobID, message.SubmissionUUID)
			finalStatus = "MATCH_FAILED" // JD文本为空，无法匹配
		} else {
			rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功获取JD文本 for JobID %s. JD长度: %d", message.TargetJobID, len(job.JobDescriptionText)))
			evaluation, evalErr := rp.MatchEvaluator.EvaluateMatch(ctx, job.JobDescriptionText, parsedText) // 使用完整的解析文本进行匹配
			if evalErr != nil {
				rp.Config.Logger.Printf("ProcessLLMTasks: JD匹配评估失败 for JobID %s, Submission %s: %v", message.TargetJobID, message.SubmissionUUID, evalErr)
				finalStatus = "MATCH_FAILED"
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
				} else {
					rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] 成功保存JD匹配结果 for JobID %s, Submission %s", message.TargetJobID, message.SubmissionUUID))
				}
			}
		}
	} else if message.TargetJobID != "" && rp.MatchEvaluator == nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: MatchEvaluator未初始化, 但存在TargetJobID %s for %s. 跳过JD匹配。", message.TargetJobID, message.SubmissionUUID)
		// 虽然跳过了匹配，但其他LLM处理（分块、向量化）可能已成功，所以不一定是MatchFailed
		// 状态仍然可以是 ProcessingCompleted，只是没有匹配部分
	}

	// 6. 更新最终处理状态
	// 根据之前的错误来决定最终状态，如果中间有致命错误，则会提前返回
	// storeErr now causes an early return, so this finalStatus update assumes previous steps were successful if reached.

	if err := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, finalStatus); err != nil {
		rp.Config.Logger.Printf("ProcessLLMTasks: 更新最终状态到 %s 失败 for %s: %v", finalStatus, message.SubmissionUUID, err)
		return fmt.Errorf("更新最终状态失败: %w", err)
	}

	rp.LogDebug(fmt.Sprintf("[ProcessLLMTasks] LLM任务处理完成 for %s. 最终状态: %s", message.SubmissionUUID, finalStatus))
	return nil
}

// Helper to create processor options easily
// WithPDFExtractor sets the PDFExtractor.
// ... existing code ...
