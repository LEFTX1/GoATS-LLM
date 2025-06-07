package handler // 定义包名为 handler，该包通常包含HTTP请求处理器

import (
	"ai-agent-go/internal/config"           // 导入项目内部的配置包
	"ai-agent-go/internal/constants"        // 导入项目内部的常量包
	"ai-agent-go/internal/logger"           // 导入项目内部的日志包
	"ai-agent-go/internal/processor"        // 导入项目内部的处理器模块
	storage2 "ai-agent-go/internal/storage" // 导入项目内部的存储包，并重命名为storage2以避免命名冲突
	"ai-agent-go/internal/storage/models"   // 导入存储模型
	"ai-agent-go/pkg/utils"                 // 导入项目公用的工具包
	"bytes"
	"context"       // 导入Go的上下文包，用于处理请求作用域、超时和取消信号
	"encoding/json" // 导入JSON编码和解码包
	"errors"        // 导入错误处理包
	"fmt"           // 导入格式化I/O包
	"io"            // 导入I/O基础接口包
	"net/http"      // 导入HTTP客户端和服务器实现的包
	"path/filepath" // 导入用于操作文件路径的包
	"strconv"       // 导入字符串和其他基本数据类型之间转换的包
	"strings"       // 导入字符串操作包
	"time"          // 导入时间处理包

	"github.com/cloudwego/hertz/pkg/app" // 导入Hertz框架的应用上下文包
	"github.com/gofrs/uuid/v5"           // 导入UUID生成库，特别是版本5
	"github.com/rabbitmq/amqp091-go"
)

// ResumeHandler 简历处理器，负责协调简历的处理流程
type ResumeHandler struct {
	cfg             *config.Config             // 指向配置结构体的指针
	storage         *storage2.Storage          // 使用聚合的storage实例
	processorModule *processor.ResumeProcessor // 使用简历处理器组件
}

// NewResumeHandler 创建一个新的简历处理器
func NewResumeHandler(
	cfg *config.Config, // 传入配置对象
	storage *storage2.Storage, // 接收聚合的storage实例
	processorModule *processor.ResumeProcessor, // 接收简历处理器模块
) *ResumeHandler {
	return &ResumeHandler{ // 返回一个新的ResumeHandler实例
		cfg:             cfg,             // 初始化配置
		storage:         storage,         // 初始化聚合的storage实例
		processorModule: processorModule, // 初始化处理器模块
	}
}

// ResumeUploadResponse 简历上传响应结构体
type ResumeUploadResponse struct {
	SubmissionUUID string `json:"submission_uuid"` // 提交的UUID
	Status         string `json:"status"`          // 处理状态
}

// HandleResumeUpload 处理简历上传请求
func (h *ResumeHandler) HandleResumeUpload(c context.Context, ctx *app.RequestContext) {
	// 1. 文件大小校验
	maxUploadSize := h.cfg.Upload.MaxSizeMB * 1024 * 1024 // 从配置读取最大上传大小（MB），并转换为字节

	// 优先使用 Content-Length 头进行校验，直接从header获取，避免框架的二次处理
	clHeader := string(ctx.Request.Header.Peek("Content-Length")) // 获取Content-Length请求头
	if clHeader != "" {                                           // 如果请求头不为空
		if cl, err := strconv.ParseInt(clHeader, 10, 64); err == nil { // 将其解析为64位整数
			if cl > maxUploadSize { // 如果文件大小超过限制
				logger.Warn(). // 记录警告日志
						Int64("size", cl).                  // 日志字段：文件大小
						Int64("max_size", maxUploadSize).   // 日志字段：最大允许大小
						Msg("上传文件大小超限 (根据Content-Length头)") // 日志消息
				ctx.JSON(http.StatusRequestEntityTooLarge, map[string]interface{}{"error": fmt.Sprintf("文件大小不能超过 %d MB", h.cfg.Upload.MaxSizeMB)}) // 返回413错误
				return                                                                                                                             // 终止处理
			}
		}
	}

	// 获取上传的文件
	fileHeader, err := ctx.FormFile("file") // 从表单中获取名为 "file" 的文件
	if err != nil {                         // 如果获取失败
		logger.Error().Err(err).Msg("获取上传文件失败")                                        // 记录错误日志
		ctx.JSON(http.StatusBadRequest, map[string]interface{}{"error": "文件未找到或获取失败"}) // 返回400错误
		return                                                                         // 终止处理
	}

	// 如果 Content-Length 不可用或未通过，则回退到检查解析后的文件头大小，作为最后防线
	if fileHeader.Size > maxUploadSize { // 再次检查解析后的文件大小
		logger.Warn(). // 记录警告日志
				Int64("size", fileHeader.Size).    // 日志字段：文件大小
				Int64("max_size", maxUploadSize).  // 日志字段：最大允许大小
				Msg("上传文件大小超限 (根据multipart form)") // 日志消息
		ctx.JSON(http.StatusRequestEntityTooLarge, map[string]interface{}{"error": fmt.Sprintf("文件大小不能超过 %d MB", h.cfg.Upload.MaxSizeMB)}) // 返回413错误
		return                                                                                                                             // 终止处理
	}

	// 获取目标岗位ID
	targetJobID := ctx.PostForm("target_job_id") // 从表单获取 "target_job_id"
	// 获取来源渠道
	sourceChannel := ctx.PostForm("source_channel") // 从表单获取 "source_channel"
	if sourceChannel == "" {                        // 如果来源渠道为空
		sourceChannel = constants.SourceChannelWebUpload // 设置默认值为 "web_upload"
	}

	file, err := fileHeader.Open() // 打开上传的文件
	if err != nil {                // 如果打开失败
		logger.Error().Err(err).Msg("打开上传文件失败")                                             // 记录错误日志
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "打开文件失败"}) // 返回500错误
		return                                                                              // 终止处理
	}
	defer file.Close() // 确保函数结束时关闭文件

	// 优化文件读取：使用TeeReader同时读取文件内容并捕获前512字节用于MIME检测
	var contentBuffer bytes.Buffer
	fileBytes, err := io.ReadAll(io.TeeReader(io.LimitReader(file, 512), &contentBuffer))
	if err != nil && err != io.EOF {
		logger.Error().Err(err).Msg("读取文件内容失败")
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "读取文件内容失败"})
		return
	}

	// 如果只读取了部分内容（超过512字节的文件），继续读取剩余部分
	if fileHeader.Size > 512 {
		remainingBytes, err := io.ReadAll(file)
		if err != nil {
			logger.Error().Err(err).Msg("读取文件剩余内容失败")
			ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "读取文件内容失败"})
			return
		}
		fileBytes = append(fileBytes, remainingBytes...)
	}

	// 使用前512字节进行MIME类型检测
	mimeType := http.DetectContentType(contentBuffer.Bytes())
	baseMimeType, _, _ := strings.Cut(mimeType, ";")                // 切割MIME类型字符串，去掉参数部分
	baseMimeType = strings.TrimSpace(strings.ToLower(baseMimeType)) // 转换为小写并去除首尾空格

	allowed := false                                            // 标志位，表示文件类型是否被允许，默认为false
	for _, allowedType := range h.cfg.Upload.AllowedMIMETypes { // 遍历配置中允许的MIME类型列表
		// 比较时也将配置中的类型转为小写，以防配置中存在大小写问题
		if baseMimeType == strings.ToLower(strings.TrimSpace(allowedType)) { // 如果当前MIME类型在允许列表中
			allowed = true // 设为允许
			break          // 退出循环
		}
	}

	if !allowed { // 如果文件类型不被允许
		logger.Warn(). // 记录警告日志
				Str("detected_mime_type", mimeType).  // 日志字段：检测到的MIME类型
				Str("filename", fileHeader.Filename). // 日志字段：文件名
				Msg("检测到不允许的文件MIME类型")                // 日志消息
		ctx.JSON(http.StatusUnsupportedMediaType, map[string]interface{}{"error": fmt.Sprintf("不支持的文件类型: %s", baseMimeType)}) // 返回415错误
		return                                                                                                                // 终止处理
	}

	// 1. 生成UUIDv7
	uuidV7, err := uuid.NewV7() // 生成一个版本7的UUID，它包含了时间戳信息，有利于数据库索引
	if err != nil {             // 如果生成失败
		logger.Error().Err(err).Msg("生成UUIDv7失败")                                             // 记录错误日志
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "生成UUID失败"}) // 返回500错误
		return                                                                                // 终止处理
	}
	submissionUUID := uuidV7.String() // 将UUID转换为字符串

	// 创建带有submissionUUID的上下文
	c = logger.WithSubmissionUUID(c, submissionUUID)
	logCtx := logger.FromContext(c)

	// 2. 获取文件扩展名
	ext := filepath.Ext(fileHeader.Filename) // 从文件名中提取扩展名
	if ext == "" {                           // 如果文件没有扩展名
		ext = ".pdf" // 默认为 .pdf
	}

	// 优化：先计算MD5进行去重，再上传到MinIO，以节省带宽和存储
	// 将文件内容读入内存以计算MD5。基于配置中的MaxSizeMB，这在可接受的范围内。
	// MIME检测后，文件指针已重置到开头
	fileMD5Hex := utils.CalculateMD5(fileBytes)

	// 4. 使用原子操作检查并添加文件MD5，用于文件去重
	exists, err := h.storage.Redis.CheckAndAddRawFileMD5(c, fileMD5Hex) // 在Redis中检查MD5是否存在，如果不存在则添加
	if err != nil {                                                     // 如果Redis操作失败
		logCtx.Error().
			Err(err).
			Str("md5", fileMD5Hex).
			Msg("检查并添加文件MD5失败")
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "检查文件MD5重复性时Redis操作失败"})
		return
	}

	if exists { // 如果文件MD5已存在，说明是重复文件
		logCtx.Info().
			Str("md5", fileMD5Hex).
			Str("filename", fileHeader.Filename).
			Msg("检测到重复的文件MD5，跳过处理")

		// 由于文件尚未上传到MinIO，因此无需执行异步删除操作。
		ctx.JSON(http.StatusConflict, ResumeUploadResponse{
			SubmissionUUID: "",
			Status:         constants.StatusDuplicateFileSkipped,
		})
		return
	}

	// 3. 上传文件到MinIO
	// 从配置中读取超时时间，如果未设置则使用默认值
	uploadTimeout := h.cfg.Upload.Timeout // 获取上传超时配置
	if uploadTimeout <= 0 {               // 如果未设置或设置不合法
		uploadTimeout = 2 * time.Minute // 使用默认值2分钟
	}
	uploadCtx, cancel := context.WithTimeout(c, uploadTimeout) // 创建一个带超时的上下文，用于控制上传操作的时间
	defer cancel()                                             // 确保函数结束时取消上下文，释放资源

	originalObjectKey, err := h.storage.MinIO.UploadResumeFile(uploadCtx, submissionUUID, ext, bytes.NewReader(fileBytes), int64(len(fileBytes))) // 从内存上传文件到对象存储
	if err != nil {                                                                                                                               // 如果上传失败
		// 检查是否是上下文超时错误
		if errors.Is(err, context.DeadlineExceeded) { // 如果错误是由于上下文超时引起的
			logCtx.Error().Err(err).Msg("上传简历到MinIO超时")                                       // 使用上下文日志（已包含submissionUUID）
			ctx.JSON(http.StatusGatewayTimeout, map[string]interface{}{"error": "上传到存储服务超时"}) // 返回504网关超时错误
			return                                                                            // 终止处理
		}

		// 其他错误
		logCtx.Error().Err(err).Msg("上传简历到MinIO失败")                                               // 使用上下文日志
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "上传简历到MinIO失败"}) // 返回500内部服务器错误
		return                                                                                    // 终止处理
	}

	// 5. 构建消息并发送到RabbitMQ，以进行异步处理
	message := storage2.ResumeUploadMessage{ // 创建一个简历上传消息结构体实例
		SubmissionUUID:      submissionUUID,      // 填充提交UUID
		OriginalFilePathOSS: originalObjectKey,   // 填充原始文件在对象存储中的路径（键）
		OriginalFilename:    fileHeader.Filename, // 填充原始文件名
		TargetJobID:         targetJobID,         // 填充目标岗位ID
		SourceChannel:       sourceChannel,       // 填充来源渠道
		SubmissionTimestamp: time.Now(),          // 记录当前的提交时间戳
		RawFileMD5:          fileMD5Hex,          // 填充文件的MD5值，用于后续流程或失败场景的回滚

		// 兼容性字段 (保留以向后兼容旧版客户端)
		OriginalFileObjectKey: originalObjectKey, // 再次填充对象键以兼容旧版
	}

	// 发布消息到上传交换机
	err = h.storage.RabbitMQ.PublishJSON( // 调用RabbitMQ客户端发布JSON格式的消息
		c,                                   // 使用传入的上下文
		h.cfg.RabbitMQ.ResumeEventsExchange, // 指定要发布的交换机名称
		h.cfg.RabbitMQ.UploadedRoutingKey,   // 指定路由键
		message,                             // 要发布的消息体
		true,                                // 设置消息为持久化，防止MQ服务重启时丢失
	)
	if err != nil { // 如果发布失败
		logCtx.Error().Err(err).Msg("发布消息到RabbitMQ失败")                                               // 使用上下文日志记录错误
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "发布消息到RabbitMQ失败"}) // 返回500错误
		return                                                                                       // 终止处理
	}

	// 6. 返回响应
	ctx.JSON(http.StatusOK, ResumeUploadResponse{ // 返回200 OK成功响应
		SubmissionUUID: submissionUUID,                         // 在响应中包含提交UUID
		Status:         constants.StatusSubmittedForProcessing, // 状态为"已提交等待处理"
	})
}

// StartResumeUploadConsumer 启动简历上传消费者，用于处理来自RabbitMQ的消息
func (h *ResumeHandler) StartResumeUploadConsumer(ctx context.Context, batchSize int, batchTimeout time.Duration) error {
	if h.storage == nil || h.storage.RabbitMQ == nil {
		logger.Warn().Msg("RabbitMQ client is nil, skipping resume upload consumer start.")
		return nil
	}
	// 添加日志打印当前配置中的交换机名称
	logger.Info(). // 记录信息日志
			Str("exchange", h.cfg.RabbitMQ.ResumeEventsExchange).  // 日志字段：交换机名称
			Str("routing_key", h.cfg.RabbitMQ.UploadedRoutingKey). // 日志字段：路由键
			Msg("初始化RabbitMQ配置")                                   // 日志消息

	// 1. 确保交换机和队列存在，如果不存在则创建
	if err := h.storage.RabbitMQ.EnsureExchange(h.cfg.RabbitMQ.ResumeEventsExchange, "direct", true); err != nil { // 确保交换机存在
		return fmt.Errorf("确保交换机存在失败: %w", err) // 如果失败，返回包装后的错误
	}

	if err := h.storage.RabbitMQ.EnsureQueue(h.cfg.RabbitMQ.RawResumeQueue, true); err != nil { // 确保队列存在
		return fmt.Errorf("确保队列存在失败: %w", err) // 如果失败，返回包装后的错误
	}

	if err := h.storage.RabbitMQ.BindQueue( // 将队列绑定到交换机
		h.cfg.RabbitMQ.RawResumeQueue,       // 队列名称
		h.cfg.RabbitMQ.ResumeEventsExchange, // 交换机名称
		h.cfg.RabbitMQ.UploadedRoutingKey,   // 使用路由键进行绑定
	); err != nil {
		return fmt.Errorf("绑定队列失败: %w", err) // 如果绑定失败，返回包装后的错误
	}

	logger.Info().
		Str("queue", h.cfg.RabbitMQ.RawResumeQueue).
		Int("batch_size", batchSize).
		Dur("batch_timeout", batchTimeout).
		Msg("简历上传批量消费者就绪")

	// 启动批量消费者
	prefetchMultiplier := h.cfg.RabbitMQ.BatchPrefetchMultiplier
	if prefetchMultiplier <= 0 {
		prefetchMultiplier = 2 // 默认值
	}
	prefetchCount := batchSize * prefetchMultiplier
	_, err := h.storage.RabbitMQ.StartBatchConsumer(h.cfg.RabbitMQ.RawResumeQueue, batchSize, batchTimeout, prefetchCount, func(deliveries []amqp091.Delivery) bool {
		submissions := make([]models.ResumeSubmission, 0, len(deliveries))
		messages := make([]storage2.ResumeUploadMessage, 0, len(deliveries))

		// 1. 解码所有消息
		for _, d := range deliveries {
			var message storage2.ResumeUploadMessage
			if err := json.Unmarshal(d.Body, &message); err != nil {
				logger.Error().Err(err).Bytes("message_body", d.Body).Msg("解析消息失败，该消息将被跳过")
				// 单个消息解析失败，不影响批次中其他消息
				continue
			}
			messages = append(messages, message)
			submissions = append(submissions, models.ResumeSubmission{
				SubmissionUUID:      message.SubmissionUUID,
				OriginalFilePathOSS: message.OriginalFilePathOSS,
				OriginalFilename:    message.OriginalFilename,
				TargetJobID:         utils.StringPtr(message.TargetJobID),
				SourceChannel:       message.SourceChannel,
				SubmissionTimestamp: message.SubmissionTimestamp,
				ProcessingStatus:    constants.StatusPendingParsing,
				RawTextMD5:          message.RawFileMD5,
			})
		}

		if len(submissions) == 0 {
			logger.Warn().Msg("批次中没有可处理的消息，全部跳过")
			return true // 返回true以确认空批次
		}

		// 2. 批量插入数据库
		// 为整个批次创建一个独立的上下文
		batchCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		if err := h.storage.MySQL.BatchInsertResumeSubmissions(batchCtx, submissions); err != nil {
			logger.Error().Err(err).Int("batch_size", len(submissions)).Msg("批量插入初始简历提交记录失败")

			// 补偿操作：如果DB批量插入失败，则从Redis中移除所有相关MD5
			go func(msgsToRollback []storage2.ResumeUploadMessage) {
				bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				for _, msg := range msgsToRollback {
					if msg.RawFileMD5 != "" {
						if err := h.storage.Redis.RemoveRawFileMD5(bgCtx, msg.RawFileMD5); err != nil {
							logger.Error().Err(err).Str("md5", msg.RawFileMD5).Msg("补偿操作：异步删除Redis中的文件MD5失败")
						}
					}
				}
			}(messages)

			return false // 返回false，表示批次处理失败，MQ将Nack整个批次
		}

		// 3. 逐个为处理成功的消息调用Processor
		for _, message := range messages {
			// 为每个消息的后续处理创建上下文
			msgCtx, msgCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			if err := h.processorModule.ProcessUploadedResume(msgCtx, message, h.cfg); err != nil {
				logger.Error().
					Err(err).
					Str(constants.LogKeySubmissionUUID, message.SubmissionUUID).
					Msg("ResumeProcessor 处理上传简历失败（在批量插入后）")
				// 即使单个processor失败，我们也不Nack整个批次，因为DB记录已插入。
				// Processor内部应该负责更新该简历的状态为失败。
			}
			msgCancel()
		}

		return true // 批次处理成功，MQ将Ack整个批次
	})

	if err != nil {
		return fmt.Errorf("启动批量消费者失败: %w", err)
	}

	return nil
}

// StartLLMParsingConsumer 启动LLM解析消费者
func (h *ResumeHandler) StartLLMParsingConsumer(ctx context.Context, prefetchCount int) error { // ctx: 上下文, prefetchCount: 预取数量
	if h.storage == nil || h.storage.RabbitMQ == nil { // 检查RabbitMQ客户端是否已初始化
		logger.Warn().Msg("RabbitMQ client is nil, skipping LLM parsing consumer start.") // 如果未初始化，则记录警告并跳过
		return nil                                                                        // 返回nil，表示正常跳过
	}
	// 1. 确保交换机和队列存在
	if err := h.storage.RabbitMQ.EnsureExchange(h.cfg.RabbitMQ.ProcessingEventsExchange, "direct", true); err != nil { // 确保处理事件的交换机存在
		return fmt.Errorf("确保交换机存在失败: %w", err) // 返回错误
	}

	if err := h.storage.RabbitMQ.EnsureQueue(h.cfg.RabbitMQ.LLMParsingQueue, true); err != nil { // 确保LLM解析队列存在
		return fmt.Errorf("确保队列存在失败: %w", err) // 返回错误
	}

	if err := h.storage.RabbitMQ.BindQueue( // 绑定队列到交换机
		h.cfg.RabbitMQ.LLMParsingQueue,          // 队列名称
		h.cfg.RabbitMQ.ProcessingEventsExchange, // 交换机名称
		h.cfg.RabbitMQ.ParsedRoutingKey,         // 使用"已解析"路由键进行绑定
	); err != nil {
		return fmt.Errorf("绑定队列失败: %w", err) // 返回错误
	}

	logger.Info(). // 记录消费者准备就绪的日志
			Str("queue", h.cfg.RabbitMQ.LLMParsingQueue). // 日志字段：监听的队列名
			Int("prefetch_count", prefetchCount).         // 日志字段：预取数量（QoS设置）
			Msg("LLM解析消费者就绪")                             // 日志消息

	// 2. 启动消费者
	_, err := h.storage.RabbitMQ.StartConsumer(h.cfg.RabbitMQ.LLMParsingQueue, prefetchCount, func(data []byte) bool { // 启动消费者
		var message storage2.ResumeProcessingMessage           // 定义用于接收消息的结构体变量
		if err := json.Unmarshal(data, &message); err != nil { // 解析JSON消息
			logger.Error(). // 记录错误日志
					Err(err).     // 错误详情
					Msg("解析消息失败") // 错误消息
			return false // 解析失败，消息处理失败
		}

		// 为每个消息创建一个独立的上下文，并设置超时，以隔离处理并防止任务无限期运行
		msgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // LLM处理可能耗时较长，设置5分钟超时
		defer cancel()                                                             // 确保处理函数结束时取消上下文

		// 新逻辑: 调用 Processor 进行处理
		if err := h.processorModule.ProcessLLMTasks(msgCtx, message, h.cfg); err != nil { // 调用处理器处理LLM任务
			logger.Error(). // 记录错误日志
					Err(err).                                                    // 错误详情
					Str(constants.LogKeySubmissionUUID, message.SubmissionUUID). // 提交UUID
					Msg("ResumeProcessor 处理LLM任务失败")                             // 错误消息
			// 错误状态的更新现在由 ProcessLLMTasks 内部或其调用者处理
			return false // 处理失败，消息处理失败
		}

		return true // 消息处理成功
	})

	if err != nil { // 如果启动消费者失败
		return fmt.Errorf("启动消费者失败: %w", err) // 返回错误
	}

	return nil // 成功启动，返回nil
}

// StartMD5CleanupTask 启动MD5记录清理任务
// 此方法可选调用，用于定期检查和重置MD5记录的过期时间
func (h *ResumeHandler) StartMD5CleanupTask(ctx context.Context) { // ctx: 用于控制任务生命周期的上下文
	// 定义清理间隔，默认每周执行一次
	cleanupInterval := 7 * 24 * time.Hour // 设置清理间隔为7天

	logger.Info(). // 记录任务启动日志
			Dur("interval", cleanupInterval). // 日志字段：清理间隔
			Msg("启动MD5记录清理任务")                // 日志消息

	ticker := time.NewTicker(cleanupInterval) // 创建一个定时器，按指定间隔触发
	defer ticker.Stop()                       // 确保函数退出时停止定时器，释放资源

	// 立即执行一次清理
	h.cleanupMD5Records(ctx) // 调用实际的清理函数

	for { // 无限循环，使任务持续运行
		select { // 使用select监听多个通道
		case <-ticker.C: // 当定时器触发时
			h.cleanupMD5Records(ctx) // 执行清理操作
		case <-ctx.Done(): // 当传入的上下文被取消时（例如，服务关闭）
			logger.Info().Msg("MD5记录清理任务退出") // 记录任务正常退出的信息
			return                           // 退出循环和函数
		}
	}
}

// cleanupMD5Records 是实际执行MD5记录清理的函数
// 此方法会检查MD5集合键是否设置了过期时间，如果没有则为其设置一个默认的过期时间
func (h *ResumeHandler) cleanupMD5Records(ctx context.Context) { // ctx: 上下文
	logger.Info().Msg("执行MD5记录清理任务...") // 记录开始执行清理任务

	// 使用新的内部常量来检查和设置文件MD5集合的过期时间
	ttlFile, errFile := h.storage.Redis.Client.TTL(ctx, constants.RawFileMD5SetKey).Result() // 获取原始文件MD5集合的TTL（剩余生存时间）
	if errFile != nil {                                                                      // 如果获取TTL失败
		logger.Error().Err(errFile).Str("setKey", constants.RawFileMD5SetKey).Msg("获取文件MD5集合过期时间失败") // 记录错误
	} else if ttlFile < 0 { // 如果TTL为负数（-1表示无过期，-2表示键不存在），说明需要设置过期时间
		expiry := h.storage.Redis.GetMD5ExpireDuration()                                                     // 从配置或默认值获取MD5记录的过期时长
		if err := h.storage.Redis.Client.Expire(ctx, constants.RawFileMD5SetKey, expiry).Err(); err != nil { // 为该键设置过期时间
			logger.Error().Err(err).Str("setKey", constants.RawFileMD5SetKey).Msg("设置文件MD5集合过期时间失败") // 记录设置失败的错误
		} else { // 如果设置成功
			logger.Info().Str("setKey", constants.RawFileMD5SetKey).Dur("expiry", expiry).Msg("成功设置文件MD5集合过期时间") // 记录成功设置的信息
		}
	}

	// 使用新的内部常量来检查和设置文本MD5集合的过期时间
	ttlText, errText := h.storage.Redis.Client.TTL(ctx, constants.ParsedTextMD5SetKey).Result() // 获取解析后文本MD5集合的TTL
	if errText != nil {                                                                         // 如果获取TTL失败
		logger.Error().Err(errText).Str("setKey", constants.ParsedTextMD5SetKey).Msg("获取文本MD5集合过期时间失败") // 记录错误
	} else if ttlText < 0 { // 如果TTL为负数，需要设置过期时间
		expiry := h.storage.Redis.GetMD5ExpireDuration()                                                        // 获取过期时长
		if err := h.storage.Redis.Client.Expire(ctx, constants.ParsedTextMD5SetKey, expiry).Err(); err != nil { // 为该键设置过期时间
			logger.Error().Err(err).Str("setKey", constants.ParsedTextMD5SetKey).Msg("设置文本MD5集合过期时间失败") // 记录设置失败的错误
		} else { // 如果设置成功
			logger.Info().Str("setKey", constants.ParsedTextMD5SetKey).Dur("expiry", expiry).Msg("成功设置文本MD5集合过期时间") // 记录成功设置的信息
		}
	}

	logger.Info().Msg("MD5记录清理任务完成") // 记录清理任务执行完毕
}
