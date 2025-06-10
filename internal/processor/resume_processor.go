package processor // 定义了简历处理相关的核心逻辑和组件

import (
	"ai-agent-go/internal/agent"
	"ai-agent-go/internal/config"               // 导入项目配置包
	"ai-agent-go/internal/constants"            // 导入项目常量包
	parser2 "ai-agent-go/internal/parser"       // 导入解析器包，并重命名为parser2以避免与包名冲突
	"ai-agent-go/internal/storage"              // 导入存储层包
	storagetypes "ai-agent-go/internal/storage" // 再次导入存储层包，并命名为storagetypes，可能用于类型区分
	"ai-agent-go/internal/storage/models"       // 导入数据库模型
	"ai-agent-go/internal/types"                // 导入项目自定义类型
	"ai-agent-go/pkg/utils"                     // 导入工具函数包
	"bytes"

	// 导入字节缓冲包
	"context"       // 导入上下文包
	"encoding/json" // 导入JSON编解码包
	"errors"        // 导入错误处理包
	"fmt"           // 导入格式化I/O包

	// 导入I/O接口包
	"log" // 导入日志包
	"os"  // 导入操作系统功能包

	// 导入字符串转换包
	// 导入字符串操作包
	"strconv" // 导入strconv包
	"time"    // 导入时间包

	// 导入eino框架的模型组件
	// 导入eino框架的schema定义
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"        // 导入GORM库
	"gorm.io/gorm/clause" // 导入GORM的子句构建器
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
	Storage *storage.Storage // 存储服务

	// 配置
	Config ComponentConfig // 组件配置
}

// ComponentConfig 组件配置 (保留兼容现有代码)
type ComponentConfig struct {
	UseLLM            bool        // 是否使用LLM
	DefaultDimensions int         // 默认向量维度
	Debug             bool        // 是否开启调试模式
	Logger            *log.Logger // 日志记录器
}

// DefaultResumeEmbedder 是 ResumeEmbedder 接口的默认实现。
type DefaultResumeEmbedder struct { // 定义 DefaultResumeEmbedder 结构体
	textEmbedder TextEmbedder // 包含一个 TextEmbedder 接口类型的字段，用于文本嵌入
}

// NewDefaultResumeEmbedder 创建一个新的 DefaultResumeEmbedder 实例。
func NewDefaultResumeEmbedder(textEmbedder TextEmbedder) (*DefaultResumeEmbedder, error) { // 函数接收 TextEmbedder 参数，返回 DefaultResumeEmbedder 指针和错误
	if textEmbedder == nil { // 检查传入的 textEmbedder 是否为 nil
		return nil, fmt.Errorf("textEmbedder cannot be nil") // 如果为 nil，则返回错误信息
	}
	return &DefaultResumeEmbedder{textEmbedder: textEmbedder}, nil // 返回新创建的 DefaultResumeEmbedder 实例和 nil 错误
}

// EmbedResumeChunks 实现了 ResumeEmbedder 接口。
// 它将简历的各个部分和基本信息转换为文本，然后进行嵌入。
func (dre *DefaultResumeEmbedder) EmbedResumeChunks(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error) { // 方法接收上下文、简历分块和基本信息，返回简历块向量切片和错误
	if dre.textEmbedder == nil { // 检查 DefaultResumeEmbedder 中的 textEmbedder 是否未初始化
		return nil, fmt.Errorf("textEmbedder is not initialized in DefaultResumeEmbedder") // 如果未初始化，则返回错误信息
	}

	var textsToEmbed []string                        // 定义一个字符串切片，用于存储待嵌入的文本
	var correspondingSections []*types.ResumeSection // 定义一个 ResumeSection 指针切片，用于存储与待嵌入文本对应的原始简历部分
	// var isBasicInfoChunk []bool // 不再需要标记basicInfo块，因为我们不再单独嵌入它

	// Process resume sections
	// 处理简历的各个部分
	for _, section := range sections { // 遍历传入的 sections 切片
		if section.Content == "" { // 检查当前 section 的内容是否为空
			continue // 如果内容为空，则跳过当前循环
		}
		var textRepresentation string // 定义一个字符串变量，用于存储当前 section 的文本表示
		if section.Title != "" {      // 检查当前 section 的标题是否不为空
			// 为了更纯粹的内容嵌入，这里可以考虑是否包含Title，或者仅嵌入Content
			// 当前保留Title以提供上下文
			textRepresentation = fmt.Sprintf("Section - %s: %s", section.Title, section.Content) // 如果标题不为空，则将标题和内容格式化为 "Section - [标题]: [内容]" 的形式
		} else { // 如果标题为空
			textRepresentation = section.Content // 则直接使用 section 的内容作为文本表示
		}
		textsToEmbed = append(textsToEmbed, textRepresentation)        // 将生成的文本表示添加到 textsToEmbed 中
		correspondingSections = append(correspondingSections, section) // 将当前 section 添加到 correspondingSections 中
		// isBasicInfoChunk = append(isBasicInfoChunk, false) // 不再需要
	}

	if len(textsToEmbed) == 0 { // 检查 textsToEmbed 是否为空
		return []*types.ResumeChunkVector{}, nil // 如果为空，则返回一个空的 ResumeChunkVector 切片和 nil 错误
	}

	embeddings, err := dre.textEmbedder.EmbedStrings(ctx, textsToEmbed) // 调用 textEmbedder 的 EmbedStrings 方法对 textsToEmbed 中的所有文本进行嵌入
	if err != nil {                                                     // 检查嵌入过程中是否发生错误
		return nil, fmt.Errorf("embedding texts failed: %w", err) // 如果发生错误，则返回包装后的错误信息
	}

	if len(embeddings) != len(textsToEmbed) { // 检查返回的嵌入向量数量是否与待嵌入文本数量一致
		return nil, fmt.Errorf("embedding count mismatch: expected %d, got %d", len(textsToEmbed), len(embeddings)) // 如果数量不一致，则返回错误信息
	}

	var resumeChunkVectors []*types.ResumeChunkVector // 定义一个 ResumeChunkVector 指针切片，用于存储最终生成的简历块向量
	for i, embeddingVector := range embeddings {      // 遍历生成的嵌入向量
		if len(embeddingVector) == 0 { // 检查当前嵌入向量是否为空
			continue // 如果为空，则跳过当前循环
		}

		originalSection := correspondingSections[i] // 获取与当前嵌入向量对应的原始简历部分
		metadata := make(map[string]interface{})    // 创建一个 map 用于存储元数据

		metadata["original_chunk_type"] = string(originalSection.Type) // 将原始分块类型添加到元数据中
		if originalSection.Title != "" {                               // 检查原始分块的标题是否不为空
			metadata["original_chunk_title"] = originalSection.Title // 如果不为空，则将原始分块标题添加到元数据中
		}
		if originalSection.ChunkID > 0 { // 检查原始分块的 ChunkID 是否大于0
			metadata["llm_chunk_id"] = originalSection.ChunkID // 如果大于0，则将 llm_chunk_id 添加到元数据中
		}
		if originalSection.ResumeIdentifier != "" { // 检查原始分块的简历标识符是否不为空
			// ResumeIdentifier 通常从 basicInfo 中来，这里确保它被正确关联
			metadata["llm_resume_identifier"] = originalSection.ResumeIdentifier
		} else if biRI, ok := basicInfo["resume_identifier"]; ok && biRI != "" {
			// 如果 section 本身没有，尝试从 basicInfo 获取
			metadata["llm_resume_identifier"] = biRI
		}

		// 将 basicInfo 中的相关信息添加到每个 chunk 的 metadata 中
		// 这是因为 basicInfo 现在不单独生成向量，但其信息对每个 chunk 都可能有用
		if basicInfo != nil { // 如果基本信息不为空
			for k, v := range basicInfo { // 遍历基本信息
				// 避免覆盖上面已经从 originalSection 设置的字段，除非特定需要
				if _, exists := metadata[k]; !exists { // 如果元数据中不存在该键
					metadata[k] = v // 则添加
				}
			}
		}
		// 确保一些关键的 basicInfo 字段（如果存在）被包含
		// 例如，如果 "name", "phone", "email" 等字段在 basicInfo 中，它们会在这里被加入 metadata
		// 也可以选择性地只添加 "valuableBasicKeys" 对应的字段到metadata

		resumeChunkVectors = append(resumeChunkVectors, &types.ResumeChunkVector{ // 创建一个新的 ResumeChunkVector 并将其指针添加到 resumeChunkVectors 中
			Section:  originalSection, // 设置原始简历部分
			Vector:   embeddingVector, // 设置嵌入向量
			Metadata: metadata,        // 设置元数据
		})
	}

	return resumeChunkVectors, nil // 返回生成的简历块向量切片和 nil 错误
}

// Embed 是 EmbedResumeChunks 的别名，用于满足 ResumeEmbedder 接口。
func (dre *DefaultResumeEmbedder) Embed(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error) { // 定义 Embed 方法，参数和返回值与 EmbedResumeChunks 一致
	return dre.EmbedResumeChunks(ctx, sections, basicInfo) // 调用 EmbedResumeChunks 方法并返回其结果
}

// NewResumeProcessorV2 创建新的简历处理器，使用明确分离的组件和设置
// 这是推荐的构造方法，可以确保清晰的依赖关系
func NewResumeProcessorV2(comp *Components, set *Settings, opts ...SettingOpt) *ResumeProcessor {
	// 应用额外的设置选项
	for _, opt := range opts { // 遍历并应用所有设置选项
		opt(set) // 应用选项到settings
	}

	// 确保必要的默认值
	if set.Logger == nil { // 如果logger为空
		set.Logger = log.New(os.Stdout, "[Processor] ", log.LstdFlags) // 设置一个默认的标准输出logger
	}

	if set.TimeLocation == nil { // 如果时区为空
		set.TimeLocation = time.Local // 设置为本地时区
	}

	// 创建处理器实例，复制组件和设置到对应字段
	processor := &ResumeProcessor{
		// 从 Components 复制组件
		PDFExtractor:   comp.PDFExtractor,   // 复制PDF提取器
		ResumeChunker:  comp.ResumeChunker,  // 复制简历分块器
		ResumeEmbedder: comp.ResumeEmbedder, // 复制简历嵌入器
		MatchEvaluator: comp.MatchEvaluator, // 复制岗位匹配评估器
		Storage:        comp.Storage,        // 复制存储服务

		// 从 Settings 复制设置到 Config
		Config: ComponentConfig{
			UseLLM:            set.UseLLM,            // 复制是否使用LLM的设置
			DefaultDimensions: set.DefaultDimensions, // 复制默认向量维度
			Debug:             set.Debug,             // 复制是否开启调试模式
			Logger:            set.Logger,            // 复制日志记录器
		},
	}

	// 验证关键组件
	if processor.Storage == nil { // 如果存储服务为空
		processor.Config.Logger.Println("警告: ResumeProcessor 的 Storage 依赖未初始化。某些功能可能受限。") // 打印警告信息
	}

	return processor // 返回创建的处理器实例
}

// NewResumeProcessor 创建新的简历处理器组件集合 (向后兼容的构造函数)
func NewResumeProcessor(options ...ProcessorOption) *ResumeProcessor {
	// 创建默认处理器
	processor := &ResumeProcessor{
		Config: ComponentConfig{
			UseLLM:            true,                                              // 默认使用LLM
			DefaultDimensions: 1536,                                              // 默认向量维度为1536
			Debug:             false,                                             // 默认关闭调试模式
			Logger:            log.New(os.Stdout, "[Processor] ", log.LstdFlags), // 默认使用标准输出的logger
		},
	}

	// 应用选项
	for _, option := range options { // 遍历并应用所有传入的选项
		option(processor) // 应用选项到processor
	}

	// 确保 Storage 已被正确初始化，如果没有通过Option注入，则警告
	if processor.Storage == nil {
		processor.Config.Logger.Println("警告: ResumeProcessor 的 Storage 依赖未通过选项注入。某些功能可能受限。")
	}

	return processor // 返回创建的处理器实例
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

// CreateProcessorFromConfig 从配置创建处理器组件集合
func CreateProcessorFromConfig(ctx context.Context, cfg *config.Config, storageManager *storage.Storage) (*ResumeProcessor, error) {
	if cfg == nil { // 检查配置是否为空
		return nil, fmt.Errorf("配置不能为空") // 如果为空则返回错误
	}

	// 1. 创建组件实例
	components := &Components{ // 初始化组件结构体
		Storage: storageManager, // 设置存储管理器
	}

	// 2. 创建设置实例，并使用配置文件中的值
	settings := &Settings{ // 初始化设置
		UseLLM:            true,                                              // 默认启用LLM
		DefaultDimensions: cfg.Qdrant.Dimension,                              // 使用配置中的向量维度
		Debug:             cfg.Logger.Level == "debug",                       // 根据日志级别设置调试模式
		Logger:            log.New(os.Stdout, "[Processor] ", log.LstdFlags), // 创建默认logger
		TimeLocation:      time.Local,                                        // 设置本地时区
	}

	// 3. 根据配置创建PDF提取器
	var err error
	// 使用统一的PDF解析器构建函数
	components.PDFExtractor, err = BuildPDFExtractor(ctx, cfg, func(prefix string) *log.Logger {
		return log.New(os.Stdout, prefix, log.LstdFlags)
	})
	if err != nil {
		return nil, fmt.Errorf("创建PDF提取器失败: %w", err)
	}

	// 4. 如果配置了API密钥，添加LLM功能
	if cfg.Aliyun.APIKey != "" {
		settings.Logger.Println("检测到API密钥，配置LLM功能...")

		// 创建用于分块的千问模型
		chunkerModel, err := agent.NewAliyunQwenChatModel(
			cfg.Aliyun.APIKey,
			cfg.GetModelForTask("resume_chunking"),
			cfg.Aliyun.APIURL,
		)
		if err != nil {
			return nil, fmt.Errorf("创建分块LLM模型失败: %w", err)
		}
		chunkerLogger := log.New(os.Stdout, "[LLMChunker] ", log.LstdFlags)
		components.ResumeChunker = parser2.NewLLMResumeChunker(chunkerModel, chunkerLogger)

		// 创建用于评估的千问模型
		evaluatorModel, err := agent.NewAliyunQwenChatModel(
			cfg.Aliyun.APIKey,
			cfg.GetModelForTask("job_evaluate"),
			cfg.Aliyun.APIURL,
		)
		if err != nil {
			return nil, fmt.Errorf("创建评估LLM模型失败: %w", err)
		}
		evaluatorLogger := log.New(os.Stdout, "[JobEvaluator] ", log.LstdFlags)
		components.MatchEvaluator = parser2.NewLLMJobEvaluator(evaluatorModel, evaluatorLogger)

		// 创建简历嵌入器
		aliyunEmbedder, err := parser2.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
		if err != nil {
			return nil, fmt.Errorf("创建阿里云嵌入器失败: %w", err)
		}
		// NewDefaultResumeEmbedder 位于 resume_service.go 中，但在同一个包内，可以直接调用
		resumeEmbedder, embedErr := NewDefaultResumeEmbedder(aliyunEmbedder)
		if embedErr != nil {
			return nil, fmt.Errorf("创建简历嵌入器失败: %w", embedErr)
		}
		components.ResumeEmbedder = resumeEmbedder
	}

	// 5. 创建处理器
	processor := NewResumeProcessorV2(components, settings)

	return processor, nil // 返回创建的处理器和nil错误
}

// NewProcessorFromConfig 是 CreateProcessorFromConfig 的别名，保持向后兼容性
func NewProcessorFromConfig(ctx context.Context, cfg *config.Config, storageManager *storage.Storage) (*ResumeProcessor, error) {
	return CreateProcessorFromConfig(ctx, cfg, storageManager) // 直接调用CreateProcessorFromConfig函数
}

// ProcessUploadedResume 接收上传消息，完成文本提取、去重、发送到LLM队列的完整流程
// 使用数据库事务确保所有状态更新的原子性
func (rp *ResumeProcessor) ProcessUploadedResume(ctx context.Context, message storagetypes.ResumeUploadMessage, cfg *config.Config) error {
	if rp.Storage == nil { // 检查存储是否初始化
		return fmt.Errorf("ResumeProcessor: Storage is not initialized") // 未初始化则返回错误
	}
	if rp.PDFExtractor == nil { // 检查PDF提取器是否初始化
		return fmt.Errorf("ResumeProcessor: PDFExtractor is not initialized") // 未初始化则返回错误
	}

	err := rp.Storage.MySQL.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error { // 开启数据库事务
		// 1. 更新初始状态为 PENDING_PARSING
		if err := tx.Model(&models.ResumeSubmission{}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			Update("processing_status", constants.StatusPendingParsing).Error; err != nil {
			rp.logDebug("更新简历 %s 状态为 %s 失败: %v", message.SubmissionUUID, constants.StatusPendingParsing, err)
			return NewUpdateError(message.SubmissionUUID, fmt.Sprintf("更新状态为%s失败", constants.StatusPendingParsing))
		}

		// 2. 解析并去重
		text, textMD5Hex, err := rp._parseAndDeduplicateResume(ctx, tx, message) // 调用内部方法解析和去重
		if err != nil {                                                          // 如果出错
			if errors.Is(err, ErrDuplicateContent) { // 如果是内容重复错误
				return nil // 内容重复是正常流程，提交状态更新并返回nil，事务将提交
			}
			return err // 其他错误则回滚事务
		}

		// 3. 上传解析后的文本到MinIO
		textObjectKey, err := rp.Storage.MinIO.UploadParsedText(ctx, message.SubmissionUUID, text) // 上传文本
		if err != nil {                                                                            // 如果上传失败
			rp.logDebug("上传解析后的文本到MinIO失败 (简历 %s): %v", message.SubmissionUUID, err)
			return NewStoreError(message.SubmissionUUID, err.Error()) // 返回存储错误
		}
		rp.logDebug("简历 %s 的解析文本已上传到MinIO: %s", message.SubmissionUUID, textObjectKey)

		// 4. 构建下一个队列的消息
		processingMessage := storagetypes.ResumeProcessingMessage{
			SubmissionUUID:      message.SubmissionUUID, // 提交UUID
			ParsedTextPathOSS:   textObjectKey,          // 解析后文本的对象存储路径
			TargetJobID:         message.TargetJobID,    // 目标岗位ID
			ParsedTextObjectKey: textObjectKey,          // 兼容旧字段
		}

		// 5. [Outbox] 将消息写入 Outbox 表，而不是直接发布
		payloadBytes, err := json.Marshal(processingMessage) // 序列化消息为JSON
		if err != nil {                                      // 如果序列化失败
			rp.logDebug("ProcessUploadedResume: 序列化 outbox payload 失败 for %s: %v", message.SubmissionUUID, err)
			return NewUpdateError(message.SubmissionUUID, "序列化 outbox payload 失败") // 返回更新错误
		}

		outboxEntry := models.OutboxMessage{ // 创建发件箱条目
			AggregateID:      message.SubmissionUUID,                // 聚合ID
			EventType:        "resume.parsed",                       // 事件类型
			Payload:          string(payloadBytes),                  // 消息负载
			TargetExchange:   cfg.RabbitMQ.ProcessingEventsExchange, // 目标交换机
			TargetRoutingKey: cfg.RabbitMQ.ParsedRoutingKey,         // 目标路由键
		}

		if err := tx.Create(&outboxEntry).Error; err != nil { // 在事务中创建发件箱记录
			rp.logDebug("ProcessUploadedResume: 插入 outbox 记录失败 for %s: %v", message.SubmissionUUID, err)
			return NewUpdateError(message.SubmissionUUID, "插入 outbox 记录失败") // 返回更新错误
		}
		rp.logDebug("ProcessUploadedResume: 成功为 %s 创建 outbox 记录", message.SubmissionUUID)

		// 6. 更新数据库记录
		if err := tx.Model(&models.ResumeSubmission{}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			Updates(map[string]interface{}{ // 更新多个字段
				"parsed_text_path_oss": textObjectKey,                // 解析后文本路径
				"raw_text_md5":         textMD5Hex,                   // 文本MD5
				"processing_status":    constants.StatusQueuedForLLM, // 状态更新为排队等待LLM处理
				"parser_version":       cfg.ActiveParserVersion,      // 解析器版本
			}).Error; err != nil {
			rp.logDebug("更新简历 %s 数据库记录失败: %v", message.SubmissionUUID, err)
			return NewUpdateError(message.SubmissionUUID, "更新数据库失败") // 返回更新错误
		}

		return nil // 事务成功，返回nil
	})

	if err != nil { // 如果事务执行过程中发生错误
		updateErr := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusUploadProcessingFailed) // 更新状态为上传处理失败
		if updateErr != nil {                                                                                                           // 如果更新状态也失败了
			rp.logDebug("在事务失败后更新状态为失败时出错 (简历 %s): %v", message.SubmissionUUID, updateErr) // 记录日志
		}
		return err // 返回原始错误
	}

	rp.logDebug("上传任务 (简历 %s) 的处理已成功完成。", message.SubmissionUUID)
	return nil // 所有操作成功，返回nil
}

// _parseAndDeduplicateResume 解析简历文本，去重，并将结果上传。
// 它返回一个特殊的 ErrDuplicateContent 错误，如果文本内容是重复的。
func (rp *ResumeProcessor) _parseAndDeduplicateResume(ctx context.Context, tx *gorm.DB, message storagetypes.ResumeUploadMessage) (string, string, error) {
	ctx, span := tracer.Start(ctx, "ResumeProcessor._parseAndDeduplicateResume")
	defer span.End()

	// 步骤 1: 从MinIO下载简历文件
	originalFileBytes, err := rp.Storage.MinIO.GetResumeFile(ctx, message.OriginalFilePathOSS)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to download resume file")
		rp.logDebug("从MinIO下载简历 %s 失败: %v", message.SubmissionUUID, err)
		return "", "", NewDownloadError(message.SubmissionUUID, err.Error())
	}
	span.AddEvent("file content downloaded")
	rp.logDebug("简历 %s 从MinIO下载成功，大小: %d bytes", message.SubmissionUUID, len(originalFileBytes))

	// 步骤 2: 使用注入的 PDFExtractor 提取文本
	parsedText, _, err := rp.PDFExtractor.ExtractTextFromReader(ctx, bytes.NewReader(originalFileBytes), message.OriginalFilePathOSS, nil)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to extract text from PDF")
		rp.logDebug("ProcessUploadedResume: 提取简历文本失败 for %s: %v", message.SubmissionUUID, err)
		return "", "", NewParseError(message.SubmissionUUID, err.Error())
	}
	span.AddEvent("parsed text extracted")
	rp.logDebug("ProcessUploadedResume: 成功提取文本 for %s, 长度: %d", message.SubmissionUUID, len(parsedText))

	// 步骤 3: 使用专用的、带BOM的方法将提取的文本上传到 Minio
	_, err = rp.Storage.MinIO.UploadParsedText(ctx, message.SubmissionUUID, parsedText)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to upload parsed text")
		return "", "", fmt.Errorf("上传解析后的文本到MinIO失败: %w", err)
	}
	span.AddEvent("parsed text uploaded")

	// 步骤 4: 计算解析后文本的MD5并去重
	parsedTextMD5 := utils.CalculateMD5([]byte(parsedText))
	rp.logDebug("ProcessUploadedResume: 计算得到文本MD5 %s for %s", parsedTextMD5, message.SubmissionUUID)

	textExists, err := rp.Storage.Redis.CheckAndAddParsedTextMD5(ctx, parsedTextMD5)
	if err != nil {
		rp.logDebug("ProcessUploadedResume: 使用Redis原子操作检查文本MD5失败 for %s: %v, 将继续处理，但文本去重可能失效", message.SubmissionUUID, err)
	} else if textExists {
		rp.logDebug("ProcessUploadedResume: 检测到重复的文本MD5 %s for %s，标记为重复内容", parsedTextMD5, message.SubmissionUUID)
		if err := tx.Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", message.SubmissionUUID).Update("processing_status", constants.StatusContentDuplicateSkipped).Error; err != nil {
			return "", "", NewUpdateError(message.SubmissionUUID, "更新重复内容状态失败")
		}
		return "", "", ErrDuplicateContent
	}
	rp.logDebug("ProcessUploadedResume: 文本MD5 %s 不存在于Redis, 继续处理 for %s", parsedTextMD5, message.SubmissionUUID)

	return parsedText, parsedTextMD5, nil
}

// ProcessLLMTasks 接收LLM处理消息，完成分块、评估、存储向量和结果的完整流程
// 使用数据库事务确保所有状态更新的原子性
func (rp *ResumeProcessor) ProcessLLMTasks(ctx context.Context, message storagetypes.ResumeProcessingMessage, cfg *config.Config) error {
	ctx, span := tracer.Start(ctx, "ResumeProcessor.ProcessLLMTasks",
		trace.WithAttributes(
			attribute.String("submission_uuid", message.SubmissionUUID),
			attribute.String("target_job_id", message.TargetJobID),
		),
	)
	defer span.End()

	rp.logDebug("[ProcessLLMTasks] 开始处理LLM任务 for %s, 目标Job ID: %s", message.SubmissionUUID, message.TargetJobID)

	// 使用事务来保证读取-更新的原子性和幂等性
	err := rp.Storage.MySQL.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error { // 开启数据库事务
		// 1. 获取最新的 ResumeSubmission 记录并锁定，防止并发处理
		var submission models.ResumeSubmission                    // 声明一个submission变量
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}). // 添加悲观锁
										Where("submission_uuid = ?", message.SubmissionUUID).
										First(&submission).Error; err != nil { // 根据UUID查找记录

			if errors.Is(err, gorm.ErrRecordNotFound) { // 如果记录未找到
				rp.logInfo("ProcessLLMTasks: ResumeSubmission 记录未找到 for %s: %v", message.SubmissionUUID, err)
				// 记录不存在，可能已被删除，直接确认消息
				return nil
			}
			rp.logInfo("ProcessLLMTasks: 获取 ResumeSubmission 记录失败 for %s: %v", message.SubmissionUUID, err)
			return fmt.Errorf("获取 ResumeSubmission 记录失败: %w", err)
		}

		// 2. 关键的幂等性检查
		// 使用常量集替代内联的状态检查
		if !constants.IsStatusAllowed(submission.ProcessingStatus, constants.AllowedStatusesForLLM) {
			rp.logDebug("[ProcessLLMTasks] 跳过重复/无效状态的消息 for %s, 当前状态: %s", message.SubmissionUUID, submission.ProcessingStatus)
			return nil // 状态不匹配，说明是重复消息或已处理，直接确认并返回
		}

		// 3. 更新状态为 PENDING_LLM，表示开始处理
		if err := tx.Model(&submission).Update("processing_status", constants.StatusPendingLLM).Error; err != nil {
			rp.logInfo("ProcessLLMTasks: 更新状态到 PENDING_LLM 失败 for %s: %v", message.SubmissionUUID, err)
			return err
		}

		return nil // 事务成功提交
	})

	if err != nil { // 如果开始处理的事务失败
		return err // 直接返回错误
	}

	// --- 事务外执行IO操作 (下载文本，LLM分块，向量化，写入Qdrant) ---
	parsedText, sections, basicInfo, err := rp._downloadAndChunkResume(ctx, message)
	if err != nil { // 如果下载或分块失败
		updateErr := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusChunkingFailed) // 更新状态为分块失败
		if updateErr != nil {                                                                                                   // 如果更新状态也失败
			rp.logInfo("更新状态为 CHUNKING_FAILED 时出错 for %s: %v", message.SubmissionUUID, updateErr)
		}
		return err // 返回错误，消息将被nack
	}

	// 向量化
	chunkEmbeddings, err := rp._embedChunks(ctx, sections, basicInfo)
	if err != nil { // 如果向量化失败
		updateErr := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusVectorizationFailed) // 更新状态为向量化失败
		if updateErr != nil {                                                                                                        // 如果更新状态也失败
			rp.logInfo("更新状态为 VECTORIZATION_FAILED 时出错 for %s: %v", message.SubmissionUUID, updateErr)
		}
		return err // 返回错误，消息将被nack
	}

	// 准备Qdrant数据
	qdrantChunks, floatEmbeddings, err := rp._prepareQdrantData(message.SubmissionUUID, sections, basicInfo, chunkEmbeddings)
	if err != nil { // 如果准备失败
		return err // 返回错误
	}

	// 执行Qdrant写入
	var pointIDs []string
	if rp.Storage.Qdrant != nil { // 检查是否初始化
		pointIDs, err = rp.Storage.Qdrant.StoreResumeVectors(ctx, message.SubmissionUUID, qdrantChunks, floatEmbeddings) // 存储向量
		if err != nil {                                                                                                  // 如果存储失败
			rp.logInfo("ProcessLLMTasks: 存储向量到Qdrant失败 for %s: %v", message.SubmissionUUID, err)
			// 更新状态为失败
			updateErr := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusQdrantStoreFailed)
			if updateErr != nil {
				rp.logInfo("更新状态为 QDRANT_STORE_FAILED 时出错 for %s: %v", message.SubmissionUUID, updateErr)
			}
			return fmt.Errorf("存储向量到Qdrant失败: %w", err)
		}
		rp.logDebug("[ProcessLLMTasks] 成功存储 %d 个向量到Qdrant for %s", len(pointIDs), message.SubmissionUUID)
	} else { // 如果未初始化
		return fmt.Errorf("qdrant存储服务未初始化")
	}

	// --- 重新进入事务，执行数据库写操作 ---
	tx := rp.Storage.MySQL.DB().WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()

	// 执行LLM处理的数据库事务
	err = rp._executeLLMProcessingTransaction(ctx, tx, message, qdrantChunks, floatEmbeddings, sections, basicInfo, parsedText, pointIDs)
	if err != nil {
		return err
	}

	if err = tx.Commit().Error; err != nil {
		// 最终失败状态由具体的失败点决定，这里只记录通用失败
		rp.logInfo("[ProcessLLMTasks] 提交事务失败 for %s: %v", message.SubmissionUUID, err)
		// 最终的失败状态更新，如果之前的步骤没有成功更新的话
		finalErrUpdate := rp.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusLLMProcessingFailed)
		if finalErrUpdate != nil { // 如果更新失败
			rp.logInfo("在最终更新状态为 LLM_PROCESSING_FAILED 时出错 for %s: %v", message.SubmissionUUID, finalErrUpdate)
		}
		return err // 返回错误
	}

	rp.logDebug("[ProcessLLMTasks] 成功完成LLM处理流程 for %s", message.SubmissionUUID)
	return nil // 成功完成
}

func (rp *ResumeProcessor) _downloadAndChunkResume(ctx context.Context, message storagetypes.ResumeProcessingMessage) (string, []*types.ResumeSection, map[string]string, error) { // 下载并分块简历
	parsedText, err := rp.Storage.MinIO.GetParsedText(ctx, message.ParsedTextPathOSS) // 从MinIO获取解析后的文本
	if err != nil {                                                                   // 如果获取失败
		rp.logInfo("ProcessLLMTasks: 从MinIO下载解析文本失败 for %s (path: %s): %v", message.SubmissionUUID, message.ParsedTextPathOSS, err)
		return "", nil, nil, fmt.Errorf("下载解析文本失败: %w", err)
	}
	if parsedText == "" { // 如果文本为空
		rp.logInfo("ProcessLLMTasks: 下载的解析文本为空 for %s (path: %s)", message.SubmissionUUID, message.ParsedTextPathOSS)
		return "", nil, nil, fmt.Errorf("下载的解析文本为空")
	}
	rp.logDebug("[ProcessLLMTasks] 成功下载解析文本 for %s. 文本长度: %d", message.SubmissionUUID, len(parsedText))

	if rp.ResumeChunker == nil { // 检查分块器是否初始化
		return "", nil, nil, fmt.Errorf("ResumeChunker未初始化")
	}
	sections, basicInfo, chunkErr := rp.ResumeChunker.ChunkResume(ctx, parsedText) // 对文本进行分块
	if chunkErr != nil {                                                           // 如果分块失败
		rp.logInfo("ProcessLLMTasks: LLM简历分块失败 for %s: %v", message.SubmissionUUID, chunkErr)
		return "", nil, nil, fmt.Errorf("LLM简历分块失败: %w", chunkErr)
	}
	if len(sections) == 0 { // 如果分块结果为空
		rp.logInfo("ProcessLLMTasks: LLM简历分块结果为空 for %s", message.SubmissionUUID)
		return "", nil, nil, fmt.Errorf("LLM简历分块结果为空")
	}
	rp.logDebug("[ProcessLLMTasks] LLM简历分块成功 for %s. 分块数量: %d", message.SubmissionUUID, len(sections))
	return parsedText, sections, basicInfo, nil // 返回解析文本、分块、基本信息和nil错误
}

func (rp *ResumeProcessor) _embedChunks(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error) { // 对分块进行向量化
	if rp.ResumeEmbedder == nil { // 检查嵌入器是否初始化
		return nil, fmt.Errorf("ResumeEmbedder未初始化")
	}

	// 筛选非 BASIC_INFO 类型的分块
	// 注释：我们可以选择在这里过滤掉 BASIC_INFO 分块，但为了保持向后兼容，
	// 我们依然对所有分块进行向量化，并在 _prepareQdrantData 中进行过滤。
	// 未来可以取消下面的注释，启用过滤，以节约API调用和计算资源。
	/*
		var sectionsToEmbed []*types.ResumeSection
		for _, section := range sections {
			if section.Type != types.SectionBasicInfo {
				sectionsToEmbed = append(sectionsToEmbed, section)
			} else if rp.Config.Debug {
				rp.Config.Logger.Printf("在向量化阶段跳过BASIC_INFO分块，分块ID: %d", section.ChunkID)
			}
		}

		if len(sectionsToEmbed) == 0 {
			return nil, fmt.Errorf("过滤BASIC_INFO后没有可向量化的分块")
		}

		// 调用嵌入器进行向量化，只处理非 BASIC_INFO 分块
		return rp.ResumeEmbedder.Embed(ctx, sectionsToEmbed, basicInfo)
	*/

	// 当前实现：对所有分块进行向量化
	chunkEmbeddings, embedErr := rp.ResumeEmbedder.Embed(ctx, sections, basicInfo) // 调用嵌入器进行向量化
	if embedErr != nil {                                                           // 如果失败
		rp.logInfo("ProcessLLMTasks: 向量嵌入失败: %v", embedErr)
		return nil, fmt.Errorf("向量嵌入失败: %w", embedErr)
	}
	rp.logDebug("[ProcessLLMTasks] 向量嵌入调用完成. 返回向量组数量: %d", len(chunkEmbeddings))
	if len(chunkEmbeddings) != len(sections) { // 检查返回的向量数量是否与分块数量一致
		return nil, fmt.Errorf("向量嵌入数量(%d)与分块数量(%d)不匹配", len(chunkEmbeddings), len(sections))
	}
	return chunkEmbeddings, nil // 返回向量和nil错误
}

// _executeLLMProcessingTransaction 执行LLM处理相关的数据库事务
// 此方法已经假设Qdrant写入已在事务外完成，不再包含Qdrant写入逻辑
func (rp *ResumeProcessor) _executeLLMProcessingTransaction(ctx context.Context, tx *gorm.DB, message storagetypes.ResumeProcessingMessage, qdrantChunks []types.ResumeChunk, floatEmbeddings [][]float64, sections []*types.ResumeSection, basicInfo map[string]string, parsedText string, pointIDs []string) error {
	// 此处PointID已通过外部Qdrant.StoreResumeVectors调用获取并传入
	// 不再需要从qdrantChunks中提取

	// 1. 查找或创建候选人
	var candidateID string
	if basicInfo != nil {
		// 确保至少有一个联系方式
		email, emailOk := basicInfo["email"]
		phone, phoneOk := basicInfo["phone"]

		if emailOk && email != "" || phoneOk && phone != "" {
			candidate, err := rp.Storage.MySQL.FindOrCreateCandidate(ctx, tx, basicInfo)
			if err != nil {
				rp.logInfo("ProcessLLMTasks: 查找或创建候选人失败 for %s: %v", message.SubmissionUUID, err)
				return fmt.Errorf("查找或创建候选人失败: %w", err)
			}
			if candidate != nil {
				candidateID = candidate.CandidateID
				rp.logDebug("[ProcessLLMTasks] 成功关联候选人 %s for submission %s", candidateID, message.SubmissionUUID)
			}
		}
	}

	// 2. 保存简历分块到MySQL
	if err := rp.Storage.MySQL.SaveResumeChunks(tx, message.SubmissionUUID, sections, pointIDs); err != nil { // 保存分块信息到MySQL
		rp.logInfo("ProcessLLMTasks: 保存简历分块到MySQL失败 for %s: %v", message.SubmissionUUID, err)
		return fmt.Errorf("保存简历分块到MySQL失败: %w", err)
	}
	rp.logDebug("[ProcessLLMTasks] 成功保存 %d 个简历分块信息到MySQL for %s", len(sections), message.SubmissionUUID)

	// 3. 准备并执行最终的数据库更新
	updates := rp._prepareFinalDBUpdates(basicInfo, pointIDs, candidateID)                                                                   // 准备最终更新的数据
	if err := tx.Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", message.SubmissionUUID).Updates(updates).Error; err != nil { // 执行更新
		rp.logInfo("ProcessLLMTasks: 最终更新数据库失败 for %s: %v", message.SubmissionUUID, err)
		return fmt.Errorf("最终更新数据库失败: %w", err)
	}

	// 4. 如果有指定目标岗位，则保存匹配评估结果
	if message.TargetJobID != "" {
		if err := rp._saveMatchEvaluation(ctx, tx, message, parsedText); err != nil {
			// 这个失败不影响整体流程，记录日志后继续
			log.Printf("WARNING: 保存JD匹配评估失败 for %s, JobID %s: %v", message.SubmissionUUID, message.TargetJobID, err)
		}
	}

	// 5. 所有步骤成功，更新为最终成功状态
	if err := tx.Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", message.SubmissionUUID).Update("processing_status", constants.StatusProcessingCompleted).Error; err != nil {
		rp.logInfo("ProcessLLMTasks: 更新最终成功状态失败 for %s: %v", message.SubmissionUUID, err)
		return fmt.Errorf("更新最终成功状态失败: %w", err)
	}

	return nil // 返回nil表示成功
}

func (rp *ResumeProcessor) _prepareFinalDBUpdates(basicInfo map[string]string, pointIDs []string, candidateID string) map[string]interface{} { // 准备最终的数据库更新内容
	updates := make(map[string]interface{}) // 创建一个map用于存储更新
	if candidateID != "" {
		updates["candidate_id"] = candidateID
	}
	if basicInfo != nil { // 如果有基本信息
		name, nameOk := basicInfo["name"]                   // 获取姓名
		phone, phoneOk := basicInfo["phone"]                // 获取电话
		identifier := ""                                    // 初始化标识符
		if nameOk && name != "" && phoneOk && phone != "" { // 如果姓名和电话都存在
			identifier = fmt.Sprintf("%s_%s", name, phone) // 格式化标识符
		} else if nameOk && name != "" { // 如果只有姓名
			identifier = name
		} else if phoneOk && phone != "" { // 如果只有电话
			identifier = phone
		}
		if identifier != "" { // 如果标识符不为空
			updates["llm_resume_identifier"] = identifier // 添加到更新map
		}
		basicInfoJSON, jsonErr := json.Marshal(basicInfo) // 将基本信息序列化为JSON
		if jsonErr == nil {                               // 如果序列化成功
			updates["llm_parsed_basic_info"] = basicInfoJSON // 添加到更新map
		}
	}
	// 不在这里更新状态，由 _executeLLMProcessingTransaction 控制
	// updates["processing_status"] = "LLM_PROCESSED"
	if len(pointIDs) > 0 { // 如果有Qdrant的point ID
		pointIDsJSON, err := json.Marshal(pointIDs) // 序列化为JSON
		if err == nil {                             // 如果成功
			updates["qdrant_point_ids"] = pointIDsJSON // 添加到更新map
		}
	}
	return updates // 返回更新map
}

// _prepareQdrantData prepares the data structures needed for Qdrant storage.
func (rp *ResumeProcessor) _prepareQdrantData(submissionUUID string, sections []*types.ResumeSection, basicInfo map[string]string, chunkEmbeddings []*types.ResumeChunkVector) ([]types.ResumeChunk, [][]float64, error) {
	if len(chunkEmbeddings) == 0 {
		return nil, nil, fmt.Errorf("没有有效的向量数据")
	}

	// 创建用于存储的切片
	var chunks []types.ResumeChunk
	var embeddings [][]float64

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

	// 遍历处理每个分块向量
	for i, chunkVector := range chunkEmbeddings {
		// 跳过 BASIC_INFO 类型的分块，不存储到向量数据库
		if chunkVector.Section != nil && chunkVector.Section.Type == types.SectionBasicInfo {
			if rp.Config.Debug {
				rp.Config.Logger.Printf("跳过BASIC_INFO分块，不存入向量数据库, 分块ID: %d", i+1)
			}
			continue
		}

		// 使用向量
		vectors := chunkVector.Vector
		if vectors == nil || len(vectors) == 0 {
			// 创建一个默认的空向量
			vectors = make([]float64, rp.Config.DefaultDimensions)
			rp.logInfo("ProcessLLMTasks: 发现空嵌入向量 at index %d for %s, 使用默认空向量", i, submissionUUID)
		}

		// 创建分块数据
		chunk := types.ResumeChunk{
			ChunkID:   chunkVector.Section.ChunkID,
			ChunkType: string(chunkVector.Section.Type),
			Content:   chunkVector.Section.Content,
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

		// 添加特定的元数据信息（如果存在）
		if chunkVector.Metadata != nil {
			if specificExp, ok := chunkVector.Metadata["experience_years"].(int); ok {
				chunk.Metadata.ExperienceYears = specificExp
			}
			if specificEdu, ok := chunkVector.Metadata["education_level"].(string); ok {
				chunk.Metadata.EducationLevel = specificEdu
			}
			if score, ok := chunkVector.Metadata["importance_score"].(float32); ok {
				chunk.ImportanceScore = score
			} else if scoreF64, ok := chunkVector.Metadata["importance_score"].(float64); ok {
				chunk.ImportanceScore = float32(scoreF64)
			}
		}

		chunks = append(chunks, chunk)
		embeddings = append(embeddings, vectors)
	}

	if len(chunks) == 0 {
		return nil, nil, fmt.Errorf("过滤BASIC_INFO后没有可存储的分块")
	}

	return chunks, embeddings, nil
}

// _saveMatchEvaluation handles the logic for fetching JD, evaluating match, and saving the result.
func (rp *ResumeProcessor) _saveMatchEvaluation(ctx context.Context, tx *gorm.DB, message storagetypes.ResumeProcessingMessage, parsedText string) error { // 保存人岗匹配评估结果
	if message.TargetJobID == "" || rp.MatchEvaluator == nil { // 如果没有目标岗位ID或匹配评估器
		return nil // 无需操作，直接返回
	}

	job, err := rp.Storage.MySQL.GetJobByID(tx.WithContext(ctx), message.TargetJobID) // 根据ID获取岗位信息
	if err != nil {                                                                   // 如果获取失败
		rp.logInfo("ProcessLLMTasks: 获取JD信息失败 for JobID %s, Submission %s: %v", message.TargetJobID, message.SubmissionUUID, err)
		return fmt.Errorf("获取JD信息失败: %w", err)
	}
	if job.JobDescriptionText == "" { // 如果JD文本为空
		rp.logInfo("ProcessLLMTasks: JD文本为空 for JobID %s, Submission %s", message.TargetJobID, message.SubmissionUUID)
		return fmt.Errorf("JD文本为空")
	}

	rp.logDebug("[ProcessLLMTasks] 成功获取JD文本 for JobID %s. JD长度: %d", message.TargetJobID, len(job.JobDescriptionText))
	evaluation, evalErr := rp.MatchEvaluator.EvaluateMatch(ctx, job.JobDescriptionText, parsedText) // 进行人岗匹配评估
	if evalErr != nil {                                                                             // 如果评估失败
		rp.logInfo("ProcessLLMTasks: JD匹配评估失败 for JobID %s, Submission %s: %v", message.TargetJobID, message.SubmissionUUID, evalErr)
		return fmt.Errorf("JD匹配评估失败: %w", evalErr)
	}

	rp.logDebug("[ProcessLLMTasks] JD匹配评估成功 for JobID %s, Submission %s. Score: %d", message.TargetJobID, message.SubmissionUUID, evaluation.MatchScore)
	matchHighlightsJSON, _ := json.Marshal(evaluation.MatchHighlights) // 序列化匹配亮点
	potentialGapsJSON, _ := json.Marshal(evaluation.PotentialGaps)     // 序列化潜在差距

	matchRecord := models.JobSubmissionMatch{ // 创建匹配记录
		SubmissionUUID:         message.SubmissionUUID,        // 提交UUID
		JobID:                  message.TargetJobID,           // 岗位ID
		LLMMatchScore:          &evaluation.MatchScore,        // LLM匹配分数
		LLMMatchHighlightsJSON: matchHighlightsJSON,           // 匹配亮点JSON
		LLMPotentialGapsJSON:   potentialGapsJSON,             // 潜在差距JSON
		LLMResumeSummaryForJD:  evaluation.ResumeSummaryForJD, // 针对JD的简历摘要
		EvaluationStatus:       "EVALUATED",                   // 评估状态
		EvaluatedAt:            utils.TimePtr(time.Now()),     // 评估时间
	}
	if err := rp.Storage.MySQL.CreateJobSubmissionMatch(tx.WithContext(ctx), &matchRecord); err != nil { // 创建记录
		rp.logInfo("ProcessLLMTasks: 保存JD匹配结果失败 for JobID %s, Submission %s: %v", message.TargetJobID, message.SubmissionUUID, err)
		return fmt.Errorf("保存JD匹配结果失败: %w", err)
	}

	rp.logDebug("[ProcessLLMTasks] 成功保存JD匹配结果 for JobID %s, Submission %s", message.TargetJobID, message.SubmissionUUID)
	return nil // 返回nil表示成功
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
