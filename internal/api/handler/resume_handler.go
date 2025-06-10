package handler // 定义包名为 handler，通常用于存放HTTP请求的处理器。

import (
	"ai-agent-go/internal/config"           // 导入项目内部的配置包，用于管理应用配置。
	"ai-agent-go/internal/constants"        // 导入项目内部的常量包，存放固定的常量值。
	"ai-agent-go/internal/logger"           // 导入项目内部的日志包，用于记录日志。
	"ai-agent-go/internal/processor"        // 导入项目内部的处理器模块，负责核心业务逻辑。
	storage2 "ai-agent-go/internal/storage" // 导入项目内部的存储包，并重命名为storage2以避免与其它包名冲突。
	"ai-agent-go/internal/storage/models"   // 导入存储模型，定义数据库表的结构。
	"ai-agent-go/pkg/utils"                 // 导入项目公用的工具包，提供通用函数。
	"bytes"                                 // 导入bytes包，提供对字节切片的操作。
	"context"                               // 导入Go的上下文包，用于在API边界之间传递请求作用域、超时和取消信号。
	"encoding/json"                         // 导入JSON编码和解码包。
	"errors"                                // 导入错误处理包，用于创建和检查错误。
	"fmt"                                   // 导入格式化I/O包，用于字符串格式化。
	"io"                                    // 导入I/O基础接口包，如Reader和Writer。
	"net/http"                              // 导入HTTP客户端和服务器实现的包。
	"path/filepath"                         // 导入用于操作文件路径的包，处理路径分隔符等。
	"strconv"                               // 导入字符串和其他基本数据类型之间转换的包。
	"strings"                               // 导入字符串操作包。
	"time"                                  // 导入时间处理包。

	"github.com/cloudwego/hertz/pkg/app"     // 导入Hertz框架的应用上下文包，Hertz是高性能的HTTP框架。
	"github.com/gofrs/uuid/v5"               // 导入UUID生成库，特别是版本5，用于生成唯一标识符。
	amqp091 "github.com/rabbitmq/amqp091-go" // 导入RabbitMQ的Go客户端库。
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute" // 导入OpenTelemetry的attribute包，用于定义span的属性。
	"go.opentelemetry.io/otel/codes"     // 导入OpenTelemetry的codes包，用于定义span的状态码。
	"go.opentelemetry.io/otel/trace"     // 导入OpenTelemetry的trace包，用于手动创建和管理span。
)

var handlerTracer = otel.Tracer("ai-agent-go/handler") // 定义一个全局的tracer实例，用于在handler层创建span。

// ResumeHandler 简历处理器，封装了处理简历上传和后续流程所需的所有依赖和方法。
type ResumeHandler struct {
	cfg             *config.Config             // cfg 字段持有应用的全局配置。
	storage         *storage2.Storage          // storage 字段是一个聚合了所有存储客户端（如MySQL, Redis, MinIO）的实例。
	processorModule *processor.ResumeProcessor // processorModule 字段持有简历处理的核心逻辑模块。
}

// NewResumeHandler 是 ResumeHandler 的构造函数，用于创建一个新的简历处理器实例。
func NewResumeHandler(
	cfg *config.Config, // cfg 参数是应用的配置信息。
	storage *storage2.Storage, // storage 参数是聚合的存储实例。
	processorModule *processor.ResumeProcessor, // processorModule 参数是简历处理器模块。
) *ResumeHandler {
	return &ResumeHandler{ // 返回一个初始化后的 ResumeHandler 实例指针。
		cfg:             cfg,             // 将传入的配置赋值给实例的 cfg 字段。
		storage:         storage,         // 将传入的存储实例赋值给实例的 storage 字段。
		processorModule: processorModule, // 将传入的处理器模块赋值给实例的 processorModule 字段。
	}
}

// ResumeUploadResponse 定义了简历上传成功后返回给客户端的JSON响应体结构。
type ResumeUploadResponse struct {
	SubmissionUUID string `json:"submission_uuid"` // SubmissionUUID 是本次提交的唯一标识符。
	Status         string `json:"status"`          // Status 表示简历提交后的当前状态。
}

// HandleResumeUpload 是处理简历上传HTTP请求的核心方法。
func (h *ResumeHandler) HandleResumeUpload(c context.Context, ctx *app.RequestContext) {
	// 创建一个名为 "HandleResumeUpload" 的主span，用于追踪整个上传请求的生命周期。
	c, span := handlerTracer.Start(c, "HandleResumeUpload",
		trace.WithAttributes( // 为span设置初始属性。
			attribute.String("http.method", "POST"),          // 记录HTTP请求方法。
			attribute.String("http.route", "/resume/upload"), // 记录请求的路由。
		))
	defer span.End() // 使用defer确保在函数退出时结束span，从而记录其耗时。

	// 1. 文件大小校验
	// 创建一个子span来追踪文件大小校验的耗时。
	c, validateSpan := handlerTracer.Start(c, "file.validate_size")
	maxUploadSize := h.cfg.Upload.MaxSizeMB * 1024 * 1024 // 从配置中读取最大上传大小（单位：MB），并转换为字节。

	// 优先使用 Content-Length 头进行校验，这比解析整个表单更高效。
	clHeader := string(ctx.Request.Header.Peek("Content-Length")) // 从请求头中获取 "Content-Length" 的值。
	if clHeader != "" {                                           // 检查 "Content-Length" 头是否存在。
		if cl, err := strconv.ParseInt(clHeader, 10, 64); err == nil { // 将头信息的值（字符串）解析为64位整数。
			validateSpan.SetAttributes(attribute.Int64("file.size", cl))                // 在span中记录文件大小。
			validateSpan.SetAttributes(attribute.Int64("file.max_size", maxUploadSize)) // 在span中记录允许的最大文件大小。
			if cl > maxUploadSize {                                                     // 如果文件大小超过了配置的限制。
				logger.Warn(). // 记录一条警告级别的日志。
						Int64("size", cl).                  // 在日志中添加文件大小字段。
						Int64("max_size", maxUploadSize).   // 在日志中添加最大允许大小字段。
						Msg("上传文件大小超限 (根据Content-Length头)") // 日志消息。
				validateSpan.SetStatus(codes.Error, "file size exceeds limit")                                                                     // 将校验span的状态设置为错误。
				validateSpan.End()                                                                                                                 // 立即结束校验span。
				span.SetStatus(codes.Error, "file size validation failed")                                                                         // 将主span的状态也设置为错误。
				ctx.JSON(http.StatusRequestEntityTooLarge, map[string]interface{}{"error": fmt.Sprintf("文件大小不能超过 %d MB", h.cfg.Upload.MaxSizeMB)}) // 向客户端返回 413 Request Entity Too Large 错误。
				return                                                                                                                             // 终止后续处理。
			}
		}
	}
	validateSpan.End() // 结束文件大小校验的span。

	// 从multipart/form-data格式的请求体中获取上传的文件。
	fileHeader, err := ctx.FormFile("file") // "file" 是表单中文件字段的名称。
	if err != nil {                         // 如果获取文件失败。
		logger.Error().Err(err).Msg("获取上传文件失败")                                        // 记录一条错误级别的日志。
		span.RecordError(err)                                                          // 在主span中记录这个错误。
		span.SetStatus(codes.Error, "failed to get uploaded file")                     // 将主span的状态设置为错误。
		ctx.JSON(http.StatusBadRequest, map[string]interface{}{"error": "文件未找到或获取失败"}) // 向客户端返回 400 Bad Request 错误。
		return                                                                         // 终止后续处理。
	}

	// 作为双重保险，如果 Content-Length 不可用或校验通过，再次检查通过框架解析后的文件大小。
	if fileHeader.Size > maxUploadSize { // 比较文件头中的大小和最大限制。
		logger.Warn(). // 记录一条警告级别的日志。
				Int64("size", fileHeader.Size).    // 日志中记录文件大小。
				Int64("max_size", maxUploadSize).  // 日志中记录最大允许大小。
				Msg("上传文件大小超限 (根据multipart form)") // 日志消息。
		span.SetAttributes(attribute.Int64("file.size", fileHeader.Size))                                                                  // 在主span中更新文件大小属性。
		span.SetStatus(codes.Error, "file size exceeds limit")                                                                             // 将主span的状态设置为错误。
		ctx.JSON(http.StatusRequestEntityTooLarge, map[string]interface{}{"error": fmt.Sprintf("文件大小不能超过 %d MB", h.cfg.Upload.MaxSizeMB)}) // 向客户端返回 413 Request Entity Too Large 错误。
		return                                                                                                                             // 终止后续处理。
	}

	// 从表单数据中获取目标岗位ID。
	targetJobID := ctx.PostForm("target_job_id")
	// 从表单数据中获取来源渠道。
	sourceChannel := ctx.PostForm("source_channel")
	if sourceChannel == "" { // 如果来源渠道字段为空。
		sourceChannel = constants.SourceChannelWebUpload // 使用默认值 "web_upload"。
	}

	// 在主span中记录关键的业务属性，便于追踪和分析。
	span.SetAttributes(
		attribute.String("target_job_id", targetJobID),    // 记录目标岗位ID。
		attribute.String("source_channel", sourceChannel), // 记录来源渠道。
	)

	// 创建一个子span来追踪文件读取和MD5计算的耗时。
	c, fileSpan := handlerTracer.Start(c, "file.read_and_calculate_md5")
	file, err := fileHeader.Open() // 打开上传的文件，获取 io.ReadCloser 接口。
	if err != nil {                // 如果打开文件失败。
		logger.Error().Err(err).Msg("打开上传文件失败")                                             // 记录错误日志。
		fileSpan.RecordError(err)                                                           // 在文件span中记录错误。
		fileSpan.End()                                                                      // 结束文件span。
		span.RecordError(err)                                                               // 在主span中也记录错误。
		span.SetStatus(codes.Error, "failed to open uploaded file")                         // 设置主span状态为错误。
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "打开文件失败"}) // 返回 500 Internal Server Error 错误。
		return                                                                              // 终止后续处理。
	}
	defer file.Close() // 使用defer确保在函数退出时关闭文件句柄，释放资源。

	// 优化文件读取：使用 io.TeeReader 同时将文件内容读取到内存并复制一份到 contentBuffer。
	// 这里只读取前512字节，这对于MIME类型检测通常是足够的。
	var contentBuffer bytes.Buffer                                                        // 创建一个缓冲区来存储文件的前512字节。
	fileBytes, err := io.ReadAll(io.TeeReader(io.LimitReader(file, 512), &contentBuffer)) // TeeReader将从file读取的数据同时写入fileBytes和contentBuffer。
	if err != nil && err != io.EOF {                                                      // 如果发生错误且不是文件结束符（EOF是正常情况）。
		logger.Error().Err(err).Msg("读取文件内容失败")                                               // 记录错误。
		fileSpan.RecordError(err)                                                             // 在文件span中记录错误。
		fileSpan.End()                                                                        // 结束文件span。
		span.RecordError(err)                                                                 // 在主span中记录错误。
		span.SetStatus(codes.Error, "failed to read file content")                            // 设置主span状态。
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "读取文件内容失败"}) // 返回 500 错误。
		return                                                                                // 终止处理。
	}

	// 如果文件大于512字节，需要继续读取剩余的部分。
	if fileHeader.Size > 512 {
		remainingBytes, err := io.ReadAll(file) // 从当前文件偏移量继续读取到末尾。
		if err != nil {                         // 如果读取失败。
			logger.Error().Err(err).Msg("读取文件剩余内容失败")                                             // 记录错误。
			fileSpan.RecordError(err)                                                             // 在文件span中记录错误。
			fileSpan.End()                                                                        // 结束文件span。
			span.RecordError(err)                                                                 // 在主span中记录错误。
			span.SetStatus(codes.Error, "failed to read remaining file content")                  // 设置主span状态。
			ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "读取文件内容失败"}) // 返回 500 错误。
			return                                                                                // 终止处理。
		}
		fileBytes = append(fileBytes, remainingBytes...) // 将剩余的字节追加到已读取的字节切片中。
	}

	// 使用文件的前512字节（已存入contentBuffer）进行MIME类型检测。
	mimeType := http.DetectContentType(contentBuffer.Bytes())
	baseMimeType, _, _ := strings.Cut(mimeType, ";")                // 切割MIME类型字符串，例如 "application/pdf; charset=binary" -> "application/pdf"。
	baseMimeType = strings.TrimSpace(strings.ToLower(baseMimeType)) // 转换为小写并去除首尾空格，以便进行不区分大小写的比较。

	// 在文件span中记录文件的元数据。
	fileSpan.SetAttributes(
		attribute.String("file.name", fileHeader.Filename),        // 记录原始文件名。
		attribute.String("file.mime_type", baseMimeType),          // 记录检测到的基础MIME类型。
		attribute.Int64("file.size_bytes", int64(len(fileBytes))), // 记录文件的总字节数。
	)

	allowed := false                                            // 初始化标志位，表示文件类型是否被允许。
	for _, allowedType := range h.cfg.Upload.AllowedMIMETypes { // 遍历配置中允许的MIME类型列表。
		// 比较时也将配置中的类型进行标准化处理（小写、去空格），以防配置错误。
		if baseMimeType == strings.ToLower(strings.TrimSpace(allowedType)) { // 如果检测到的类型在允许列表中。
			allowed = true // 设置标志位为true。
			break          // 找到匹配项，无需继续遍历，退出循环。
		}
	}

	if !allowed { // 如果遍历完所有允许的类型后，标志位仍然是false。
		logger.Warn(). // 记录一条警告日志。
				Str("detected_mime_type", mimeType).  // 记录检测到的完整MIME类型。
				Str("filename", fileHeader.Filename). // 记录文件名。
				Msg("检测到不允许的文件MIME类型")                // 日志消息。
		fileSpan.SetStatus(codes.Error, "unsupported mime type")                                                              // 设置文件span状态为错误。
		fileSpan.End()                                                                                                        // 结束文件span。
		span.SetStatus(codes.Error, "file type validation failed")                                                            // 设置主span状态为错误。
		ctx.JSON(http.StatusUnsupportedMediaType, map[string]interface{}{"error": fmt.Sprintf("不支持的文件类型: %s", baseMimeType)}) // 返回 415 Unsupported Media Type 错误。
		return                                                                                                                // 终止处理。
	}

	// 计算文件的MD5哈希值，用于后续的文件去重。
	fileMD5Hex := utils.CalculateMD5(fileBytes)
	fileSpan.SetAttributes(attribute.String("file.md5", fileMD5Hex)) // 在文件span中记录文件的MD5值。
	fileSpan.End()                                                   // 结束文件读取和MD5计算的span。

	// 1. 生成一个UUID v7作为本次提交的唯一标识符。
	// UUID v7是基于时间的，有利于数据库索引性能。
	uuidV7, err := uuid.NewV7()
	if err != nil { // 如果UUID生成失败（极少见）。
		logger.Error().Err(err).Msg("生成UUIDv7失败")                                             // 记录错误日志。
		span.RecordError(err)                                                                 // 在主span中记录错误。
		span.SetStatus(codes.Error, "failed to generate UUID")                                // 设置主span状态。
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "生成UUID失败"}) // 返回 500 错误。
		return                                                                                // 终止处理。
	}
	submissionUUID := uuidV7.String()                                       // 将UUID对象转换为字符串格式。
	span.SetAttributes(attribute.String("submission_uuid", submissionUUID)) // 在主span中记录这个UUID。

	// 创建一个带有submissionUUID的子日志记录器上下文，方便后续日志追踪。
	c = logger.WithSubmissionUUID(c, submissionUUID)
	logCtx := logger.FromContext(c) // 从上下文中获取带有预设字段的日志记录器。

	// 2. 获取文件的扩展名。
	ext := filepath.Ext(fileHeader.Filename) // 从原始文件名中提取扩展名，例如 ".pdf"。
	if ext == "" {                           // 如果文件名没有扩展名。
		ext = ".pdf" // 默认使用 ".pdf" 作为扩展名。
	}

	// 4. 使用原子操作检查并添加文件MD5，用于文件去重。
	// 创建一个子span来追踪Redis去重操作的耗时。
	c, redisSpan := handlerTracer.Start(c, "redis.deduplication")
	redisSpan.SetAttributes(
		attribute.String("md5", fileMD5Hex),                 // 记录要检查的MD5值。
		attribute.String("submission_uuid", submissionUUID), // 记录当前的提交UUID。
	)

	// 调用Redis存储层的方法，原子性地检查MD5是否存在于Set中，如果不存在则添加。
	exists, err := h.storage.Redis.CheckAndAddRawFileMD5(c, fileMD5Hex)
	if err != nil { // 如果Redis操作失败。
		logCtx.Error(). // 使用带UUID的日志记录器。
				Err(err).
				Str("md5", fileMD5Hex).
				Msg("检查并添加文件MD5失败")
		redisSpan.RecordError(err)
		redisSpan.SetStatus(codes.Error, "redis operation failed")
		redisSpan.End()
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to check file MD5")
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "检查文件MD5重复性时Redis操作失败"}) // 返回 500 错误。
		return                                                                                            // 终止处理。
	}

	if exists { // 如果MD5已存在，说明是重复文件。
		logCtx.Info(). // 记录一条信息级别的日志。
				Str("md5", fileMD5Hex).
				Str("filename", fileHeader.Filename).
				Msg("检测到重复的文件MD5，跳过处理")

		redisSpan.SetAttributes(attribute.Bool("file.duplicate", true)) // 在Redis span中标记为重复文件。
		redisSpan.End()                                                 // 结束Redis span。
		span.SetAttributes(attribute.Bool("file.duplicate", true))      // 在主span中也标记为重复文件。
		span.SetStatus(codes.Ok, "duplicate file detected")             // 主span以成功状态结束，但带有特殊消息。

		// 由于文件尚未上传到MinIO，因此无需执行异步删除操作。
		// 返回 409 Conflict 状态码，表示资源冲突（文件已存在）。
		ctx.JSON(http.StatusConflict, ResumeUploadResponse{
			SubmissionUUID: "",                                   // 因为是重复提交，不返回新的UUID。
			Status:         constants.StatusDuplicateFileSkipped, // 返回特定状态，告知客户端是因重复而跳过。
		})
		return // 终止处理。
	}
	redisSpan.End() // 结束Redis去重操作的span。

	// 3. 上传文件到MinIO对象存储。
	// 从配置中读取上传超时时间。
	uploadTimeout := h.cfg.Upload.Timeout
	if uploadTimeout <= 0 { // 如果未配置或配置不合法。
		uploadTimeout = 2 * time.Minute // 使用默认值2分钟。
	}
	// 创建一个带超时的上下文，用于控制MinIO上传操作的时间。
	uploadCtx, cancel := context.WithTimeout(c, uploadTimeout)
	defer cancel() // 使用defer确保在函数退出时取消上下文，释放相关资源。

	// 创建一个子span来追踪MinIO上传操作。
	c, minioSpan := handlerTracer.Start(c, "storage.upload_file")
	minioSpan.SetAttributes(
		attribute.String("submission_uuid", submissionUUID),       // 记录提交UUID。
		attribute.String("file.extension", ext),                   // 记录文件扩展名。
		attribute.Int64("file.size_bytes", int64(len(fileBytes))), // 记录文件大小。
	)

	// 调用MinIO存储层的方法，将文件内容上传到对象存储。
	originalObjectKey, err := h.storage.MinIO.UploadResumeFile(uploadCtx, submissionUUID, ext, bytes.NewReader(fileBytes), int64(len(fileBytes)))
	if err != nil { // 如果上传失败。
		// 检查错误类型是否是上下文超时。
		if errors.Is(err, context.DeadlineExceeded) { // 如果是超时错误。
			logCtx.Error().Err(err).Msg("上传简历到MinIO超时")                                       // 使用带UUID的日志记录器记录错误。
			minioSpan.RecordError(err)                                                        // 在MinIO span中记录错误。
			minioSpan.SetStatus(codes.Error, "upload timeout")                                // 设置MinIO span状态。
			minioSpan.End()                                                                   // 结束MinIO span。
			span.RecordError(err)                                                             // 在主span中记录错误。
			span.SetStatus(codes.Error, "minio upload timeout")                               // 设置主span状态。
			ctx.JSON(http.StatusGatewayTimeout, map[string]interface{}{"error": "上传到存储服务超时"}) // 返回 504 Gateway Timeout 错误。
			return                                                                            // 终止处理。
		}

		// 如果是其他类型的错误。
		logCtx.Error().Err(err).Msg("上传简历到MinIO失败")                                               // 使用带UUID的日志记录器记录错误。
		minioSpan.RecordError(err)                                                                // 在MinIO span中记录错误。
		minioSpan.SetStatus(codes.Error, "upload failed")                                         // 设置MinIO span状态。
		minioSpan.End()                                                                           // 结束MinIO span。
		span.RecordError(err)                                                                     // 在主span中记录错误。
		span.SetStatus(codes.Error, "minio upload failed")                                        // 设置主span状态。
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "上传简历到MinIO失败"}) // 返回 500 错误。
		return                                                                                    // 终止处理。
	}
	minioSpan.SetAttributes(attribute.String("minio.object_key", originalObjectKey)) // 在MinIO span中记录上传后在MinIO中的对象键。
	minioSpan.End()                                                                  // 结束MinIO上传的span。

	// 5. 构建将要发送到RabbitMQ的消息，以触发异步处理流程。
	message := storage2.ResumeUploadMessage{ // 创建一个简历上传消息结构体实例。
		SubmissionUUID:      submissionUUID,      // 填充提交UUID。
		OriginalFilePathOSS: originalObjectKey,   // 填充原始文件在对象存储中的路径（即对象键）。
		OriginalFilename:    fileHeader.Filename, // 填充原始文件名。
		TargetJobID:         targetJobID,         // 填充目标岗位ID。
		SourceChannel:       sourceChannel,       // 填充来源渠道。
		SubmissionTimestamp: time.Now(),          // 记录当前的提交时间戳。
		RawFileMD5:          fileMD5Hex,          // 填充文件的MD5值，用于后续流程或失败场景的回滚。

		// 兼容性字段 (保留以向后兼容旧版客户端)。
		OriginalFileObjectKey: originalObjectKey, // 再次填充对象键以兼容旧版。
	}

	// 创建一个子span来追踪消息发布到RabbitMQ的操作。
	c, mqSpan := handlerTracer.Start(c, "message.publish")
	mqSpan.SetAttributes(
		attribute.String("rabbitmq.exchange", h.cfg.RabbitMQ.ResumeEventsExchange),  // 记录目标交换机。
		attribute.String("rabbitmq.routing_key", h.cfg.RabbitMQ.UploadedRoutingKey), // 记录路由键。
		attribute.String("submission_uuid", submissionUUID),                         // 记录提交UUID。
	)

	// 调用RabbitMQ客户端的PublishJSON方法来发布消息。
	err = h.storage.RabbitMQ.PublishJSON(
		c,                                   // 传递带有追踪信息的上下文。
		h.cfg.RabbitMQ.ResumeEventsExchange, // 指定要发布的交换机名称，从配置中读取。
		h.cfg.RabbitMQ.UploadedRoutingKey,   // 指定路由键，从配置中读取。
		message,                             // 要发布的消息体（会被自动序列化为JSON）。
		true,                                // 设置消息为持久化（durable），防止MQ服务重启时丢失。
	)
	if err != nil { // 如果发布失败。
		logCtx.Error().Err(err).Msg("发布消息到RabbitMQ失败")                                               // 使用带UUID的日志记录器记录错误。
		mqSpan.RecordError(err)                                                                      // 在MQ span中记录错误。
		mqSpan.SetStatus(codes.Error, "publish failed")                                              // 设置MQ span状态。
		mqSpan.End()                                                                                 // 结束MQ span。
		span.RecordError(err)                                                                        // 在主span中记录错误。
		span.SetStatus(codes.Error, "rabbitmq publish failed")                                       // 设置主span状态。
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "发布消息到RabbitMQ失败"}) // 返回 500 错误。
		return                                                                                       // 终止处理。
	}
	mqSpan.End() // 结束消息发布的span。

	// 6. 所有同步操作成功，向客户端返回成功响应。
	span.SetStatus(codes.Ok, "resume upload successful") // 设置主span状态为成功。
	ctx.JSON(http.StatusOK, ResumeUploadResponse{        // 返回 200 OK 成功响应。
		SubmissionUUID: submissionUUID,                         // 在响应中包含提交UUID，客户端可以用它来查询处理状态。
		Status:         constants.StatusSubmittedForProcessing, // 状态为"已提交等待处理"。
	})
}

// StartResumeUploadConsumer 启动一个后台消费者，监听并处理简历上传消息。
func (h *ResumeHandler) StartResumeUploadConsumer(ctx context.Context, batchSize int, batchTimeout time.Duration) error {
	if h.storage == nil || h.storage.RabbitMQ == nil { // 检查RabbitMQ客户端是否已初始化。
		logger.Warn().Msg("RabbitMQ client is nil, skipping resume upload consumer start.") // 如果未初始化，记录警告并安全退出。
		return nil                                                                          // 返回nil，表示正常跳过。
	}
	// 记录即将使用的RabbitMQ配置信息。
	logger.Info().
		Str("exchange", h.cfg.RabbitMQ.ResumeEventsExchange).  // 日志字段：交换机名称。
		Str("routing_key", h.cfg.RabbitMQ.UploadedRoutingKey). // 日志字段：路由键。
		Msg("初始化RabbitMQ配置")                                   // 日志消息。

	// 1. 幂等地确保交换机存在，如果不存在则创建。
	if err := h.storage.RabbitMQ.EnsureExchange(h.cfg.RabbitMQ.ResumeEventsExchange, "direct", true); err != nil { // "direct"类型交换机，true表示持久化。
		return fmt.Errorf("确保交换机存在失败: %w", err) // 如果失败，返回包装后的错误，提供更多上下文。
	}

	// 幂等地确保队列存在，如果不存在则创建。
	if err := h.storage.RabbitMQ.EnsureQueue(h.cfg.RabbitMQ.RawResumeQueue, true); err != nil { // true表示持久化。
		return fmt.Errorf("确保队列存在失败: %w", err) // 如果失败，返回包装后的错误。
	}

	// 将队列绑定到交换机，这样消息才能从交换机路由到队列。
	if err := h.storage.RabbitMQ.BindQueue(
		h.cfg.RabbitMQ.RawResumeQueue,       // 绑定的队列名称。
		h.cfg.RabbitMQ.ResumeEventsExchange, // 绑定的交换机名称。
		h.cfg.RabbitMQ.UploadedRoutingKey,   // 使用此路由键进行绑定。
	); err != nil {
		return fmt.Errorf("绑定队列失败: %w", err) // 如果绑定失败，返回包装后的错误。
	}

	// 记录消费者已准备好开始工作的日志。
	logger.Info().
		Str("queue", h.cfg.RabbitMQ.RawResumeQueue). // 监听的队列。
		Int("batch_size", batchSize).                // 配置的批处理大小。
		Dur("batch_timeout", batchTimeout).          // 配置的批处理超时时间。
		Msg("简历上传批量消费者就绪")

	// 启动批量消费者。
	prefetchMultiplier := h.cfg.RabbitMQ.BatchPrefetchMultiplier // 获取预取倍数配置。
	if prefetchMultiplier <= 0 {                                 // 如果配置不合法。
		prefetchMultiplier = 2 // 使用默认值2。
	}
	prefetchCount := batchSize * prefetchMultiplier // 计算QoS的prefetch count，通常是批大小的几倍。
	// 启动批量消费者，它会在后台goroutine中运行。
	_, err := h.storage.RabbitMQ.StartBatchConsumer(h.cfg.RabbitMQ.RawResumeQueue, batchSize, batchTimeout, prefetchCount, func(deliveries []amqp091.Delivery) bool {
		// 这是处理每个批次消息的回调函数。
		// 创建一个用于追踪整个批次处理的span。
		batchCtx, batchSpan := handlerTracer.Start(context.Background(), "resume.process_batch",
			trace.WithAttributes(
				attribute.String("rabbitmq.queue", h.cfg.RabbitMQ.RawResumeQueue), // 记录队列名。
				attribute.Int("batch.size", len(deliveries)),                      // 记录当前批次的实际大小。
				attribute.Int("batch.configured_size", batchSize),                 // 记录配置的批次大小。
			))
		defer batchSpan.End() // 确保批处理span在函数结束时关闭。

		// 初始化用于存储解码后消息和数据库模型的切片。
		submissions := make([]models.ResumeSubmission, 0, len(deliveries))
		messages := make([]storage2.ResumeUploadMessage, 0, len(deliveries))
		submissionUUIDs := make([]string, 0, len(deliveries))

		// 1. 解码批次中的所有消息。
		batchCtx, decodeSpan := handlerTracer.Start(batchCtx, "message.decode_batch") // 创建解码子span。
		for _, d := range deliveries {                                                // 遍历批次中的每条投递。
			var message storage2.ResumeUploadMessage                 // 定义用于接收解码后消息的变量。
			if err := json.Unmarshal(d.Body, &message); err != nil { // 将JSON消息体解码到结构体中。
				logger.Error().Err(err).Bytes("message_body", d.Body).Msg("解析消息失败，该消息将被跳过") // 记录解码失败的错误。
				// 单个消息解析失败，不应影响批次中其他消息的处理，所以选择跳过。
				continue
			}
			messages = append(messages, message)                       // 将成功解码的消息添加到切片中。
			submissions = append(submissions, models.ResumeSubmission{ // 将消息转换为数据库模型。
				SubmissionUUID:      message.SubmissionUUID,
				OriginalFilePathOSS: message.OriginalFilePathOSS,
				OriginalFilename:    message.OriginalFilename,
				TargetJobID:         utils.StringPtr(message.TargetJobID),
				SourceChannel:       message.SourceChannel,
				SubmissionTimestamp: message.SubmissionTimestamp,
				ProcessingStatus:    constants.StatusPendingParsing, // 初始状态设置为待解析。
				RawTextMD5:          message.RawFileMD5,
			})
			submissionUUIDs = append(submissionUUIDs, message.SubmissionUUID) // 收集所有UUID用于日志和追踪。
		}
		decodeSpan.End() // 结束解码span。

		if len(submissions) == 0 { // 如果批次中没有一条消息成功解码。
			logger.Warn().Msg("批次中没有可处理的消息，全部跳过")
			batchSpan.SetStatus(codes.Ok, "no valid messages in batch") // 设置span状态。
			return true                                                 // 返回true以ack这个（空的）批次，防止消息重投。
		}

		// 在批处理span中记录所有处理的UUID，便于问题排查。
		batchSpan.SetAttributes(attribute.String("submission_uuids", strings.Join(submissionUUIDs, ",")))

		// 2. 将所有简历提交记录批量插入数据库。
		// 为数据库操作创建一个带超时的上下文，防止长时间阻塞。
		insertCtx, cancel := context.WithTimeout(batchCtx, 2*time.Minute)
		defer cancel() // 确保上下文被取消。

		batchCtx, dbSpan := handlerTracer.Start(batchCtx, "database.batch_insert", // 创建数据库操作子span。
			trace.WithAttributes(
				attribute.Int("submissions.count", len(submissions)), // 记录插入的记录数。
			))

		if err := h.storage.MySQL.BatchInsertResumeSubmissions(insertCtx, submissions); err != nil { // 执行批量插入。
			logger.Error().Err(err).Int("batch_size", len(submissions)).Msg("批量插入初始简历提交记录失败")
			dbSpan.RecordError(err)                                          // 在DB span中记录错误。
			dbSpan.SetStatus(codes.Error, "batch insert failed")             // 设置DB span状态。
			dbSpan.End()                                                     // 结束DB span。
			batchSpan.RecordError(err)                                       // 在批处理span中记录错误。
			batchSpan.SetStatus(codes.Error, "database batch insert failed") // 设置批处理span状态。

			// 补偿操作：如果DB批量插入失败，这是一个严重问题，意味着简历已上传但无法记录。
			// 我们需要从Redis中移除这些文件的MD5记录，以便它们可以被重新上传和处理。
			// 开启一个新的goroutine在后台执行补偿操作，不阻塞当前消费者。
			go func(msgsToRollback []storage2.ResumeUploadMessage, parentCtx context.Context) {
				// 从父span创建一个后台任务span，以连接追踪链路。
				bgCtx, bgSpan := handlerTracer.Start(parentCtx, "CompensationRedisMD5Removal")
				defer bgSpan.End() // 确保后台span结束。

				for _, msg := range msgsToRollback { // 遍历需要回滚的消息。
					if msg.RawFileMD5 != "" { // 确保MD5存在。
						if err := h.storage.Redis.RemoveRawFileMD5(bgCtx, msg.RawFileMD5); err != nil { // 从Redis Set中移除MD5。
							logger.Error().Err(err).Str("md5", msg.RawFileMD5).Msg("补偿操作：异步删除Redis中的文件MD5失败")
							bgSpan.RecordError(err) // 记录可能发生的错误。
						}
					}
				}
				bgSpan.SetAttributes(attribute.Int("removed_md5_count", len(msgsToRollback))) // 记录移除的MD5数量。
			}(messages, batchCtx) // 将当前批次的消息和追踪上下文传入。

			return false // 返回false，表示批次处理失败。MQ将nack整个批次，稍后会重试。
		}
		dbSpan.End() // 结束数据库操作span。

		// 3. 数据库插入成功后，逐个调用Processor模块对每个简历进行后续处理（如文件解析）。
		successCount := 0                                                                  // 初始化成功计数器。
		failCount := 0                                                                     // 初始化失败计数器。
		batchCtx, processorSpan := handlerTracer.Start(batchCtx, "ProcessUploadedResumes") // 创建处理器span。

		for _, message := range messages { // 遍历成功解码的消息。
			// 为每个消息的后续处理创建一个独立的、带超时的上下文。
			msgCtx, msgCancel := context.WithTimeout(batchCtx, 2*time.Minute)
			if err := h.processorModule.ProcessUploadedResume(msgCtx, message, h.cfg); err != nil { // 调用核心处理逻辑。
				logger.Error().
					Err(err).
					Str(constants.LogKeySubmissionUUID, message.SubmissionUUID).
					Msg("ResumeProcessor 处理上传简历失败（在批量插入后）")
				failCount++ // 失败计数加一。
				// 注意：即使单个processor处理失败，我们也不nack整个批次。
				// 因为DB记录已经插入，nack会导致重复插入。
				// Processor内部应该负责将数据库中对应记录的状态更新为失败。
			} else {
				successCount++ // 成功计数加一。
			}
			msgCancel() // 取消单个消息的处理上下文。
		}

		processorSpan.SetAttributes(
			attribute.Int("process.success_count", successCount), // 记录成功数。
			attribute.Int("process.failure_count", failCount),    // 记录失败数。
		)
		processorSpan.End() // 结束处理器span。

		// 在批处理span中记录最终的处理结果。
		batchSpan.SetAttributes(
			attribute.Int("process.success_count", successCount),
			attribute.Int("process.failure_count", failCount),
		)

		if failCount > 0 { // 如果有失败的。
			batchSpan.SetStatus(codes.Ok, fmt.Sprintf("partial success: %d/%d", successCount, len(messages))) // 标记为部分成功。
		} else {
			batchSpan.SetStatus(codes.Ok, "batch processing successful") // 标记为全部成功。
		}

		return true // 返回true，表示批次处理成功，MQ将ack整个批次中的所有消息。
	})

	if err != nil { // 如果启动消费者的过程（如连接MQ）本身就失败了。
		return fmt.Errorf("启动批量消费者失败: %w", err) // 返回包装后的错误。
	}

	return nil // 消费者成功启动，返回nil。
}

// StartLLMParsingConsumer 启动一个消费者，用于处理需要LLM（大语言模型）进行解析的任务。
func (h *ResumeHandler) StartLLMParsingConsumer(ctx context.Context, prefetchCount int) error { // ctx: 上下文, prefetchCount: 预取数量
	if h.storage == nil || h.storage.RabbitMQ == nil { // 检查RabbitMQ客户端是否已初始化。
		logger.Warn().Msg("RabbitMQ client is nil, skipping LLM parsing consumer start.") // 如果未初始化，记录警告并跳过。
		return nil                                                                        // 返回nil，表示正常跳过。
	}
	// 1. 幂等地确保交换机和队列存在。
	if err := h.storage.RabbitMQ.EnsureExchange(h.cfg.RabbitMQ.ProcessingEventsExchange, "direct", true); err != nil { // 确保处理事件的交换机存在。
		return fmt.Errorf("确保交换机存在失败: %w", err) // 返回错误。
	}

	if err := h.storage.RabbitMQ.EnsureQueue(h.cfg.RabbitMQ.LLMParsingQueue, true); err != nil { // 确保LLM解析队列存在。
		return fmt.Errorf("确保队列存在失败: %w", err) // 返回错误。
	}

	if err := h.storage.RabbitMQ.BindQueue( // 将LLM解析队列绑定到处理事件的交换机。
		h.cfg.RabbitMQ.LLMParsingQueue,          // 队列名称。
		h.cfg.RabbitMQ.ProcessingEventsExchange, // 交换机名称。
		h.cfg.RabbitMQ.ParsedRoutingKey,         // 使用"parsed"路由键进行绑定，这样只有文本解析完成的消息会到这里。
	); err != nil {
		return fmt.Errorf("绑定队列失败: %w", err) // 返回错误。
	}

	logger.Info(). // 记录消费者准备就绪的日志。
			Str("queue", h.cfg.RabbitMQ.LLMParsingQueue). // 日志字段：监听的队列名。
			Int("prefetch_count", prefetchCount).         // 日志字段：预取数量（QoS设置），控制一次从MQ获取多少条消息。
			Msg("LLM解析消费者就绪")                             // 日志消息。

	// 2. 启动单条消息的消费者。
	_, err := h.storage.RabbitMQ.StartConsumer(h.cfg.RabbitMQ.LLMParsingQueue, prefetchCount, func(data []byte) bool { // 启动消费者，传入处理函数。
		// 创建一个处理单条消息的span。
		consumerCtx, span := handlerTracer.Start(context.Background(), "ProcessLLMParsingMessage",
			trace.WithAttributes(
				attribute.String("rabbitmq.queue", h.cfg.RabbitMQ.LLMParsingQueue), // 记录队列名。
				attribute.Int("message.size_bytes", len(data)),                     // 记录消息大小。
			))
		defer span.End() // 确保span结束。

		var message storage2.ResumeProcessingMessage           // 定义用于接收消息的结构体变量。
		if err := json.Unmarshal(data, &message); err != nil { // 解析JSON消息体。
			logger.Error(). // 记录错误日志。
					Err(err).     // 错误详情。
					Msg("解析消息失败") // 错误消息。
			span.RecordError(err)                                      // 在span中记录错误。
			span.SetStatus(codes.Error, "failed to unmarshal message") // 设置span状态。
			return false                                               // 返回false表示消息处理失败，MQ会根据策略重试或发送到死信队列。
		}

		// 在span中记录消息的关键信息。
		span.SetAttributes(
			attribute.String("submission_uuid", message.SubmissionUUID),
			attribute.String("target_job_id", message.TargetJobID),
			attribute.String("parsed_text_path", message.ParsedTextPathOSS),
		)

		// 为每个消息的处理创建一个独立的、带超时的上下文，以隔离处理并防止任务无限期运行。
		msgCtx, cancel := context.WithTimeout(consumerCtx, 5*time.Minute) // LLM处理可能耗时较长，设置5分钟超时。
		defer cancel()                                                    // 确保上下文被取消。

		// 调用Processor模块进行核心的LLM处理。
		if err := h.processorModule.ProcessLLMTasks(msgCtx, message, h.cfg); err != nil { // 调用处理器处理LLM任务
			logger.Error(). // 记录错误日志。
					Err(err).                                                    // 错误详情。
					Str(constants.LogKeySubmissionUUID, message.SubmissionUUID). // 提交UUID。
					Msg("ResumeProcessor 处理LLM任务失败")                             // 错误消息。
			span.RecordError(err)                                // 在span中记录错误。
			span.SetStatus(codes.Error, "llm processing failed") // 设置span状态。
			// 错误状态的更新应由 ProcessLLMTasks 内部或其调用者负责，这里只返回处理结果。
			return false // 处理失败，消息处理失败
		}

		span.SetStatus(codes.Ok, "llm processing successful") // 设置span状态为成功。
		return true                                           // 消息处理成功
	})

	if err != nil { // 如果启动消费者失败。
		return fmt.Errorf("启动消费者失败: %w", err) // 返回错误。
	}

	return nil // 成功启动，返回nil。
}

// StartMD5CleanupTask 启动一个后台定时任务，用于定期清理和维护Redis中的MD5去重记录。
// 此方法是可选的，用于确保MD5记录不会无限期地占用内存。
func (h *ResumeHandler) StartMD5CleanupTask(ctx context.Context) { // ctx: 用于控制任务生命周期的上下文
	// 定义清理任务的执行间隔，这里设置为每周执行一次。
	cleanupInterval := 7 * 24 * time.Hour

	logger.Info(). // 记录任务启动日志。
			Dur("interval", cleanupInterval). // 日志字段：清理间隔。
			Msg("启动MD5记录清理任务")                // 日志消息。

	ticker := time.NewTicker(cleanupInterval) // 创建一个定时器，按指定的间隔周期性地触发。
	defer ticker.Stop()                       // 确保在函数退出时停止定时器，释放相关资源。

	// 在启动任务时，立即执行一次清理，以确保服务启动后状态是正确的。
	h.cleanupMD5Records(ctx)

	for { // 无限循环，使任务持续在后台运行。
		select { // 使用select监听多个通道事件。
		case <-ticker.C: // 当定时器触发时（即一个清理间隔过去后）。
			h.cleanupMD5Records(ctx) // 执行清理操作。
		case <-ctx.Done(): // 当传入的上下文被取消时（通常在服务正常关闭时发生）。
			logger.Info().Msg("MD5记录清理任务退出") // 记录任务正常退出的信息。
			return                           // 退出循环和函数。
		}
	}
}

// cleanupMD5Records 是实际执行MD5记录清理的函数。
// 它的主要逻辑是检查用于去重的Redis Set键是否设置了过期时间，如果没有，则为其设置一个默认的过期时间。
func (h *ResumeHandler) cleanupMD5Records(ctx context.Context) { // ctx: 上下文
	logger.Info().Msg("执行MD5记录清理任务...") // 记录开始执行清理任务的日志。

	// 检查和设置原始文件MD5集合的过期时间。
	ttlFile, errFile := h.storage.Redis.Client.TTL(ctx, constants.RawFileMD5SetKey).Result() // 获取原始文件MD5集合键的TTL（剩余生存时间）。
	if errFile != nil {                                                                      // 如果获取TTL失败。
		logger.Error().Err(errFile).Str("setKey", constants.RawFileMD5SetKey).Msg("获取文件MD5集合过期时间失败") // 记录错误。
	} else if ttlFile < 0 { // 如果TTL为负数（-1表示无过期时间，-2表示键不存在），则需要设置。
		expiry := h.storage.Redis.GetMD5ExpireDuration()                                                     // 从配置或默认值获取MD5记录的过期时长。
		if err := h.storage.Redis.Client.Expire(ctx, constants.RawFileMD5SetKey, expiry).Err(); err != nil { // 为该键设置过期时间。
			logger.Error().Err(err).Str("setKey", constants.RawFileMD5SetKey).Msg("设置文件MD5集合过期时间失败") // 记录设置失败的错误。
		} else { // 如果设置成功。
			logger.Info().Str("setKey", constants.RawFileMD5SetKey).Dur("expiry", expiry).Msg("成功设置文件MD5集合过期时间") // 记录成功设置的信息。
		}
	}

	// 检查和设置解析后文本MD5集合的过期时间。
	ttlText, errText := h.storage.Redis.Client.TTL(ctx, constants.ParsedTextMD5SetKey).Result() // 获取解析后文本MD5集合键的TTL。
	if errText != nil {                                                                         // 如果获取TTL失败。
		logger.Error().Err(errText).Str("setKey", constants.ParsedTextMD5SetKey).Msg("获取文本MD5集合过期时间失败") // 记录错误。
	} else if ttlText < 0 { // 如果TTL为负数，需要设置。
		expiry := h.storage.Redis.GetMD5ExpireDuration()                                                        // 获取过期时长。
		if err := h.storage.Redis.Client.Expire(ctx, constants.ParsedTextMD5SetKey, expiry).Err(); err != nil { // 为该键设置过期时间。
			logger.Error().Err(err).Str("setKey", constants.ParsedTextMD5SetKey).Msg("设置文本MD5集合过期时间失败") // 记录设置失败的错误。
		} else { // 如果设置成功。
			logger.Info().Str("setKey", constants.ParsedTextMD5SetKey).Dur("expiry", expiry).Msg("成功设置文本MD5集合过期时间") // 记录成功设置的信息。
		}
	}

	logger.Info().Msg("MD5记录清理任务完成") // 记录清理任务执行完毕。
}
