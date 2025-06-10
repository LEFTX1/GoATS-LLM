package processor

import (
	"ai-agent-go/internal/agent"
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	"ai-agent-go/internal/logger"
	"ai-agent-go/internal/parser"
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
	"log"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 定义公共错误类型，用于整个服务
var (
	// 注意：ErrDuplicateContent已在resume_processor.go中定义，此处不再重复定义
	ErrStorageNotInit   = errors.New("storage is not initialized")   // 存储未初始化错误
	ErrExtractorNotInit = errors.New("extractor is not initialized") // 提取器未初始化错误
	ErrChunkerNotInit   = errors.New("chunker is not initialized")   // 分块器未初始化错误
	ErrEmbedderNotInit  = errors.New("embedder is not initialized")  // 嵌入器未初始化错误
	ErrEvaluatorNotInit = errors.New("evaluator is not initialized") // 评估器未初始化错误
	ErrDuplicateContent = errors.New("duplicate content detected")   // 定义一个内容重复的错误
)

// 定义tracer
var tracer = otel.Tracer("processor")

// ResumeService 定义简历处理服务的接口
// 提供统一的服务层接口，隐藏内部实现细节
type ResumeService interface {
	// ProcessUploadedResume 处理上传的简历，包括文本提取和去重
	ProcessUploadedResume(ctx context.Context, message storagetypes.ResumeUploadMessage) error

	// ProcessLLMTasks 处理LLM相关任务，包括分块、向量化和存储
	ProcessLLMTasks(ctx context.Context, message storagetypes.ResumeProcessingMessage) error
}

// resumeServiceImpl 是ResumeService的实现
// 采用Facade模式，内部持有所有需要的组件，但不暴露给外部
type resumeServiceImpl struct {
	components Components      // 组件依赖
	config     *config.Config  // 配置信息
	logger     *zerolog.Logger // 使用zerolog替代log.Logger
}

// NewResumeService 创建新的简历服务实例
func NewResumeService(cfg *config.Config, storage *storage.Storage, logger *zerolog.Logger) (ResumeService, error) {
	if logger == nil {
		// 如果未提供logger，创建一个默认的
		defaultLogger := zerolog.Nop()
		logger = &defaultLogger
	}

	// 创建组件
	components, err := createComponents(cfg, storage, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create components: %w", err)
	}

	return &resumeServiceImpl{
		components: components,
		config:     cfg,
		logger:     logger,
	}, nil
}

// createComponents 创建所有必要的组件
func createComponents(cfg *config.Config, storageManager *storage.Storage, logger *zerolog.Logger) (Components, error) {
	components := Components{
		Storage: storageManager,
	}

	// 创建Tika PDF提取器
	if cfg.Tika.ServerURL != "" {
		tikaOptions := []parser.TikaOption{
			parser.WithMinimalMetadata(true),
			parser.WithAnnotations(true),
		}

		if logger != nil {
			// 转换zerolog为标准log，因为Tika提取器使用标准log库
			stdLogger := log.New(
				zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
					w.NoColor = false
					w.TimeFormat = "15:04:05"
				}),
				"[TikaExtractor] ",
				log.LstdFlags,
			)
			tikaOptions = append(tikaOptions, parser.WithTikaLogger(stdLogger))
		}

		tikaTimeout := time.Duration(cfg.Tika.Timeout) * time.Second
		if tikaTimeout > 0 {
			tikaOptions = append(tikaOptions, parser.WithTimeout(tikaTimeout))
		}

		pdfExtractor := parser.NewTikaPDFExtractor(cfg.Tika.ServerURL, tikaOptions...)
		components.PDFExtractor = pdfExtractor
	}

	// 创建LLM简历分块器（如果有必要的配置）
	if storageManager != nil && cfg.Aliyun.APIKey != "" {
		// 创建千问模型
		qwenModel, err := agent.NewAliyunQwenChatModel(
			cfg.Aliyun.APIKey,
			cfg.GetModelForTask("resume_chunking"),
			cfg.Aliyun.APIURL,
		)
		if err == nil {
			stdLogger := log.New(os.Stdout, "[LLMChunker] ", log.LstdFlags)
			chunker := parser.NewLLMResumeChunker(qwenModel, stdLogger)
			components.ResumeChunker = chunker
		}
	}

	// 创建简历嵌入器
	if cfg.Aliyun.Embedding.Model != "" && cfg.Aliyun.APIKey != "" {
		aliyunEmbedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
		if err == nil {
			resumeEmbedder, embedErr := NewDefaultResumeEmbedder(aliyunEmbedder)
			if embedErr == nil {
				components.ResumeEmbedder = resumeEmbedder
			}
		}
	}

	// 创建岗位匹配评估器
	if cfg.Aliyun.APIKey != "" {
		// 为Job评估创建专门的千问模型
		qwenModel, err := agent.NewAliyunQwenChatModel(
			cfg.Aliyun.APIKey,
			cfg.GetModelForTask("job_evaluate"),
			cfg.Aliyun.APIURL,
		)
		if err == nil {
			stdLogger := log.New(os.Stdout, "[JobEvaluator] ", log.LstdFlags)
			evaluator := parser.NewLLMJobEvaluator(qwenModel, stdLogger)
			components.MatchEvaluator = evaluator
		}
	}

	return components, nil
}

// CheckComponentsInitialized 检查所有必要的组件是否已初始化
func (rs *resumeServiceImpl) CheckComponentsInitialized() error {
	if rs.components.Storage == nil {
		return ErrStorageNotInit
	}
	if rs.components.PDFExtractor == nil {
		return ErrExtractorNotInit
	}
	if rs.components.ResumeChunker == nil {
		return ErrChunkerNotInit
	}
	if rs.components.ResumeEmbedder == nil {
		return ErrEmbedderNotInit
	}
	if rs.components.MatchEvaluator == nil {
		return ErrEvaluatorNotInit
	}
	return nil
}

// ResumeServiceFacade 是新的Facade模式实现，用于替代旧的ResumeProcessor
// 它持有内部的ResumeService实现，并简单地将调用委托给它
type ResumeServiceFacade struct {
	service ResumeService
	config  *config.Config
	logger  *zerolog.Logger
}

// NewResumeServiceFacade 创建一个新的Facade，用于平滑过渡
func NewResumeServiceFacade(cfg *config.Config, storage *storage.Storage, logger *zerolog.Logger) (*ResumeServiceFacade, error) {
	// 创建内部服务实现
	service, err := NewResumeService(cfg, storage, logger)
	if err != nil {
		return nil, err
	}

	return &ResumeServiceFacade{
		service: service,
		config:  cfg,
		logger:  logger,
	}, nil
}

// ProcessUploadedResume 委托给内部服务
// 保持与旧ResumeProcessor相同的函数签名，以便平滑过渡
func (f *ResumeServiceFacade) ProcessUploadedResume(ctx context.Context, message storagetypes.ResumeUploadMessage, cfg *config.Config) error {
	// 简单地委托给内部服务实现
	return f.service.ProcessUploadedResume(ctx, message)
}

// ProcessLLMTasks 委托给内部服务
// 保持与旧ResumeProcessor相同的函数签名，以便平滑过渡
func (f *ResumeServiceFacade) ProcessLLMTasks(ctx context.Context, message storagetypes.ResumeProcessingMessage, cfg *config.Config) error {
	// 简单地委托给内部服务实现
	return f.service.ProcessLLMTasks(ctx, message)
}

// 兼容性函数，确保类型断言成功
func AsResumeProcessor(facade *ResumeServiceFacade) interface{} {
	return facade
}

// ProcessUploadedResume 处理上传的简历
// 实现ResumeService接口
func (rs *resumeServiceImpl) ProcessUploadedResume(ctx context.Context, message storagetypes.ResumeUploadMessage) error {
	// 创建span
	ctx, span := tracer.Start(ctx, "ProcessUploadedResume",
		trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	// 添加关键业务属性
	span.SetAttributes(
		attribute.String("submission_uuid", message.SubmissionUUID),
		attribute.String("source_channel", message.SourceChannel),
	)

	// 使用带trace信息的logger
	ctx = logger.WithSubmissionUUID(ctx, message.SubmissionUUID)
	log := logger.FromContext(ctx)

	log.Debug().Msg("开始处理上传的简历")

	// 检查组件初始化
	if rs.components.Storage == nil {
		span.RecordError(ErrStorageNotInit)
		span.SetStatus(codes.Error, "存储未初始化")
		return ErrStorageNotInit
	}
	if rs.components.PDFExtractor == nil {
		span.RecordError(ErrExtractorNotInit)
		span.SetStatus(codes.Error, "提取器未初始化")
		return ErrExtractorNotInit
	}

	// 使用数据库事务确保操作的原子性
	err := rs.components.Storage.MySQL.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 更新初始状态为 PENDING_PARSING
		if err := tx.Model(&models.ResumeSubmission{}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			Update("processing_status", constants.StatusPendingParsing).Error; err != nil {
			log.Error().Err(err).Msg("更新简历状态为PENDING_PARSING失败")
			return fmt.Errorf("更新状态为%s失败: %w", constants.StatusPendingParsing, err)
		}

		// 2. 解析并去重 - 创建子span
		ctx, parseSpan := tracer.Start(ctx, "ParseAndDeduplicateResume")
		text, textMD5Hex, err := rs.parseAndDeduplicateResume(ctx, tx, message)
		parseSpan.End()

		if err != nil {
			if errors.Is(err, ErrDuplicateContent) {
				log.Info().Msg("检测到重复内容，跳过处理")
				return nil // 内容重复是正常流程，提交状态更新并返回nil，事务将提交
			}
			return err // 其他错误则回滚事务
		}

		// 3. 上传解析后的文本到MinIO - 只记录事件而不创建子span
		span.AddEvent("uploading_to_minio")
		textObjectKey, err := rs.components.Storage.MinIO.UploadParsedText(ctx, message.SubmissionUUID, text)
		if err != nil {
			log.Error().Err(err).Msg("上传解析后的文本到MinIO失败")
			return fmt.Errorf("上传解析文本失败: %w", err)
		}
		log.Debug().Str("object_key", textObjectKey).Msg("解析文本已上传到MinIO")

		// 4. 构建下一个队列的消息
		processingMessage := storagetypes.ResumeProcessingMessage{
			SubmissionUUID:      message.SubmissionUUID,
			ParsedTextPathOSS:   textObjectKey,
			TargetJobID:         message.TargetJobID,
			ParsedTextObjectKey: textObjectKey, // 兼容旧字段
		}

		// 5. [Outbox] 将消息写入 Outbox 表，而不是直接发布
		ctx, outboxSpan := tracer.Start(ctx, "WriteToOutbox")
		payloadBytes, err := json.Marshal(processingMessage)
		if err != nil {
			log.Error().Err(err).Msg("序列化outbox payload失败")
			outboxSpan.RecordError(err)
			outboxSpan.SetStatus(codes.Error, "序列化失败")
			outboxSpan.End()
			return fmt.Errorf("序列化outbox payload失败: %w", err)
		}

		outboxEntry := models.OutboxMessage{
			AggregateID:      message.SubmissionUUID,
			EventType:        "resume.parsed",
			Payload:          string(payloadBytes),
			TargetExchange:   rs.config.RabbitMQ.ProcessingEventsExchange,
			TargetRoutingKey: rs.config.RabbitMQ.ParsedRoutingKey,
		}

		if err := tx.Create(&outboxEntry).Error; err != nil {
			log.Error().Err(err).Msg("插入outbox记录失败")
			outboxSpan.RecordError(err)
			outboxSpan.SetStatus(codes.Error, "插入失败")
			outboxSpan.End()
			return fmt.Errorf("插入outbox记录失败: %w", err)
		}
		outboxSpan.End()
		log.Debug().Msg("成功创建outbox记录")

		// 6. 更新数据库记录
		if err := tx.Model(&models.ResumeSubmission{}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			Updates(map[string]interface{}{
				"parsed_text_path_oss": textObjectKey,
				"raw_text_md5":         textMD5Hex,
				"processing_status":    constants.StatusQueuedForLLM,
				"parser_version":       rs.config.ActiveParserVersion,
			}).Error; err != nil {
			log.Error().Err(err).Msg("更新数据库记录失败")
			return fmt.Errorf("更新数据库失败: %w", err)
		}

		span.SetStatus(codes.Ok, "处理成功")
		return nil // 事务成功
	})

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		// 如果事务执行过程中发生错误，更新状态为失败
		updateErr := rs.components.Storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, constants.StatusUploadProcessingFailed)
		if updateErr != nil {
			log.Error().Err(updateErr).Msg("更新状态为失败时出错")
		}
		return err // 返回原始错误
	}

	log.Info().Msg("上传任务处理成功完成")
	return nil
}

// parseAndDeduplicateResume 内部辅助方法，解析并检查简历文本是否重复
func (rs *resumeServiceImpl) parseAndDeduplicateResume(ctx context.Context, tx *gorm.DB, message storagetypes.ResumeUploadMessage) (string, string, error) {
	// 获取带trace的日志
	log := logger.FromContext(ctx)
	span := trace.SpanFromContext(ctx)

	// 从MinIO获取原始简历文件
	originalFileBytes, err := rs.components.Storage.MinIO.GetResumeFile(ctx, message.OriginalFilePathOSS)
	if err != nil {
		log.Error().Err(err).Msg("从MinIO下载简历失败")
		span.SetAttributes(attribute.String("error.type", "download_failure"))
		return "", "", fmt.Errorf("下载简历失败: %w", err)
	}
	log.Debug().Int("size_bytes", len(originalFileBytes)).Msg("从MinIO下载简历成功")
	span.SetAttributes(attribute.Int("file_size_bytes", len(originalFileBytes)))

	// 提取文本
	text, _, err := rs.components.PDFExtractor.ExtractTextFromReader(ctx, bytes.NewReader(originalFileBytes), message.OriginalFilePathOSS, nil)
	if err != nil {
		log.Error().Err(err).Msg("提取简历文本失败")
		span.RecordError(err)
		span.SetAttributes(attribute.String("error.type", "extract_failure"))
		return "", "", fmt.Errorf("提取文本失败: %w", err)
	}
	log.Debug().Int("text_length", len(text)).Msg("成功提取文本")
	span.SetAttributes(attribute.Int("text_length", len(text)))

	// 记录一个事件表示文本提取完成
	span.AddEvent("text_extraction_completed")

	// 计算文本MD5用于去重
	textMD5Hex := utils.CalculateMD5([]byte(text))
	log.Debug().Str("md5", textMD5Hex).Msg("计算得到文本MD5")

	// 在Redis中原子地检查并添加文本MD5
	textExists, err := rs.components.Storage.Redis.CheckAndAddParsedTextMD5(ctx, textMD5Hex)
	if err != nil {
		log.Warn().Err(err).Msg("Redis检查文本MD5失败，将继续处理，但文本去重可能失效")
	} else if textExists {
		log.Info().Str("md5", textMD5Hex).Msg("检测到重复的文本MD5，标记为重复内容")
		if err := tx.Model(&models.ResumeSubmission{}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			Update("processing_status", constants.StatusContentDuplicateSkipped).Error; err != nil {
			return "", "", fmt.Errorf("更新重复内容状态失败: %w", err)
		}
		span.SetAttributes(
			attribute.Bool("duplicate_content", true),
			attribute.String("md5", textMD5Hex),
		)
		return "", "", ErrDuplicateContent
	}

	log.Debug().Msg("文本MD5不存在于Redis，继续处理")
	return text, textMD5Hex, nil
}

// ProcessLLMTasks 处理LLM任务
// 实现ResumeService接口
func (rs *resumeServiceImpl) ProcessLLMTasks(ctx context.Context, message storagetypes.ResumeProcessingMessage) error {
	// 创建span
	ctx, span := tracer.Start(ctx, "ProcessLLMTasks",
		trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	// 添加关键属性
	span.SetAttributes(
		attribute.String("submission_uuid", message.SubmissionUUID),
		attribute.String("target_job_id", message.TargetJobID),
	)

	// 使用带trace信息的logger
	ctx = logger.WithSubmissionUUID(ctx, message.SubmissionUUID)
	log := logger.FromContext(ctx).With().Str("method", "ProcessLLMTasks").Logger()

	log.Debug().Msg("开始处理LLM任务")

	// 检查组件初始化
	if rs.components.Storage == nil {
		span.RecordError(ErrStorageNotInit)
		span.SetStatus(codes.Error, "存储未初始化")
		return ErrStorageNotInit
	}
	if rs.components.ResumeChunker == nil {
		span.RecordError(ErrChunkerNotInit)
		span.SetStatus(codes.Error, "分块器未初始化")
		return ErrChunkerNotInit
	}
	if rs.components.ResumeEmbedder == nil {
		span.RecordError(ErrEmbedderNotInit)
		span.SetStatus(codes.Error, "嵌入器未初始化")
		return ErrEmbedderNotInit
	}

	// 使用事务来保证读取-更新的原子性和幂等性
	err := rs.components.Storage.MySQL.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 获取最新的 ResumeSubmission 记录并锁定，防止并发处理
		ctx, txSpan := tracer.Start(ctx, "GetAndLockSubmission")
		defer txSpan.End()
		var submission models.ResumeSubmission
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("submission_uuid = ?", message.SubmissionUUID).
			First(&submission).Error; err != nil {

			if errors.Is(err, gorm.ErrRecordNotFound) {
				log.Info().Msg("ResumeSubmission记录未找到，可能已被删除")
				txSpan.SetStatus(codes.Error, "记录不存在")
				return nil // 记录不存在，可能已被删除，直接确认消息
			}
			log.Error().Err(err).Msg("获取ResumeSubmission记录失败")
			txSpan.RecordError(err)
			txSpan.SetStatus(codes.Error, "查询失败")
			return fmt.Errorf("获取ResumeSubmission记录失败: %w", err)
		}

		// 2. 幂等性检查 - 使用常量集替代内联的状态检查
		if !constants.IsStatusAllowed(submission.ProcessingStatus, constants.AllowedStatusesForLLM) {
			log.Debug().Str("current_status", submission.ProcessingStatus).Msg("跳过重复/无效状态的消息")
			span.SetAttributes(
				attribute.String("skipped_reason", "invalid_status"),
				attribute.String("current_status", submission.ProcessingStatus),
			)
			return nil // 状态不匹配，说明是重复消息或已处理，直接确认并返回
		}

		// 3. 更新状态为 PENDING_LLM，表示开始处理
		if err := tx.WithContext(ctx).Model(&submission).Update("processing_status", constants.StatusPendingLLM).Error; err != nil {
			log.Error().Err(err).Msg("更新状态到PENDING_LLM失败")
			return fmt.Errorf("更新状态失败: %w", err)
		}

		return nil // 事务成功提交
	})

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "事务处理失败")
		return err // 直接返回错误
	}

	// --- 事务外执行IO操作 (下载文本，LLM分块，向量化，写入Qdrant) ---
	ctx, processSpan := tracer.Start(ctx, "DownloadAndChunkResume")
	parsedText, sections, basicInfo, err := rs.downloadAndChunkResume(ctx, message)
	processSpan.End()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "下载或分块失败")
		updateErr := rs.components.Storage.MySQL.UpdateResumeProcessingStatus(
			ctx, message.SubmissionUUID, constants.StatusChunkingFailed)
		if updateErr != nil {
			log.Error().Err(updateErr).Msg("更新状态为CHUNKING_FAILED失败")
		}
		return err
	}

	// 向量化
	ctx, embedSpan := tracer.Start(ctx, "EmbedChunks")
	chunkEmbeddings, err := rs.embedChunks(ctx, sections, basicInfo)
	embedSpan.End()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "向量化失败")
		updateErr := rs.components.Storage.MySQL.UpdateResumeProcessingStatus(
			ctx, message.SubmissionUUID, constants.StatusVectorizationFailed)
		if updateErr != nil {
			log.Error().Err(updateErr).Msg("更新状态为VECTORIZATION_FAILED失败")
		}
		return err
	}

	// 准备Qdrant数据
	ctx, prepareSpan := tracer.Start(ctx, "PrepareQdrantData")
	qdrantChunks, floatEmbeddings, err := rs.prepareQdrantData(message.SubmissionUUID, sections, basicInfo, chunkEmbeddings)
	prepareSpan.End()
	if err != nil {
		log.Error().Err(err).Msg("准备Qdrant数据失败")
		span.RecordError(err)
		span.SetStatus(codes.Error, "准备Qdrant数据失败")
		return err
	}

	// 执行Qdrant写入
	var pointIDs []string
	if rs.components.Storage.Qdrant != nil {
		log.Debug().Int("chunks_count", len(qdrantChunks)).Msg("开始存储向量到Qdrant")
		ctx, storeSpan := tracer.Start(ctx, "StoreVectorsToQdrant")
		storeSpan.SetAttributes(attribute.Int("chunk_count", len(qdrantChunks)))
		pointIDs, err = rs.components.Storage.Qdrant.StoreResumeVectors(ctx, message.SubmissionUUID, qdrantChunks, floatEmbeddings)
		if err != nil {
			log.Error().Err(err).Msg("存储向量到Qdrant失败")
			storeSpan.RecordError(err)
			storeSpan.SetStatus(codes.Error, "存储失败")
			storeSpan.End()
			updateErr := rs.components.Storage.MySQL.UpdateResumeProcessingStatus(
				ctx, message.SubmissionUUID, constants.StatusQdrantStoreFailed)
			if updateErr != nil {
				log.Error().Err(updateErr).Msg("更新状态为QDRANT_STORE_FAILED失败")
			}
			return err
		}
		storeSpan.SetAttributes(attribute.Int("stored_points_count", len(pointIDs)))
		storeSpan.End()
		log.Debug().Int("point_ids_count", len(pointIDs)).Msg("成功存储向量到Qdrant")
	} else {
		log.Warn().Msg("Qdrant未初始化，跳过向量存储")
	}

	// 使用事务来保证最终的数据库更新
	ctx, finalTxSpan := tracer.Start(ctx, "ExecuteFinalTransaction")
	defer finalTxSpan.End()
	err = rs.components.Storage.MySQL.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return rs.executeLLMProcessingTransaction(ctx, tx, message, qdrantChunks, floatEmbeddings, sections, basicInfo, parsedText, pointIDs)
	})

	if err != nil {
		log.Error().Err(err).Msg("LLM处理最终事务失败")
		span.RecordError(err)
		span.SetStatus(codes.Error, "最终事务失败")
		updateErr := rs.components.Storage.MySQL.UpdateResumeProcessingStatus(
			ctx, message.SubmissionUUID, constants.StatusLLMProcessingFailed)
		if updateErr != nil {
			log.Error().Err(updateErr).Msg("更新状态为LLM_PROCESSING_FAILED失败")
		}
		return err
	}

	span.SetStatus(codes.Ok, "处理成功")
	log.Info().Msg("LLM任务处理成功完成")
	return nil
}

// downloadAndChunkResume 下载简历文本并分块
func (rs *resumeServiceImpl) downloadAndChunkResume(ctx context.Context, message storagetypes.ResumeProcessingMessage) (string, []*types.ResumeSection, map[string]string, error) {
	log := rs.logger.With().
		Str("submission_uuid", message.SubmissionUUID).
		Str("method", "downloadAndChunkResume").
		Logger()

	// 1. 从MinIO下载解析后的文本
	parsedText, err := rs.components.Storage.MinIO.GetParsedText(ctx, message.ParsedTextPathOSS)
	if err != nil {
		log.Error().Err(err).Str("path", message.ParsedTextPathOSS).Msg("从MinIO下载解析文本失败")
		return "", nil, nil, fmt.Errorf("下载解析文本失败: %w", err)
	}
	log.Debug().Int("text_length", len(parsedText)).Msg("成功下载解析文本")

	// 2. 使用LLM进行简历分块
	sections, basicInfo, err := rs.components.ResumeChunker.ChunkResume(ctx, parsedText)
	if err != nil {
		log.Error().Err(err).Msg("LLM分块简历失败")
		return "", nil, nil, fmt.Errorf("LLM分块失败: %w", err)
	}
	log.Debug().
		Int("sections_count", len(sections)).
		Int("basic_info_count", len(basicInfo)).
		Msg("成功进行LLM分块")

	return parsedText, sections, basicInfo, nil
}

// embedChunks 对简历分块进行向量化
func (rs *resumeServiceImpl) embedChunks(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error) {
	log := rs.logger.With().
		Int("sections_count", len(sections)).
		Str("method", "embedChunks").
		Logger()

	if rs.components.ResumeEmbedder == nil {
		log.Error().Msg("嵌入器未初始化")
		return nil, ErrEmbedderNotInit
	}

	// 筛选非 BASIC_INFO 类型的分块
	// 注释：我们可以选择在这里过滤掉 BASIC_INFO 分块，但为了保持向后兼容，
	// 我们依然对所有分块进行向量化，并在 prepareQdrantData 中进行过滤。
	// 未来可以取消下面的注释，启用过滤，以节约API调用和计算资源。
	/*
		var sectionsToEmbed []*types.ResumeSection
		for _, section := range sections {
			if section.Type != types.SectionBasicInfo {
				sectionsToEmbed = append(sectionsToEmbed, section)
			} else {
				log.Debug().
					Int("chunk_id", section.ChunkID).
					Msg("在向量化阶段跳过BASIC_INFO分块")
			}
		}

		if len(sectionsToEmbed) == 0 {
			log.Warn().Msg("过滤BASIC_INFO后没有可向量化的分块")
			return nil, fmt.Errorf("没有可向量化的分块")
		}

		// 调用嵌入器进行向量化，只处理非 BASIC_INFO 分块
		return rs.components.ResumeEmbedder.Embed(ctx, sectionsToEmbed, basicInfo)
	*/

	// 当前实现：对所有分块进行向量化
	startTime := time.Now()
	chunkEmbeddings, err := rs.components.ResumeEmbedder.Embed(ctx, sections, basicInfo)
	if err != nil {
		log.Error().Err(err).Msg("向量嵌入失败")
		return nil, fmt.Errorf("嵌入失败: %w", err)
	}

	log.Debug().
		Int("embeddings_count", len(chunkEmbeddings)).
		Float64("elapsed_seconds", time.Since(startTime).Seconds()).
		Msg("向量嵌入完成")

	return chunkEmbeddings, nil
}

// prepareQdrantData 准备用于Qdrant的数据
func (rs *resumeServiceImpl) prepareQdrantData(submissionUUID string, sections []*types.ResumeSection, basicInfo map[string]string, chunkEmbeddings []*types.ResumeChunkVector) ([]types.ResumeChunk, [][]float64, error) {
	log := rs.logger.With().
		Str("submission_uuid", submissionUUID).
		Str("method", "prepareQdrantData").
		Logger()

	// 将经过LLM分块和向量化的信息转换为Qdrant存储格式
	var qdrantChunks []types.ResumeChunk
	var floatEmbeddings [][]float64

	if len(chunkEmbeddings) == 0 {
		log.Warn().Msg("没有要存储的向量")
		return qdrantChunks, floatEmbeddings, nil
	}

	// 提取基本信息中的唯一标识符
	identifiers := types.Identity{
		Name:  basicInfo["name"],
		Phone: basicInfo["phone"],
		Email: basicInfo["email"],
	}

	// 提取基本信息中的元数据
	metadata := types.ChunkMetadata{
		ExperienceYears: parseExperienceYears(basicInfo["years_of_experience"]),
		EducationLevel:  basicInfo["highest_education"],
	}

	// 为每个分块创建Qdrant条目
	for _, embedding := range chunkEmbeddings {
		if embedding.Section == nil || len(embedding.Vector) == 0 {
			log.Warn().Msg("跳过无效的嵌入向量")
			continue
		}

		// 跳过BASIC_INFO类型的分块，不存储到向量数据库
		if embedding.Section.Type == types.SectionBasicInfo {
			log.Debug().
				Int("chunk_id", embedding.Section.ChunkID).
				Msg("跳过BASIC_INFO分块，不存入向量数据库")
			continue
		}

		// 创建Qdrant chunk结构
		chunk := types.ResumeChunk{
			ChunkID:           embedding.Section.ChunkID,
			ChunkType:         string(embedding.Section.Type),
			Content:           embedding.Section.Content,
			ImportanceScore:   1.0, // 默认重要性分数
			UniqueIdentifiers: identifiers,
			Metadata:          metadata,
		}

		qdrantChunks = append(qdrantChunks, chunk)
		floatEmbeddings = append(floatEmbeddings, embedding.Vector)
	}

	if len(qdrantChunks) == 0 {
		log.Warn().Msg("过滤BASIC_INFO后没有可存储的分块")
	} else {
		log.Debug().
			Int("chunks_count", len(qdrantChunks)).
			Int("embeddings_count", len(floatEmbeddings)).
			Msg("成功准备Qdrant数据")
	}

	return qdrantChunks, floatEmbeddings, nil
}

// parseExperienceYears 解析经验年限字符串为整数
func parseExperienceYears(yearsStr string) int {
	if yearsStr == "" {
		return 0
	}

	// 尝试解析为浮点数，然后转为整数
	years, err := strconv.ParseFloat(yearsStr, 64)
	if err != nil {
		return 0
	}

	// 四舍五入到整数
	return int(math.Round(years))
}

// executeLLMProcessingTransaction 执行LLM处理的最终事务
func (rs *resumeServiceImpl) executeLLMProcessingTransaction(
	ctx context.Context,
	tx *gorm.DB,
	message storagetypes.ResumeProcessingMessage,
	qdrantChunks []types.ResumeChunk,
	floatEmbeddings [][]float64,
	sections []*types.ResumeSection,
	basicInfo map[string]string,
	parsedText string,
	pointIDs []string,
) error {
	logger := rs.logger.With().
		Str("submission_uuid", message.SubmissionUUID).
		Str("method", "executeLLMProcessingTransaction").
		Logger()

	// 1. 保存分块到MySQL
	if err := rs.components.Storage.MySQL.SaveResumeChunks(tx, message.SubmissionUUID, sections, pointIDs); err != nil {
		logger.Error().Err(err).Msg("保存简历分块到MySQL失败")
		return fmt.Errorf("保存分块失败: %w", err)
	}

	// 2. 保存基本信息到MySQL
	if err := rs.components.Storage.MySQL.SaveResumeBasicInfo(tx, message.SubmissionUUID, basicInfo); err != nil {
		logger.Error().Err(err).Msg("保存基本信息到MySQL失败")
		return fmt.Errorf("保存基本信息失败: %w", err)
	}

	// 3. 如果有目标岗位ID，则保存评估结果
	if message.TargetJobID != "" {
		if err := rs.saveMatchEvaluation(ctx, tx, message, parsedText); err != nil {
			logger.Error().Err(err).Str("target_job_id", message.TargetJobID).Msg("保存匹配评估失败")
			// 继续处理，不因为评估失败而中断整个流程
		}
	} else {
		logger.Debug().Msg("没有目标岗位ID，跳过匹配评估")
	}

	// 4. 准备最终更新
	// 把重要信息编码为JSON，以便在Dashboard UI中展示
	updates := rs.prepareFinalDBUpdates(basicInfo, pointIDs)

	// 5. 更新最终状态
	if err := tx.Model(&models.ResumeSubmission{}).
		Where("submission_uuid = ?", message.SubmissionUUID).
		Updates(updates).Error; err != nil {
		logger.Error().Err(err).Msg("更新最终状态到MySQL失败")
		return fmt.Errorf("更新最终状态失败: %w", err)
	}

	logger.Debug().Msg("成功执行LLM处理事务")
	return nil
}

// prepareFinalDBUpdates 准备最终的数据库更新内容
func (rs *resumeServiceImpl) prepareFinalDBUpdates(basicInfo map[string]string, pointIDs []string) map[string]interface{} {
	// 从基本信息中提取
	updates := map[string]interface{}{
		"processing_status": constants.StatusProcessingCompleted, // 处理完成状态
	}

	// 将candidate_id从空指针更新为具体值
	if nameEmail := basicInfo["name"] + "_" + basicInfo["email"]; nameEmail != "_" {
		updates["llm_resume_identifier"] = nameEmail
	}

	// 如果有基本信息，则序列化为JSON
	if len(basicInfo) > 0 {
		basicInfoJSON, err := json.Marshal(basicInfo)
		if err == nil {
			updates["llm_parsed_basic_info"] = string(basicInfoJSON)
		}
	}

	// 如果有Qdrant点ID，则序列化为JSON
	if len(pointIDs) > 0 {
		pointIDsJSON, err := json.Marshal(pointIDs)
		if err == nil {
			updates["qdrant_point_ids"] = string(pointIDsJSON)
		}
	}

	return updates
}

// saveMatchEvaluation 保存人岗匹配评估结果
func (rs *resumeServiceImpl) saveMatchEvaluation(ctx context.Context, tx *gorm.DB, message storagetypes.ResumeProcessingMessage, parsedText string) error {
	log := rs.logger.With().
		Str("submission_uuid", message.SubmissionUUID).
		Str("target_job_id", message.TargetJobID).
		Str("method", "saveMatchEvaluation").
		Logger()

	// 检查评估器是否初始化
	if rs.components.MatchEvaluator == nil {
		log.Warn().Msg("匹配评估器未初始化，跳过评估")
		return ErrEvaluatorNotInit
	}

	// 1. 获取岗位描述
	job, err := rs.components.Storage.MySQL.GetJobByID(tx, message.TargetJobID)
	if err != nil {
		log.Error().Err(err).Msg("获取岗位信息失败")
		return fmt.Errorf("获取岗位信息失败: %w", err)
	}

	if job.JobDescriptionText == "" {
		log.Warn().Msg("岗位描述为空，跳过评估")
		return fmt.Errorf("岗位描述为空")
	}

	// 2. 调用匹配评估器 - 创建专门的评估span
	ctx, evalSpan := tracer.Start(ctx, "MatchEvaluator.EvaluateMatch",
		trace.WithAttributes(
			attribute.String("job_id", message.TargetJobID),
			attribute.String("submission_uuid", message.SubmissionUUID),
			attribute.String("llm_model", rs.config.GetModelForTask("job_evaluate")),
		))

	evalSpan.SetAttributes(
		attribute.Int("resume_text_length", len(parsedText)),
		attribute.Int("job_text_length", len(job.JobDescriptionText)),
	)

	evaluation, err := rs.components.MatchEvaluator.EvaluateMatch(ctx, job.JobDescriptionText, parsedText)
	if err != nil {
		log.Error().Err(err).Msg("执行匹配评估失败")
		evalSpan.RecordError(err)
		evalSpan.SetStatus(codes.Error, "evaluation failed")
		evalSpan.End()
		return fmt.Errorf("执行匹配评估失败: %w", err)
	}

	// 记录评估结果的主要指标
	evalSpan.SetAttributes(
		attribute.Int("match_score", evaluation.MatchScore),
		attribute.Int("highlights_count", len(evaluation.MatchHighlights)),
		attribute.Int("gaps_count", len(evaluation.PotentialGaps)),
		attribute.Int("summary_length", len(evaluation.ResumeSummaryForJD)),
	)
	evalSpan.SetStatus(codes.Ok, "evaluation successful")
	evalSpan.End()

	// 3. 序列化评估结果的一部分字段
	highlightsJSON, _ := json.Marshal(evaluation.MatchHighlights)
	gapsJSON, _ := json.Marshal(evaluation.PotentialGaps)

	// 4. 保存评估结果
	match := &models.JobSubmissionMatch{
		SubmissionUUID:         message.SubmissionUUID,
		JobID:                  message.TargetJobID,
		LLMMatchScore:          &evaluation.MatchScore,
		LLMMatchHighlightsJSON: highlightsJSON,
		LLMPotentialGapsJSON:   gapsJSON,
		LLMResumeSummaryForJD:  evaluation.ResumeSummaryForJD,
		EvaluationStatus:       "COMPLETED",
		EvaluatedAt:            utils.TimePtr(time.Now()),
	}

	if err := rs.components.Storage.MySQL.CreateJobSubmissionMatch(tx, match); err != nil {
		log.Error().Err(err).Msg("保存匹配结果到MySQL失败")
		return fmt.Errorf("保存匹配结果失败: %w", err)
	}

	log.Debug().
		Int("match_score", evaluation.MatchScore).
		Int("highlights_count", len(evaluation.MatchHighlights)).
		Msg("成功保存匹配评估结果")

	return nil
}
