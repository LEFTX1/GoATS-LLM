package handler

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	"ai-agent-go/internal/logger"
	"ai-agent-go/internal/processor"
	storage2 "ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/pkg/utils"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/gofrs/uuid/v5"
)

// ResumeHandler 简历处理器，负责协调简历的处理流程
type ResumeHandler struct {
	cfg             *config.Config
	storage         *storage2.Storage          // 使用聚合的storage实例替换独立的适配器
	processorModule *processor.ResumeProcessor // 使用组件聚合类
}

// NewResumeHandler 创建一个新的简历处理器
func NewResumeHandler(
	cfg *config.Config,
	storage *storage2.Storage, // 接收聚合的storage实例
	processorModule *processor.ResumeProcessor, // 只接收组件聚合类
) *ResumeHandler {
	return &ResumeHandler{
		cfg:             cfg,
		storage:         storage, // 初始化聚合的storage实例
		processorModule: processorModule,
	}
}

// ResumeUploadResponse 简历上传响应
type ResumeUploadResponse struct {
	SubmissionUUID string `json:"submission_uuid"`
	Status         string `json:"status"`
}

// HandleResumeUpload 处理简历上传请求
func (h *ResumeHandler) HandleResumeUpload(c context.Context, ctx *app.RequestContext) {

	// 获取上传的文件
	fileHeader, err := ctx.FormFile("file")
	if err != nil {
		logger.Error().Err(err).Msg("获取上传文件失败")
		ctx.JSON(http.StatusBadRequest, map[string]interface{}{"error": "文件未找到或获取失败"})
		return
	}

	// 获取目标岗位ID
	targetJobID := ctx.PostForm("target_job_id")
	// 获取来源渠道
	sourceChannel := ctx.PostForm("source_channel")
	if sourceChannel == "" {
		sourceChannel = "web_upload" // 默认值
	}

	file, err := fileHeader.Open()
	if err != nil {
		logger.Error().Err(err).Msg("打开上传文件失败")
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "打开文件失败"})
		return
	}
	defer file.Close()

	// 1. 生成UUIDv7
	uuidV7, err := uuid.NewV7()
	if err != nil {
		logger.Error().Err(err).Msg("生成UUIDv7失败")
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "生成UUID失败"})
		return
	}
	submissionUUID := uuidV7.String()

	// 2. 获取文件扩展名
	ext := filepath.Ext(fileHeader.Filename)
	if ext == "" {
		ext = ".pdf" // 默认为PDF
	}

	// 3. 流式上传文件到MinIO并同时计算MD5
	originalObjectKey, fileMD5Hex, err := h.storage.MinIO.UploadResumeFileStreaming(c, submissionUUID, ext, file, fileHeader.Size)
	if err != nil {
		logger.Error().Err(err).Str("submission_uuid", submissionUUID).Msg("流式上传简历到MinIO失败")
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "上传简历到MinIO失败"})
		return
	}

	// 4. 使用原子操作检查并添加文件MD5
	exists, err := h.storage.Redis.CheckAndAddRawFileMD5(c, fileMD5Hex)
	if err != nil {
		logger.Error().
			Err(err).
			Str("md5", fileMD5Hex).
			Msg("检查并添加文件MD5失败")
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "检查文件MD5重复性时Redis操作失败"})
		return
	}

	if exists {
		logger.Info().
			Str("md5", fileMD5Hex).
			Str("filename", fileHeader.Filename).
			Msg("检测到重复的文件MD5，跳过处理")

		// 异步删除已上传的重复文件，避免存储资源浪费
		go func(key string) {
			// 创建一个独立的后台上下文，避免受HTTP请求生命周期影响
			// 为删除操作设置一个合理的超时时间
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := h.storage.MinIO.DeleteResumeFile(bgCtx, key); err != nil {
				logger.Error().Err(err).Str("objectKey", key).Msg("异步删除MinIO中的重复文件失败")
			} else {
				logger.Info().Str("objectKey", key).Msg("成功异步删除MinIO中的重复文件")
			}
		}(originalObjectKey)

		// 返回409 Conflict（语义上更正确）而不是200 OK
		ctx.JSON(http.StatusConflict, ResumeUploadResponse{
			SubmissionUUID: "",
			Status:         constants.StatusDuplicateFileSkipped,
		})
		return
	}

	// 5. 构建消息并发送到RabbitMQ
	message := storage2.ResumeUploadMessage{
		SubmissionUUID:      submissionUUID,
		OriginalFilePathOSS: originalObjectKey,
		OriginalFilename:    fileHeader.Filename,
		TargetJobID:         targetJobID,
		SourceChannel:       sourceChannel,
		SubmissionTimestamp: time.Now(),
		RawFileMD5:          fileMD5Hex, // 填充MD5，用于失败场景的回滚

		// 兼容性字段 (保留以向后兼容旧版客户端)
		OriginalFileObjectKey: originalObjectKey,
	}

	// 发布消息到上传交换机
	err = h.storage.RabbitMQ.PublishJSON(
		c, // Changed 'ctx' to 'c'
		h.cfg.RabbitMQ.ResumeEventsExchange,
		h.cfg.RabbitMQ.UploadedRoutingKey,
		message,
		true, // 持久化
	)
	if err != nil {
		logger.Error().Err(err).Str("submission_uuid", submissionUUID).Msg("发布消息到RabbitMQ失败")
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "发布消息到RabbitMQ失败"})
		return
	}

	// 6. 返回响应
	ctx.JSON(http.StatusOK, ResumeUploadResponse{
		SubmissionUUID: submissionUUID,
		Status:         constants.StatusSubmittedForProcessing,
	})
}

// StartResumeUploadConsumer 启动简历上传消费者
func (h *ResumeHandler) StartResumeUploadConsumer(ctx context.Context, batchSize int, batchTimeout time.Duration) error {
	if h.storage == nil || h.storage.RabbitMQ == nil {
		logger.Warn().Msg("RabbitMQ client is nil, skipping resume upload consumer start.")
		return nil // Not an error, just skipping in environments without MQ
	}
	// 添加日志打印当前配置中的交换机名称
	logger.Info().
		Str("exchange", h.cfg.RabbitMQ.ResumeEventsExchange).
		Str("routing_key", h.cfg.RabbitMQ.UploadedRoutingKey).
		Msg("初始化RabbitMQ配置")

	// 1. 确保交换机和队列存在
	if err := h.storage.RabbitMQ.EnsureExchange(h.cfg.RabbitMQ.ResumeEventsExchange, "direct", true); err != nil {
		return fmt.Errorf("确保交换机存在失败: %w", err)
	}

	if err := h.storage.RabbitMQ.EnsureQueue(h.cfg.RabbitMQ.RawResumeQueue, true); err != nil {
		return fmt.Errorf("确保队列存在失败: %w", err)
	}

	if err := h.storage.RabbitMQ.BindQueue(
		h.cfg.RabbitMQ.RawResumeQueue,
		h.cfg.RabbitMQ.ResumeEventsExchange,
		h.cfg.RabbitMQ.UploadedRoutingKey,
	); err != nil {
		return fmt.Errorf("绑定队列失败: %w", err)
	}

	logger.Info().
		Str("queue", h.cfg.RabbitMQ.RawResumeQueue).
		Int("batch_size", batchSize).
		Dur("batch_timeout", batchTimeout).
		Msg("简历上传消费者就绪")

	// 启动消费者
	_, err := h.storage.RabbitMQ.StartConsumer(h.cfg.RabbitMQ.RawResumeQueue, batchSize, func(data []byte) bool {
		// 这里需要实现单个消息的处理逻辑
		var message storage2.ResumeUploadMessage
		if err := json.Unmarshal(data, &message); err != nil {
			logger.Error().Err(err).Msg("解析消息失败")
			return false
		}

		// 新逻辑：调用 Processor 进行处理
		// 首先，仍然需要将初始提交记录写入数据库，这部分逻辑可以保留在Handler或移至Processor的第一步
		submission := models.ResumeSubmission{
			SubmissionUUID:      message.SubmissionUUID,
			OriginalFilePathOSS: message.OriginalFilePathOSS,
			OriginalFilename:    message.OriginalFilename,
			TargetJobID:         utils.StringPtr(message.TargetJobID),
			SourceChannel:       message.SourceChannel,
			SubmissionTimestamp: message.SubmissionTimestamp,
			ProcessingStatus:    constants.StatusPendingParsing, // 初始状态
		}
		if err := h.storage.MySQL.BatchInsertResumeSubmissions(ctx, []models.ResumeSubmission{submission}); err != nil {
			logger.Error().Err(err).Str("submission_uuid", message.SubmissionUUID).Msg("插入初始简历提交记录失败")

			// 补偿操作：如果DB插入失败，则从Redis中移除之前添加的MD5，以允许重试
			if message.RawFileMD5 != "" {
				go func(md5ToRollback string) {
					// 创建一个独立的后台上下文，以防主上下文已取消
					bgCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					if err := h.storage.Redis.RemoveRawFileMD5(bgCtx, md5ToRollback); err != nil {
						logger.Error().Err(err).Str("md5", md5ToRollback).Msg("补偿操作：异步删除Redis中的文件MD5失败")
					} else {
						logger.Info().Str("md5", md5ToRollback).Msg("补偿操作：成功异步删除Redis中的文件MD5")
					}
				}(message.RawFileMD5)
			}

			return false // 插入失败，不继续
		}

		if err := h.processorModule.ProcessUploadedResume(ctx, message, h.cfg); err != nil {
			logger.Error().
				Err(err).
				Str("submission_uuid", message.SubmissionUUID).
				Msg("ResumeProcessor 处理上传简历失败")
			// 错误状态的更新现在由 ProcessUploadedResume 内部或其调用者（如果需要更细致的错误分类）处理
			// 此处仅标记消息处理失败，以便 MQ 进行重试或死信处理
			// 考虑: 如果 ProcessUploadedResume 内部已经更新了失败状态，这里是否还需要更新？
			// 为避免重复更新或状态冲突，让 Processor 内部处理其流程中的状态更新。
			// 如果 Processor 报错，通常意味着最终状态可能是某种 FAILED 状态。
			// Handler层面主要关注是否ack/nack消息。
			// 也可以在此处统一更新为更通用的上传处理失败状态，如果Processor没有处理所有错误路径的话。
			// 例如: h.storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "UPLOAD_PROCESSING_FAILED")
			return false
		}

		return true
	})

	if err != nil {
		return fmt.Errorf("启动消费者失败: %w", err)
	}

	return nil
}

// StartLLMParsingConsumer 启动LLM解析消费者
func (h *ResumeHandler) StartLLMParsingConsumer(ctx context.Context, prefetchCount int) error {
	if h.storage == nil || h.storage.RabbitMQ == nil {
		logger.Warn().Msg("RabbitMQ client is nil, skipping LLM parsing consumer start.")
		return nil // Not an error, just skipping in environments without MQ
	}
	// 1. 确保交换机和队列存在
	if err := h.storage.RabbitMQ.EnsureExchange(h.cfg.RabbitMQ.ProcessingEventsExchange, "direct", true); err != nil {
		return fmt.Errorf("确保交换机存在失败: %w", err)
	}

	if err := h.storage.RabbitMQ.EnsureQueue(h.cfg.RabbitMQ.LLMParsingQueue, true); err != nil {
		return fmt.Errorf("确保队列存在失败: %w", err)
	}

	if err := h.storage.RabbitMQ.BindQueue(
		h.cfg.RabbitMQ.LLMParsingQueue,
		h.cfg.RabbitMQ.ProcessingEventsExchange,
		h.cfg.RabbitMQ.ParsedRoutingKey,
	); err != nil {
		return fmt.Errorf("绑定队列失败: %w", err)
	}

	logger.Info().
		Str("queue", h.cfg.RabbitMQ.LLMParsingQueue).
		Int("prefetch_count", prefetchCount).
		Msg("LLM解析消费者就绪")

	// 2. 启动消费者
	_, err := h.storage.RabbitMQ.StartConsumer(h.cfg.RabbitMQ.LLMParsingQueue, prefetchCount, func(data []byte) bool {
		var message storage2.ResumeProcessingMessage
		if err := json.Unmarshal(data, &message); err != nil {
			logger.Error().
				Err(err).
				Msg("解析消息失败")
			return false
		}

		// 使用协程池处理任务 (旧逻辑)
		// if err := h.ParallelProcessResumeTask(ctx, message); err != nil {
		// 	logger.Error().
		// 		Err(err).
		// 		Str("submissionUUID", message.SubmissionUUID).
		// 		Msg("处理简历任务失败")
		// 	return false
		// }

		// 新逻辑: 调用 Processor 进行处理
		if err := h.processorModule.ProcessLLMTasks(ctx, message, h.cfg); err != nil {
			logger.Error().
				Err(err).
				Str("submissionUUID", message.SubmissionUUID).
				Msg("ResumeProcessor 处理LLM任务失败")
			// 错误状态的更新现在由 ProcessLLMTasks 内部或其调用者处理
			return false
		}

		return true
	})

	if err != nil {
		return fmt.Errorf("启动消费者失败: %w", err)
	}

	return nil
}

// StartMD5CleanupTask 启动MD5记录清理任务
// 此方法可选调用，用于定期检查和重置MD5记录的过期时间
func (h *ResumeHandler) StartMD5CleanupTask(ctx context.Context) {
	// 定义清理间隔，默认每周执行一次
	cleanupInterval := 7 * 24 * time.Hour

	logger.Info().
		Dur("interval", cleanupInterval).
		Msg("启动MD5记录清理任务")

	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	// 立即执行一次清理
	h.cleanupMD5Records(ctx)

	for {
		select {
		case <-ticker.C:
			h.cleanupMD5Records(ctx)
		case <-ctx.Done():
			logger.Info().Msg("MD5记录清理任务退出")
			return
		}
	}
}

// cleanupMD5Records 清理MD5记录
// 此方法会检查MD5集合是否有过期时间，如果没有则设置
func (h *ResumeHandler) cleanupMD5Records(ctx context.Context) {
	logger.Info().Msg("执行MD5记录清理任务...")

	// 使用新的内部常量来检查和设置文件MD5集合的过期时间
	ttlFile, errFile := h.storage.Redis.Client.TTL(ctx, constants.RawFileMD5SetKey).Result()
	if errFile != nil {
		logger.Error().Err(errFile).Str("setKey", constants.RawFileMD5SetKey).Msg("获取文件MD5集合过期时间失败")
	} else if ttlFile < 0 {
		expiry := h.storage.Redis.GetMD5ExpireDuration()
		if err := h.storage.Redis.Client.Expire(ctx, constants.RawFileMD5SetKey, expiry).Err(); err != nil {
			logger.Error().Err(err).Str("setKey", constants.RawFileMD5SetKey).Msg("设置文件MD5集合过期时间失败")
		} else {
			logger.Info().Str("setKey", constants.RawFileMD5SetKey).Dur("expiry", expiry).Msg("成功设置文件MD5集合过期时间")
		}
	}

	// 使用新的内部常量来检查和设置文本MD5集合的过期时间
	ttlText, errText := h.storage.Redis.Client.TTL(ctx, constants.ParsedTextMD5SetKey).Result()
	if errText != nil {
		logger.Error().Err(errText).Str("setKey", constants.ParsedTextMD5SetKey).Msg("获取文本MD5集合过期时间失败")
	} else if ttlText < 0 {
		expiry := h.storage.Redis.GetMD5ExpireDuration()
		if err := h.storage.Redis.Client.Expire(ctx, constants.ParsedTextMD5SetKey, expiry).Err(); err != nil {
			logger.Error().Err(err).Str("setKey", constants.ParsedTextMD5SetKey).Msg("设置文本MD5集合过期时间失败")
		} else {
			logger.Info().Str("setKey", constants.ParsedTextMD5SetKey).Dur("expiry", expiry).Msg("成功设置文本MD5集合过期时间")
		}
	}

	logger.Info().Msg("MD5记录清理任务完成")
}
