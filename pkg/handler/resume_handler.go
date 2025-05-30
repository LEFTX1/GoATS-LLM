package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"time"

	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/parser"
	"ai-agent-go/pkg/storage"
	"ai-agent-go/pkg/storage/models" // 导入GORM模型

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"
)

// StringPtr 返回字符串的指针
func StringPtr(s string) *string {
	return &s
}

// ResumeHandler 简历处理器，负责协调简历的处理流程
type ResumeHandler struct {
	cfg          *config.Config
	minioAdapter *storage.MinIOAdapter
	mqAdapter    *storage.RabbitMQAdapter
	mysqlAdapter *storage.MySQLAdapter
	pdfExtractor *parser.EinoPDFTextExtractor
	llmChunker   *parser.LLMResumeChunker
}

// NewResumeHandler 创建一个新的简历处理器
func NewResumeHandler(
	cfg *config.Config,
	minioAdapter *storage.MinIOAdapter,
	mqAdapter *storage.RabbitMQAdapter,
	mysqlAdapter *storage.MySQLAdapter,
	pdfExtractor *parser.EinoPDFTextExtractor,
	llmChunker *parser.LLMResumeChunker,
) *ResumeHandler {
	return &ResumeHandler{
		cfg:          cfg,
		minioAdapter: minioAdapter,
		mqAdapter:    mqAdapter,
		mysqlAdapter: mysqlAdapter,
		pdfExtractor: pdfExtractor,
		llmChunker:   llmChunker,
	}
}

// ResumeUploadResponse 简历上传响应
type ResumeUploadResponse struct {
	SubmissionUUID string `json:"submission_uuid"`
	Status         string `json:"status"`
}

// HandleResumeUpload 处理简历上传请求
func (h *ResumeHandler) HandleResumeUpload(ctx context.Context, reader io.Reader, fileSize int64,
	filename string, targetJobID string, sourceChannel string) (*ResumeUploadResponse, error) {
	// 1. 生成UUIDv7
	submissionUUID := uuid.NewString()

	// 2. 获取文件扩展名
	ext := filepath.Ext(filename)
	if ext == "" {
		ext = ".pdf" // 默认为PDF
	}

	// 3. 上传原始文件到MinIO
	originalObjectKey, err := h.minioAdapter.UploadResumeFile(ctx, submissionUUID, ext, reader, fileSize)
	if err != nil {
		return nil, fmt.Errorf("上传简历到MinIO失败: %w", err)
	}

	// 4. 构建消息并发送到RabbitMQ
	message := storage.ResumeUploadMessage{
		SubmissionUUID:        submissionUUID,
		OriginalFileObjectKey: originalObjectKey,
		OriginalFilename:      filename,
		TargetJobID:           targetJobID,
		SourceChannel:         sourceChannel,
		ReceivedTimestamp:     time.Now(),
	}

	// 发布消息到上传交换机
	err = h.mqAdapter.PublishJSON(
		ctx,
		h.cfg.RabbitMQ.ResumeEventsExchange,
		h.cfg.RabbitMQ.UploadedRoutingKey,
		message,
		true, // 持久化
	)
	if err != nil {
		return nil, fmt.Errorf("发布消息到RabbitMQ失败: %w", err)
	}

	// 5. 返回响应
	return &ResumeUploadResponse{
		SubmissionUUID: submissionUUID,
		Status:         "SUBMITTED_FOR_PROCESSING",
	}, nil
}

// StartResumeUploadConsumer 启动简历上传消费者
func (h *ResumeHandler) StartResumeUploadConsumer(ctx context.Context, batchSize int, batchTimeout time.Duration) error {
	// 添加日志打印当前配置中的交换机名称
	log.Printf("使用的交换机名称: '%s'", h.cfg.RabbitMQ.ResumeEventsExchange)
	log.Printf("使用的路由键: '%s'", h.cfg.RabbitMQ.UploadedRoutingKey)

	// 1. 确保交换机和队列存在
	if err := h.mqAdapter.EnsureExchange(h.cfg.RabbitMQ.ResumeEventsExchange, "direct", true); err != nil {
		return fmt.Errorf("确保交换机存在失败: %w", err)
	}

	queue, err := h.mqAdapter.EnsureQueue(h.cfg.RabbitMQ.RawResumeQueue, true)
	if err != nil {
		return fmt.Errorf("确保队列存在失败: %w", err)
	}

	if err := h.mqAdapter.BindQueue(
		h.cfg.RabbitMQ.RawResumeQueue,
		h.cfg.RabbitMQ.ResumeEventsExchange,
		h.cfg.RabbitMQ.UploadedRoutingKey,
	); err != nil {
		return fmt.Errorf("绑定队列失败: %w", err)
	}

	log.Printf("简历上传消费者就绪，监听队列 %s，批处理大小：%d，批处理超时：%v", queue.Name, batchSize, batchTimeout)

	// 2. 启动批量消费者
	handler := func(deliveries []*amqp.Delivery) ([]*amqp.Delivery, error) {
		log.Printf("收到 %d 条简历上传消息", len(deliveries))
		failedDeliveries := []*amqp.Delivery{}

		// 2.1 将消息数据收集到批次缓冲区
		messages := make([]storage.ResumeUploadMessage, 0, len(deliveries))
		for _, delivery := range deliveries {
			var message storage.ResumeUploadMessage
			if err := json.Unmarshal(delivery.Body, &message); err != nil {
				log.Printf("解析消息失败: %v", err)
				failedDeliveries = append(failedDeliveries, delivery)
				continue
			}
			messages = append(messages, message)
		}

		// 2.2 批量插入数据库
		if err := h.batchInsertResumeSubmissionsGORM(ctx, messages); err != nil {
			log.Printf("批量插入简历提交记录失败 (GORM): %v", err)
			// 如果批量插入失败，所有消息都需要重新处理
			return deliveries, err
		}

		// 2.3 逐个处理文本提取
		for _, message := range messages {
			// 处理简历文本提取
			if err := h.processResumeText(ctx, message); err != nil {
				log.Printf("处理简历文本失败: %v, submissionUUID: %s", err, message.SubmissionUUID)

				// 查找对应的delivery
				for _, delivery := range deliveries {
					var d storage.ResumeUploadMessage
					if json.Unmarshal(delivery.Body, &d) == nil && d.SubmissionUUID == message.SubmissionUUID {
						failedDeliveries = append(failedDeliveries, delivery)
						break
					}
				}

				// 更新状态为处理失败
				h.updateResumeStatusGORM(message.SubmissionUUID, "TEXT_EXTRACTION_FAILED")
			}
		}

		return failedDeliveries, nil
	}

	// 启动批量消费者
	_, err = h.mqAdapter.StartBatchConsumer(h.cfg.RabbitMQ.RawResumeQueue, batchSize, batchTimeout, handler)
	if err != nil {
		return fmt.Errorf("启动批量消费者失败: %w", err)
	}

	return nil
}

// batchInsertResumeSubmissionsGORM 批量插入简历提交记录 (使用GORM)
func (h *ResumeHandler) batchInsertResumeSubmissionsGORM(ctx context.Context, messages []storage.ResumeUploadMessage) error {
	if len(messages) == 0 {
		return nil
	}

	submissions := make([]models.ResumeSubmission, len(messages))
	for i, msg := range messages {
		submissions[i] = models.ResumeSubmission{
			SubmissionUUID:      msg.SubmissionUUID,
			OriginalFilePathOSS: msg.OriginalFileObjectKey,
			OriginalFilename:    msg.OriginalFilename,
			TargetJobID:         StringPtr(msg.TargetJobID), // 转换为*string
			SourceChannel:       msg.SourceChannel,
			SubmissionTimestamp: msg.ReceivedTimestamp,
			ProcessingStatus:    "ORIGINAL_STORED_PENDING_TEXT_EXTRACTION",
		}
	}

	return h.mysqlAdapter.DB().WithContext(ctx).Create(&submissions).Error
}

// updateResumeStatusGORM 更新简历状态 (使用GORM)
func (h *ResumeHandler) updateResumeStatusGORM(submissionUUID string, status string) error {
	return h.mysqlAdapter.DB().Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", submissionUUID).Update("processing_status", status).Error
}

// processResumeText 处理简历文本提取
func (h *ResumeHandler) processResumeText(ctx context.Context, message storage.ResumeUploadMessage) error {
	// 1. 从MinIO下载原始文件内容
	fileContentBytes, err := h.minioAdapter.GetResumeFile(ctx, message.OriginalFileObjectKey)
	if err != nil {
		return fmt.Errorf("从MinIO获取简历文件失败: %w", err)
	}

	// 2. 提取文本
	// 使用 EinoPDFTextExtractor 的 ExtractTextFromReader 方法
	var text string
	// 使用 message.OriginalFileObjectKey 作为 URI，因为它在日志和元数据中可能有用
	text, _, err = h.pdfExtractor.ExtractTextFromReader(ctx, bytes.NewReader(fileContentBytes), message.OriginalFileObjectKey, nil)
	if err != nil {
		return fmt.Errorf("提取简历文本失败: %w", err)
	}

	// 3. 将文本存储到MinIO
	textObjectKey, err := h.minioAdapter.UploadParsedText(ctx, message.SubmissionUUID, text)
	if err != nil {
		return fmt.Errorf("上传解析文本到MinIO失败: %w", err)
	}

	// 4. 更新数据库状态 (使用GORM)
	err = h.updateResumeTextInfoGORM(message.SubmissionUUID, textObjectKey)
	if err != nil {
		return fmt.Errorf("更新数据库文本信息失败 (GORM): %w", err)
	}

	// 5. 发送消息到LLM解析队列
	processingMessage := storage.ResumeProcessingMessage{
		SubmissionUUID:      message.SubmissionUUID,
		ParsedTextObjectKey: textObjectKey,
		TargetJobID:         message.TargetJobID,
	}

	err = h.mqAdapter.PublishJSON(
		ctx,
		h.cfg.RabbitMQ.ProcessingEventsExchange,
		h.cfg.RabbitMQ.ParsedRoutingKey,
		processingMessage,
		true,
	)
	if err != nil {
		return fmt.Errorf("发布消息到LLM解析队列失败: %w", err)
	}

	// 6. 更新数据库状态为已入队等待LLM解析 (使用GORM)
	return h.updateResumeStatusGORM(message.SubmissionUUID, "QUEUED_FOR_LLM_PARSING")
}

// updateResumeTextInfoGORM 更新简历文本信息 (使用GORM)
func (h *ResumeHandler) updateResumeTextInfoGORM(submissionUUID string, textObjectKey string) error {
	updates := map[string]interface{}{
		"parsed_text_path_oss": textObjectKey,
		"processing_status":    "TEXT_EXTRACTED_PENDING_LLM_PARSING",
	}
	return h.mysqlAdapter.DB().Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", submissionUUID).Updates(updates).Error
}

// StartLLMParsingConsumer 启动LLM解析消费者
func (h *ResumeHandler) StartLLMParsingConsumer(ctx context.Context, prefetchCount int) error {
	// 1. 确保交换机和队列存在
	if err := h.mqAdapter.EnsureExchange(h.cfg.RabbitMQ.ProcessingEventsExchange, "direct", true); err != nil {
		return fmt.Errorf("确保交换机存在失败: %w", err)
	}

	queue, err := h.mqAdapter.EnsureQueue(h.cfg.RabbitMQ.LLMParsingQueue, true)
	if err != nil {
		return fmt.Errorf("确保队列存在失败: %w", err)
	}

	if err := h.mqAdapter.BindQueue(
		h.cfg.RabbitMQ.LLMParsingQueue,
		h.cfg.RabbitMQ.ProcessingEventsExchange,
		h.cfg.RabbitMQ.ParsedRoutingKey,
	); err != nil {
		return fmt.Errorf("绑定队列失败: %w", err)
	}

	log.Printf("LLM解析消费者就绪，监听队列 %s，预取数量：%d", queue.Name, prefetchCount)

	// 2. 启动消费者
	handler := func(body []byte) bool {
		var message storage.ResumeProcessingMessage
		if err := json.Unmarshal(body, &message); err != nil {
			log.Printf("解析消息失败: %v", err)
			return false
		}

		// 处理LLM解析
		if err := h.processLLMParsingGORM(ctx, message); err != nil {
			log.Printf("LLM解析失败: %v, submissionUUID: %s", err, message.SubmissionUUID)
			// 更新状态为处理失败
			h.updateResumeStatusGORM(message.SubmissionUUID, "LLM_PARSING_FAILED")
			return false
		}

		return true
	}

	// 启动消费者
	_, err = h.mqAdapter.StartConsumer(h.cfg.RabbitMQ.LLMParsingQueue, prefetchCount, handler)
	if err != nil {
		return fmt.Errorf("启动消费者失败: %w", err)
	}

	return nil
}

// processLLMParsingGORM 处理LLM解析 (使用GORM)
func (h *ResumeHandler) processLLMParsingGORM(ctx context.Context, message storage.ResumeProcessingMessage) error {
	// 1. 获取解析后的文本
	var text string
	var err error

	if message.ParsedText != "" {
		// 如果消息中直接包含文本内容
		text = message.ParsedText
	} else if message.ParsedTextObjectKey != "" {
		// 否则从MinIO获取
		text, err = h.minioAdapter.GetParsedText(ctx, message.ParsedTextObjectKey)
		if err != nil {
			return fmt.Errorf("从MinIO获取解析文本失败: %w", err)
		}
	} else {
		return fmt.Errorf("消息中既没有文本内容也没有文本对象键")
	}

	// 2. 调用LLM进行结构化解析
	sections, metadata, err := h.llmChunker.ChunkResume(ctx, text)
	if err != nil {
		return fmt.Errorf("LLM解析简历失败: %w", err)
	}

	// 3. 将解析结果存入数据库 (使用GORM)
	err = h.mysqlAdapter.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := h.saveResumeChunksGORM(tx, message.SubmissionUUID, sections); err != nil {
			return fmt.Errorf("保存简历块到数据库失败 (GORM): %w", err)
		}

		// 4. 将基本信息更新到数据库 (使用GORM)
		if err := h.saveResumeBasicInfoGORM(tx, message.SubmissionUUID, metadata); err != nil {
			return fmt.Errorf("保存简历基本信息到数据库失败 (GORM): %w", err)
		}
		return nil
	})
	if err != nil {
		return err // 返回事务错误
	}

	// 5. 如果有目标岗位，触发岗位匹配评估
	if message.TargetJobID != "" {
		// 发送到岗位匹配评估队列 (实际实现中应该创建单独的处理队列)
		if err := h.triggerJobMatching(ctx, message.SubmissionUUID, message.TargetJobID); err != nil {
			log.Printf("触发岗位匹配评估失败: %v", err)
			// 这里我们不返回错误，因为主要处理流程已经完成
		}
	}

	// 6. 更新状态为解析完成 (使用GORM)
	return h.updateResumeStatusGORM(message.SubmissionUUID, "LLM_PARSING_COMPLETED")
}

// saveResumeChunksGORM 保存简历块到数据库 (使用GORM)
func (h *ResumeHandler) saveResumeChunksGORM(tx *gorm.DB, submissionUUID string, sections []*parser.ResumeSection) error {
	if len(sections) == 0 {
		return nil
	}

	chunks := make([]models.ResumeSubmissionChunk, len(sections))
	for i, section := range sections {
		chunks[i] = models.ResumeSubmissionChunk{
			SubmissionUUID:      submissionUUID,
			ChunkIDInSubmission: i + 1,                // 从1开始的chunk_id, 对应 parser.ResumeSection 中的原始 ChunkID 或其索引
			ChunkType:           string(section.Type), // 类型转换
			ChunkTitle:          section.Title,
			ChunkContentText:    section.Content,
		}
	}
	return tx.Create(&chunks).Error
}

// saveResumeBasicInfoGORM 保存简历基本信息到数据库 (使用GORM)
func (h *ResumeHandler) saveResumeBasicInfoGORM(tx *gorm.DB, submissionUUID string, metadata map[string]string) error {
	// 将metadata转换为JSON
	basicInfoJSON, err := models.StringMapToJSON(metadata) // 使用 models 中的辅助函数
	if err != nil {
		return fmt.Errorf("转换metadata为JSON失败: %w", err)
	}

	// 从metadata中提取标识符（姓名_电话号码）
	var identifier string
	if name, hasName := metadata["name"]; hasName {
		if phone, hasPhone := metadata["phone"]; hasPhone {
			identifier = fmt.Sprintf("%s_%s", name, phone)
		} else {
			identifier = fmt.Sprintf("%s_", name)
		}
	}

	updates := map[string]interface{}{
		"llm_parsed_basic_info": basicInfoJSON,
		"llm_resume_identifier": identifier,
	}

	return tx.Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", submissionUUID).Updates(updates).Error
}

// triggerJobMatching 触发岗位匹配评估
func (h *ResumeHandler) triggerJobMatching(ctx context.Context, submissionUUID string, jobID string) error {
	// 这里应该发布消息到专门的岗位匹配评估队列
	// 简化起见，目前只更新一个状态
	log.Printf("触发岗位匹配评估: submissionUUID=%s, jobID=%s", submissionUUID, jobID)

	// 实际实现中应创建job_submission_matches记录
	// 示例：创建一个 JobSubmissionMatch 记录
	match := models.JobSubmissionMatch{
		SubmissionUUID:   submissionUUID,
		JobID:            jobID,                // JobID在JobSubmissionMatch模型中是string类型，不是*string
		EvaluationStatus: "PENDING_EVALUATION", // 初始状态
	}
	// 使用主DB连接，因为这通常是一个独立的操作，可以在LLM解析事务之外
	return h.mysqlAdapter.DB().WithContext(ctx).Create(&match).Error
}
