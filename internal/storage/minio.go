package storage // 定义了 storage 包，用于处理对象存储相关的操作

import (
	"ai-agent-go/internal/config" // 导入内部配置包，用于获取MinIO的配置信息
	"bytes"                       // 导入 bytes 包，用于处理字节切片，例如将字节切片转换为 io.Reader
	"context"                     // 导入 context 包，用于在API调用之间传递截止日期、取消信号和其他请求范围的值
	"crypto/md5"                  // 导入 md5 包，用于计算文件的MD5哈希值
	"encoding/hex"                // 导入 hex 包，用于将字节切片编码为十六进制字符串
	"fmt"                         // 导入 fmt 包，用于格式化字符串和打印错误信息
	"io"                          // 导入 io 包，提供基本的I/O接口，如 io.Reader
	"log"                         // 导入 log 包，用于记录日志信息
	"net/http"                    // 导入 net/http 包，用于配置HTTP客户端的传输层
	"strings"                     // 导入 strings 包，用于处理字符串操作
	"time"                        // 导入 time 包，用于处理时间相关的操作，如URL的过期时间

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"     // 导入 MinIO 凭证包，用于身份验证
	"github.com/minio/minio-go/v7/pkg/lifecycle"       // 导入 MinIO 生命周期管理包，用于设置对象的自动删除规则
	"go.opentelemetry.io/otel"                         // 导入 OpenTelemetry 的主包
	"go.opentelemetry.io/otel/attribute"               // 导入 OpenTelemetry 的属性包，用于为追踪(span)添加键值对属性
	"go.opentelemetry.io/otel/codes"                   // 导入 OpenTelemetry 的状态码包，用于标记追踪的状态（如成功或错误）
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0" // 导入 OpenTelemetry 的语义约定包，提供标准的追踪属性键
	"go.opentelemetry.io/otel/trace"                   // 导入 OpenTelemetry 的追踪包，用于创建和管理追踪(span)
)

var (
	// utf8BOM 是UTF-8文件的字节顺序标记。
	// 将其添加到文件开头可以明确指示文件编码为UTF-8，
	// 从而提高跨系统和软件的兼容性，避免文本解析时出现乱码。
	utf8BOM = []byte{0xEF, 0xBB, 0xBF}
)

var minioTracer = otel.Tracer("storage/minio") // 创建一个名为 "storage/minio" 的 OpenTelemetry 追踪器

// ObjectStorage 对象存储接口
type ObjectStorage interface {
	// UploadFile 上传文件到指定路径
	UploadFile(ctx context.Context, objectName string, reader io.Reader, fileSize int64, contentType string) (string, error)

	// DownloadFile 下载文件
	DownloadFile(ctx context.Context, objectName string) ([]byte, error)

	// GetPresignedURL 获取预签名URL
	GetPresignedURL(ctx context.Context, objectName string, expiry time.Duration) (string, error)

	// DeleteFile 删除文件
	DeleteFile(ctx context.Context, objectName string) error

	// UploadResumeFile ResumeSpecific 简历特定操作
	UploadResumeFile(ctx context.Context, submissionUUID, fileExt string, reader io.Reader, fileSize int64) (string, error)
	// UploadParsedText 上传解析后的文本
	UploadParsedText(ctx context.Context, submissionUUID string, text string) (string, error)
	// GetResumeFile 获取简历文件
	GetResumeFile(ctx context.Context, objectName string) ([]byte, error)
	// GetParsedText 获取解析后的文本
	GetParsedText(ctx context.Context, objectName string) (string, error)

	// UploadResumeFileStreaming 新增: 流式上传并计算MD5
	UploadResumeFileStreaming(ctx context.Context, submissionUUID, fileExt string, reader io.Reader, fileSize int64) (string, string, error)
}

// 确保MinIO实现了ObjectStorage接口
var _ ObjectStorage = (*MinIO)(nil) // 编译时检查，确保 MinIO 类型实现了 ObjectStorage 接口的所有方法

// MinIO 提供对象存储功能
type MinIO struct {
	client         *minio.Client       // MinIO 客户端实例
	cfg            *config.MinIOConfig // MinIO 配置信息
	originalBucket string              // 存储原始文件的桶名称
	parsedBucket   string              // 存储解析后文本的桶名称
	logger         *log.Logger         // 日志记录器
}

// NewMinIO 创建MinIO客户端
func NewMinIO(ctx context.Context, cfg *config.MinIOConfig, logger *log.Logger) (*MinIO, error) {
	if cfg == nil { // 检查配置是否为空
		return nil, fmt.Errorf("MinIO配置不能为空")
	}
	if logger == nil { // 如果没有提供日志记录器，则创建一个丢弃所有日志的实例
		logger = log.New(io.Discard, "", 0)
	}
	logger.Printf("[MinIO] Initializing MinIO client with endpoint: %s, originalBucket: %s, parsedBucket: %s", cfg.Endpoint, cfg.OriginalsBucket, cfg.ParsedTextBucket) // 记录初始化日志

	// 创建MinIO客户端
	client, err := minio.New(cfg.Endpoint, &minio.Options{ // 使用配置信息创建新的 MinIO 客户端
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""), // 设置静态凭证
		Secure: cfg.UseSSL,                                                        // 根据配置决定是否使用 SSL/TLS
		Transport: &http.Transport{ // 自定义 HTTP Transport 以优化连接
			MaxIdleConnsPerHost:   10,               // 每个主机的最大空闲连接数
			ResponseHeaderTimeout: 30 * time.Second, // 读取响应头的超时时间
			TLSHandshakeTimeout:   10 * time.Second, // TLS 握手超时时间
		},
	})
	if err != nil { // 检查客户端创建是否出错
		logger.Printf("[MinIO] Initialization failed: %v", err) // 记录初始化失败日志
		return nil, fmt.Errorf("创建MinIO客户端失败: %w", err)         // 返回包含原始错误的错误信息
	}

	// 设置存储桶名称
	originalBucket := cfg.OriginalsBucket // 获取原始文件桶名称
	if originalBucket == "" {             // 如果 OriginalsBucket 为空
		originalBucket = cfg.BucketName // 则使用旧的 BucketName 字段，以实现向后兼容
	}

	parsedBucket := cfg.ParsedTextBucket // 获取解析文本桶名称
	if parsedBucket == "" {              // 如果 ParsedTextBucket 为空
		parsedBucket = "parsed-text" // 则使用默认值 "parsed-text"
	}

	m := &MinIO{ // 创建 MinIO 结构体实例
		client:         client,         // 赋值 MinIO 客户端
		cfg:            cfg,            // 赋值配置
		originalBucket: originalBucket, // 赋值原始文件桶名称
		parsedBucket:   parsedBucket,   // 赋值解析文本桶名称
		logger:         logger,         // 赋值日志记录器
	}

	// 使用传入的上下文，保持跟踪链连续
	err = m.EnsureBucketExists(ctx, originalBucket, cfg.Location) // 确保原始文件桶存在
	if err != nil {                                               // 如果出错
		logger.Printf("[MinIO] Failed to ensure original bucket %s exists: %v", originalBucket, err) // 记录错误日志
		return nil, fmt.Errorf("确保原始简历存储桶 %s 存在失败: %w", originalBucket, err)                         // 返回错误
	}

	err = m.EnsureBucketExists(ctx, parsedBucket, cfg.Location) // 确保解析文本桶存在
	if err != nil {                                             // 如果出错
		logger.Printf("[MinIO] Failed to ensure parsed bucket %s exists: %v", parsedBucket, err) // 记录错误日志
		return nil, fmt.Errorf("确保解析文本存储桶 %s 存在失败: %w", parsedBucket, err)                       // 返回错误
	}

	// 设置生命周期规则
	if cfg.OriginalFileExpireDays > 0 || cfg.ParsedTextExpireDays > 0 { // 如果配置了文件过期天数
		err = m.setupLifecycleRules(ctx) // 使用传入的上下文设置生命周期规则
		if err != nil {                  // 如果设置失败
			logger.Printf("[MinIO] Warning: Failed to set up lifecycle rules: %v", err) // 记录警告日志，但不中断程序
		}
	}

	logger.Printf("[MinIO] Client initialized successfully for endpoint: %s", cfg.Endpoint) // 记录客户端初始化成功日志
	return m, nil                                                                           // 返回 MinIO 实例和 nil 错误
}

// 移除了命名冲突的私有方法，直接调用导出版本

// EnsureBucketExists checks if a bucket exists and creates it if it doesn't.
func (m *MinIO) EnsureBucketExists(ctx context.Context, bucketName string, location string) error {
	// 创建一个名为 "MinIO.EnsureBucketExists" 的 OpenTelemetry 追踪(span)
	ctx, span := minioTracer.Start(ctx, "MinIO.EnsureBucketExists",
		trace.WithSpanKind(trace.SpanKindClient)) // 标记此 span 为客户端类型
	defer span.End() // 确保在函数退出时结束该 span

	span.SetAttributes( // 为 span 添加属性，便于追踪和分析
		semconv.DBSystemKey.String("s3"),                               // 数据库系统为 S3
		attribute.String("minio.endpoint", m.cfg.Endpoint),             // MinIO 端点地址
		attribute.String("minio.bucket", bucketName),                   // 桶名称
		attribute.String("minio.operation", "BucketExists/MakeBucket"), // 操作类型
		attribute.String("net.peer.name", m.cfg.Endpoint),              // 网络对端名称
	)

	// Check if the bucket exists
	exists, err := m.client.BucketExists(ctx, bucketName) // 调用 MinIO 客户端检查桶是否存在
	if err != nil {                                       // 如果检查出错
		span.RecordError(err)                    // 在 span 中记录错误
		span.SetStatus(codes.Error, err.Error()) // 设置 span 状态为错误
		return fmt.Errorf("检查桶是否存在失败: %w", err)  // 返回包装后的错误
	}

	// If the bucket doesn't exist, create it
	if !exists { // 如果桶不存在
		opt := minio.MakeBucketOptions{} // 创建创建桶的选项
		if location != "" {              // 如果指定了地理位置
			opt.Region = location // 设置区域
		}
		if err := m.client.MakeBucket(ctx, bucketName, opt); err != nil { // 调用 MinIO 客户端创建桶
			span.RecordError(err)                    // 在 span 中记录错误
			span.SetStatus(codes.Error, err.Error()) // 设置 span 状态为错误
			return fmt.Errorf("创建桶失败: %w", err)      // 返回包装后的错误
		}
		span.SetAttributes(attribute.Bool("bucket.created", true)) // 在 span 中标记桶已被创建

		// Log creation (retain original logging)
		if m.logger != nil { // 如果日志记录器存在
			m.logger.Printf("[MinIO] Created bucket %s", bucketName) // 记录创建桶的日志
		}
	} else { // 如果桶已存在
		span.SetAttributes(attribute.Bool("bucket.created", false)) // 在 span 中标记桶未被创建（因为已存在）

		// Log skipped creation (retain original logging)
		if m.logger != nil { // 如果日志记录器存在
			m.logger.Printf("[MinIO] Bucket %s already exists", bucketName) // 记录桶已存在的日志
		}
	}

	span.SetStatus(codes.Ok, "") // 设置 span 状态为成功
	return nil                   // 返回 nil 表示成功
}

// setupLifecycleRules 设置对象生命周期规则
func (m *MinIO) setupLifecycleRules(ctx context.Context) error {
	m.logger.Printf("[MinIO] Setting up lifecycle rules...") // 记录开始设置生命周期规则的日志
	if m.cfg.OriginalFileExpireDays > 0 {                    // 如果配置了原始文件的过期天数
		if err := m.setupBucketLifecycle(ctx, m.originalBucket, "expire-originals", m.cfg.OriginalFileExpireDays); err != nil { // 为原始文件桶设置生命周期
			return fmt.Errorf("为原始文件存储桶 %s 设置生命周期失败: %w", m.originalBucket, err) // 返回错误
		}
	}
	if m.cfg.ParsedTextExpireDays > 0 { // 如果配置了解析文本的过期天数
		if err := m.setupBucketLifecycle(ctx, m.parsedBucket, "expire-parsed-text", m.cfg.ParsedTextExpireDays); err != nil { // 为解析文本桶设置生命周期
			return fmt.Errorf("为解析文本存储桶 %s 设置生命周期失败: %w", m.parsedBucket, err) // 返回错误
		}
	}
	m.logger.Printf("[MinIO] Lifecycle rules setup completed.") // 记录生命周期规则设置完成的日志
	return nil                                                  // 返回 nil 表示成功
}

// setupBucketLifecycle 为指定存储桶设置生命周期规则
func (m *MinIO) setupBucketLifecycle(ctx context.Context, bucketName, ruleID string, expiryDays int) error {
	m.logger.Printf("[MinIO] Setting lifecycle rule for bucket %s: ID=%s, ExpiryDays=%d", bucketName, ruleID, expiryDays) // 记录设置规则的详细日志
	cfg := lifecycle.NewConfiguration()                                                                                   // 创建一个新的生命周期配置对象
	// lifecycle.Configuration 结构体没有 Region 字段，Region 只在创建桶时使用一次
	cfg.Rules = []lifecycle.Rule{ // 定义生命周期规则
		{
			ID:     ruleID,    // 规则ID
			Status: "Enabled", // 规则状态：启用
			Expiration: lifecycle.Expiration{ // 设置过期策略
				Days: lifecycle.ExpirationDays(expiryDays), // 指定在 expiryDays 天后过期
			},
		},
	}

	err := m.client.SetBucketLifecycle(ctx, bucketName, cfg) // 调用 MinIO 客户端为桶设置生命周期配置
	if err != nil {                                          // 如果出错
		m.logger.Printf("[MinIO] Error setting lifecycle for bucket %s: %v", bucketName, err) // 记录错误日志
		return fmt.Errorf("设置存储桶 %s 的生命周期规则失败: %w", bucketName, err)                          // 返回包装后的错误
	}
	m.logger.Printf("[MinIO] Successfully set lifecycle for bucket %s.", bucketName) // 记录成功日志
	return nil                                                                       // 返回 nil 表示成功
}

// UploadFile 上传文件到指定路径
func (m *MinIO) UploadFile(ctx context.Context, objectName string, reader io.Reader, fileSize int64, contentType string) (string, error) {
	// 创建一个名为 "MinIO.UploadFile" 的 OpenTelemetry 追踪(span)
	ctx, span := minioTracer.Start(ctx, "MinIO.UploadFile",
		trace.WithSpanKind(trace.SpanKindClient)) // 标记为客户端类型
	defer span.End() // 确保在函数退出时结束该 span

	m.logger.Printf("[MinIO] Uploading file: ObjectName=%s, Size=%d, ContentType=%s", objectName, fileSize, contentType) // 记录上传文件的日志

	// 默认使用配置中的主 bucketName，如果 objectName 包含 bucket 信息，则优先使用 objectName 中的
	bucketToUse := m.cfg.BucketName        // 默认使用的桶
	actualObjectName := objectName         // 实际的对象键
	if strings.Contains(objectName, "/") { // 如果对象名包含斜杠，可能指定了桶名
		parts := strings.SplitN(objectName, "/", 2) // 按第一个斜杠分割
		if len(parts) == 2 {                        // 如果分割成两部分
			// Check if parts[0] is one of the known buckets to avoid accidental bucket creation via objectName
			// 检查第一部分是否为已知的桶名，以避免通过对象名意外创建桶
			if parts[0] == m.originalBucket || parts[0] == m.parsedBucket || parts[0] == m.cfg.BucketName {
				bucketToUse = parts[0]                                                                                                    // 使用指定的桶名
				actualObjectName = parts[1]                                                                                               // 第二部分作为实际的对象键
				m.logger.Printf("[MinIO] Using bucket '%s' and object key '%s' from provided objectName.", bucketToUse, actualObjectName) // 记录日志
			} else {
				// 未知桶名直接返回错误，避免数据错位或越权
				return "", fmt.Errorf("未授权的桶前缀 '%s'，objectName '%s' 不允许访问", parts[0], objectName)
			}
		}
	}

	// 添加span属性
	span.SetAttributes(
		attribute.String("object.storage.operation", "put"),          // 操作类型：put
		attribute.String("object.storage.bucket", bucketToUse),       // 桶名称
		attribute.String("object.storage.key", actualObjectName),     // 对象键
		attribute.String("net.peer.name", m.cfg.Endpoint),            // 网络对端名称
		attribute.Int64("object.storage.content_length", fileSize),   // 内容长度
		attribute.String("object.storage.content_type", contentType), // 内容类型
	)

	// 如果配置允许，并且提供了logger，则记录输入
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadFile] Attempting to upload: ObjectName='%s', FileSize=%d, ContentType='%s', Bucket='%s'", actualObjectName, fileSize, contentType, bucketToUse)
	}

	uploadInfo, err := m.client.PutObject(ctx, bucketToUse, actualObjectName, reader, fileSize, minio.PutObjectOptions{ContentType: contentType}) // 调用 MinIO 客户端上传对象
	if err != nil {                                                                                                                               // 如果上传失败
		// 记录错误
		span.RecordError(err)                    // 在 span 中记录错误
		span.SetStatus(codes.Error, err.Error()) // 设置 span 状态为错误

		// 如果配置允许，并且提供了logger，则记录错误
		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-UploadFile] Error uploading %s: %v", actualObjectName, err)
		}
		return "", fmt.Errorf("上传对象 %s/%s 失败: %w", bucketToUse, actualObjectName, err) // 返回包装后的错误
	}

	// 添加成功信息
	span.SetAttributes(
		attribute.String("object.storage.etag", uploadInfo.ETag), // 上传对象的 ETag
		attribute.Int64("object.storage.size", uploadInfo.Size),  // 上传对象的实际大小
	)
	span.SetStatus(codes.Ok, "") // 设置 span 状态为成功

	// 如果配置允许，并且提供了logger，则记录成功信息
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadFile] Successfully uploaded %s, ETag: %s, Size: %d, Path: %s", actualObjectName, uploadInfo.ETag, uploadInfo.Size, actualObjectName)
	}
	return actualObjectName, nil // 返回实际的对象键和 nil 错误
}

// UploadFileFromBytes 从字节数组上传文件 (私有辅助方法)
func (m *MinIO) uploadFileFromBytes(ctx context.Context, objectName string, data []byte, contentType string) (string, error) {
	return m.UploadFile(ctx, objectName, bytes.NewReader(data), int64(len(data)), contentType) // 调用通用上传方法，使用 bytes.NewReader 将字节切片转换为 io.Reader
}

// UploadResumeFile 上传原始简历文件到MinIO的originalsBucket
// 返回MinIO中的对象键 (不含bucket前缀)
func (m *MinIO) UploadResumeFile(ctx context.Context, submissionUUID, fileExt string, reader io.Reader, fileSize int64) (string, error) {
	// 创建一个名为 "MinIO.UploadResumeFile" 的 OpenTelemetry 追踪(span)
	ctx, span := minioTracer.Start(ctx, "MinIO.UploadResumeFile",
		trace.WithSpanKind(trace.SpanKindClient)) // 标记为客户端类型
	defer span.End() // 确保在函数退出时结束该 span

	// 构建对象名称，例如: resume/submissionUUID/original.pdf
	objectName := fmt.Sprintf("resume/%s/original%s", submissionUUID, fileExt)
	contentType := getContentType(fileExt) // 根据文件扩展名获取 MIME 类型

	// 添加span属性
	span.SetAttributes(
		attribute.String("object.storage.operation", "put"),          // 操作类型：put
		attribute.String("object.storage.bucket", m.originalBucket),  // 桶名称
		attribute.String("object.storage.key", objectName),           // 对象键
		attribute.String("net.peer.name", m.cfg.Endpoint),            // 网络对端名称
		attribute.Int64("object.storage.content_length", fileSize),   // 内容长度
		attribute.String("object.storage.content_type", contentType), // 内容类型
		attribute.String("resume.submission_uuid", submissionUUID),   // 简历提交的 UUID
		attribute.String("resume.file_ext", fileExt),                 // 简历文件扩展名
	)

	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadResumeFile] Uploading: SubmissionUUID='%s', FileExt='%s', ObjectName='%s', Bucket='%s'", submissionUUID, fileExt, objectName, m.originalBucket)
	}

	// 调用通用的 UploadFile 方法，它会将文件上传到 m.originalBucket
	// UploadFile 方法已被修正为仅返回对象键 (objectName)
	// 注意：这里将 m.originalBucket 作为前缀传入，UploadFile 方法会正确解析
	uploadedObjectName, err := m.UploadFile(ctx, fmt.Sprintf("%s/%s", m.originalBucket, objectName), reader, fileSize, contentType)
	if err != nil { // 如果上传失败
		span.RecordError(err)                    // 在 span 中记录错误
		span.SetStatus(codes.Error, err.Error()) // 设置 span 状态为错误

		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-UploadResumeFile] Error during UploadFile call: %v", err)
		}
		return "", fmt.Errorf("上传简历文件失败: %w", err) // 使用 errwrap 包装错误
	}

	// 确认 UploadFile 返回的是预期的 objectName (在启用了测试日志时)
	// 正常情况下，uploadedObjectName 应该等于 objectName
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard && uploadedObjectName != objectName {
		m.logger.Printf("[MinIO-UploadResumeFile] Warning: UploadFile returned '%s' but expected '%s'", uploadedObjectName, objectName)
	}

	// Log success
	span.SetStatus(codes.Ok, "") // 设置 span 状态为成功
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadResumeFile] Successfully processed and initiated upload for %s to bucket %s", objectName, m.originalBucket)
	}

	return objectName, nil // 确保返回的是最初构建的 objectName
}

// DeleteResumeFile 从originalsBucket中删除指定的简历文件
func (m *MinIO) DeleteResumeFile(ctx context.Context, objectKey string) error {
	m.logger.Printf("[MinIO] Deleting original resume file: ObjectKey=%s, Bucket=%s", objectKey, m.originalBucket) // 记录删除日志
	err := m.client.RemoveObject(ctx, m.originalBucket, objectKey, minio.RemoveObjectOptions{})                    // 调用 MinIO 客户端删除对象
	if err != nil {                                                                                                // 如果删除失败
		m.logger.Printf("[MinIO] Error deleting object %s from bucket %s: %v", objectKey, m.originalBucket, err) // 记录错误日志
		return fmt.Errorf("从MinIO删除对象 %s 失败: %w", objectKey, err)                                                // 返回包装后的错误
	}
	return nil // 返回 nil 表示成功
}

// UploadParsedText 上传解析后的文本
func (m *MinIO) UploadParsedText(ctx context.Context, submissionUUID string, text string) (string, error) {
	ctx, span := minioTracer.Start(ctx, "MinIO.UploadParsedText",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	objectName := fmt.Sprintf("%s.txt", submissionUUID)
	contentType := "text/plain; charset=utf-8"

	// 创建一个新的字节缓冲区，先写入UTF-8 BOM，再写入文本内容。
	// 这确保了文件被明确标识为UTF-8编码，防止乱码问题。
	var buffer bytes.Buffer
	buffer.Write(utf8BOM)
	buffer.WriteString(text)

	// 使用新的带BOM的缓冲区内容
	content := buffer.Bytes()

	// 在上传前记录日志
	m.logger.Printf("[MinIO] Uploading parsed text: Bucket=%s, ObjectName=%s, Size=%d", m.parsedBucket, objectName, len(content))

	// 调用内部的上传方法
	_, err := m.client.PutObject(ctx, m.parsedBucket, objectName, &buffer, int64(buffer.Len()), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		m.logger.Printf("[MinIO] Error uploading parsed text to %s/%s: %v", m.parsedBucket, objectName, err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to upload parsed text")
		return "", fmt.Errorf("上传解析文本到 %s/%s 失败: %w", m.parsedBucket, objectName, err)
	}

	span.SetAttributes(
		attribute.String("minio.bucket", m.parsedBucket),
		attribute.String("minio.object_name", objectName),
		attribute.Int64("minio.content_length", int64(buffer.Len())),
	)
	span.SetStatus(codes.Ok, "parsed text uploaded successfully")
	m.logger.Printf("[MinIO] Successfully uploaded parsed text to %s/%s", m.parsedBucket, objectName)
	return objectName, nil
}

// DownloadFile 下载文件
func (m *MinIO) DownloadFile(ctx context.Context, objectName string) ([]byte, error) {
	// 创建一个名为 "MinIO.DownloadFile" 的 OpenTelemetry 追踪(span)
	ctx, span := minioTracer.Start(ctx, "MinIO.DownloadFile",
		trace.WithSpanKind(trace.SpanKindClient)) // 标记为客户端类型
	defer span.End() // 确保在函数退出时结束该 span

	m.logger.Printf("[MinIO] Downloading file: ObjectName=%s", objectName) // 记录下载日志
	bucketName := m.originalBucket                                         // 假设DownloadFile主要用于原始存储桶
	actualObjectName := objectName

	if strings.Contains(objectName, "/") { // 如果对象名包含斜杠，解析出桶名和对象键
		parts := strings.SplitN(objectName, "/", 2)
		if len(parts) == 2 {
			bucketName = parts[0]       // 第一部分是桶名
			actualObjectName = parts[1] // 第二部分是对象键
			m.logger.Printf("[MinIO] Using bucket '%s' and object key '%s' from provided objectName for download.", bucketName, actualObjectName)
		}
	}

	// 添加span属性
	span.SetAttributes(
		attribute.String("object.storage.operation", "get"),      // 操作类型：get
		attribute.String("object.storage.bucket", bucketName),    // 桶名称
		attribute.String("object.storage.key", actualObjectName), // 对象键
		attribute.String("net.peer.name", m.cfg.Endpoint),        // 网络对端名称
	)

	// 如果配置允许，并且提供了logger，则记录操作
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-DownloadFile] Downloading: ObjectName='%s', Bucket='%s'", actualObjectName, bucketName)
	}

	obj, err := m.client.GetObject(ctx, bucketName, actualObjectName, minio.GetObjectOptions{}) // 调用 MinIO 客户端获取对象
	if err != nil {                                                                             // 如果获取失败
		span.RecordError(err)                    // 在 span 中记录错误
		span.SetStatus(codes.Error, err.Error()) // 设置 span 状态为错误

		// 如果配置允许，并且提供了logger，则记录错误
		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-DownloadFile] Error getting object %s: %v", actualObjectName, err)
		}
		return nil, fmt.Errorf("获取对象 %s/%s 失败: %w", bucketName, actualObjectName, err) // 返回包装后的错误
	}
	defer obj.Close() // 确保在函数退出时关闭对象读取器

	// 检查对象状态，这对于了解对象是否存在或是否有权限访问很有用
	stat, err := obj.Stat() // 获取对象元数据
	if err != nil {         // 如果获取元数据失败
		span.RecordError(err)                    // 在 span 中记录错误
		span.SetStatus(codes.Error, err.Error()) // 设置 span 状态为错误

		m.logger.Printf("[MinIO] Failed to stat object %s/%s after GetObject: %v", bucketName, actualObjectName, err) // 记录错误日志
		return nil, fmt.Errorf("获取对象 %s/%s 状态失败: %w", bucketName, actualObjectName, err)                              // 返回包装后的错误
	}

	span.SetAttributes( // 添加从元数据中获取的属性
		attribute.Int64("object.storage.content_length", stat.Size),       // 内容长度
		attribute.String("object.storage.content_type", stat.ContentType), // 内容类型
		attribute.String("object.storage.etag", stat.ETag),                // ETag
	)

	m.logger.Printf("[MinIO] Object %s/%s stats: Size=%d, ContentType=%s", bucketName, actualObjectName, stat.Size, stat.ContentType) // 记录对象元数据日志

	data, err := io.ReadAll(obj) // 读取对象的所有内容到字节切片
	if err != nil {              // 如果读取失败
		span.RecordError(err)                    // 在 span 中记录错误
		span.SetStatus(codes.Error, err.Error()) // 设置 span 状态为错误

		// 如果配置允许，并且提供了logger，则记录错误
		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-DownloadFile] Error reading object data for %s: %v", actualObjectName, err)
		}
		return nil, fmt.Errorf("读取对象 %s/%s 数据失败: %w", bucketName, actualObjectName, err) // 返回包装后的错误
	}

	span.SetAttributes(attribute.Int("object.storage.downloaded_bytes", len(data))) // 添加已下载字节数的属性
	span.SetStatus(codes.Ok, "")                                                    // 设置 span 状态为成功

	// 如果配置允许，并且提供了logger，则记录成功信息
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-DownloadFile] Successfully downloaded %d bytes from %s/%s.", len(data), bucketName, actualObjectName)
	}
	return data, nil // 返回下载的数据和 nil 错误
}

// GetResumeFile 从MinIO获取简历文件
func (m *MinIO) GetResumeFile(ctx context.Context, objectKey string) ([]byte, error) {
	m.logger.Printf("[MinIO] Getting resume file: Bucket=%s, ObjectKey=%s", m.originalBucket, objectKey) // 记录日志
	// 如果配置允许，并且提供了logger，则记录操作
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-GetResumeFile] Getting: ObjectKey='%s', Bucket='%s'", objectKey, m.originalBucket)
	}
	return m.DownloadFile(ctx, fmt.Sprintf("%s/%s", m.originalBucket, objectKey)) // 调用通用的 DownloadFile 方法，并传入完整的桶/对象路径
}

// GetParsedText 从MinIO获取解析后的文本
func (m *MinIO) GetParsedText(ctx context.Context, objectKey string) (string, error) {
	m.logger.Printf("[MinIO] Getting parsed text: Bucket=%s, ObjectKey=%s", m.parsedBucket, objectKey) // 记录日志
	// 如果配置允许，并且提供了logger，则记录操作
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-GetParsedText] Getting: ObjectKey='%s', Bucket='%s'", objectKey, m.parsedBucket)
	}

	data, err := m.DownloadFile(ctx, fmt.Sprintf("%s/%s", m.parsedBucket, objectKey)) // 调用通用的 DownloadFile 方法
	if err != nil {                                                                   // 如果下载失败
		return "", err // DownloadFile 已经记录和格式化了错误，直接返回
	}
	text := string(data) // 将下载的字节切片转换为字符串
	// 如果配置允许，并且提供了logger，则记录成功信息
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-GetParsedText] Successfully downloaded parsed text %s, Size: %d bytes", objectKey, len(text))
	}
	return text, nil // 返回文本内容和 nil 错误
}

// GetPresignedURL 获取预签名URL
func (m *MinIO) GetPresignedURL(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	// 创建一个名为 "MinIO.GetPresignedURL" 的 OpenTelemetry 追踪(span)
	ctx, span := minioTracer.Start(ctx, "MinIO.GetPresignedURL",
		trace.WithSpanKind(trace.SpanKindClient)) // 标记为客户端类型
	defer span.End() // 确保在函数退出时结束该 span

	m.logger.Printf("[MinIO] Getting presigned URL: ObjectName=%s, Expiry=%v", objectName, expiry) // 记录日志
	bucketName := m.originalBucket                                                                 // 默认使用原始文件桶
	actualObjectName := objectName

	if strings.Contains(objectName, "/") { // 如果对象名包含斜杠，解析出桶名和对象键
		parts := strings.SplitN(objectName, "/", 2)
		if len(parts) == 2 {
			// 检查桶名是否为已知的允许桶
			if parts[0] == m.originalBucket || parts[0] == m.parsedBucket || parts[0] == m.cfg.BucketName {
				bucketName = parts[0]       // 使用指定的桶
				actualObjectName = parts[1] // 使用指定的对象键
				m.logger.Printf("[MinIO] Using bucket '%s' and object key '%s' from provided objectName for presigned URL.", bucketName, actualObjectName)
			} else {
				// 未知桶名直接返回错误，避免数据错位或越权
				return "", fmt.Errorf("未授权的桶前缀 '%s'，objectName '%s' 不允许访问", parts[0], objectName)
			}
		}
	}

	// 添加span属性
	span.SetAttributes(
		attribute.String("object.storage.operation", "presign"),                           // 操作类型：presign
		attribute.String("object.storage.bucket", bucketName),                             // 桶名称
		attribute.String("object.storage.key", actualObjectName),                          // 对象键
		attribute.String("net.peer.name", m.cfg.Endpoint),                                 // 网络对端名称
		attribute.Int64("object.storage.presign_expiry_seconds", int64(expiry.Seconds())), // 预签名URL的过期秒数
	)

	url, err := m.client.PresignedGetObject(ctx, bucketName, actualObjectName, expiry, nil) // 调用 MinIO 客户端生成预签名的 GET URL
	if err != nil {                                                                         // 如果生成失败
		span.RecordError(err)                    // 在 span 中记录错误
		span.SetStatus(codes.Error, err.Error()) // 设置 span 状态为错误

		m.logger.Printf("[MinIO] Error generating presigned URL for %s/%s: %v", bucketName, actualObjectName, err) // 记录错误日志
		return "", fmt.Errorf("为对象 %s/%s 生成预签名URL失败: %w", bucketName, actualObjectName, err)                       // 返回包装后的错误
	}

	span.SetAttributes(attribute.String("object.storage.presigned_url", url.String())) // 将生成的 URL 添加到 span 属性中
	span.SetStatus(codes.Ok, "")                                                       // 设置 span 状态为成功

	return url.String(), nil // 返回 URL 字符串和 nil 错误
}

// DeleteFile 删除文件
func (m *MinIO) DeleteFile(ctx context.Context, objectName string) error {
	// 创建一个名为 "MinIO.DeleteFile" 的 OpenTelemetry 追踪(span)
	ctx, span := minioTracer.Start(ctx, "MinIO.DeleteFile",
		trace.WithSpanKind(trace.SpanKindClient)) // 标记为客户端类型
	defer span.End() // 确保在函数退出时结束该 span

	m.logger.Printf("[MinIO] Deleting file: ObjectName=%s", objectName) // 记录日志
	bucketName := m.originalBucket                                      // 默认使用原始文件桶
	actualObjectName := objectName

	if strings.Contains(objectName, "/") { // 如果对象名包含斜杠，解析出桶名和对象键
		parts := strings.SplitN(objectName, "/", 2)
		if len(parts) == 2 {
			// 检查桶名是否为已知的允许桶
			if parts[0] == m.originalBucket || parts[0] == m.parsedBucket || parts[0] == m.cfg.BucketName {
				bucketName = parts[0]       // 使用指定的桶
				actualObjectName = parts[1] // 使用指定的对象键
				m.logger.Printf("[MinIO] Using bucket '%s' and object key '%s' from provided objectName for deletion.", bucketName, actualObjectName)
			} else {
				// 未知桶名直接返回错误，避免数据错位或越权
				return fmt.Errorf("未授权的桶前缀 '%s'，objectName '%s' 不允许访问", parts[0], objectName)
			}
		}
	}

	// 添加span属性
	span.SetAttributes(
		attribute.String("object.storage.operation", "delete"),   // 操作类型：delete
		attribute.String("object.storage.bucket", bucketName),    // 桶名称
		attribute.String("object.storage.key", actualObjectName), // 对象键
		attribute.String("net.peer.name", m.cfg.Endpoint),        // 网络对端名称
	)

	// 如果配置允许，并且提供了logger，则记录操作
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-DeleteFile] Deleting: ObjectName='%s', Bucket='%s'", actualObjectName, bucketName)
	}

	err := m.client.RemoveObject(ctx, bucketName, actualObjectName, minio.RemoveObjectOptions{}) // 调用 MinIO 客户端删除对象
	if err != nil {                                                                              // 如果删除失败
		span.RecordError(err)                    // 在 span 中记录错误
		span.SetStatus(codes.Error, err.Error()) // 设置 span 状态为错误

		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-DeleteFile] Error removing object %s from bucket %s: %v", actualObjectName, bucketName, err)
		}
		return fmt.Errorf("从MinIO删除对象 %s/%s 失败: %w", bucketName, actualObjectName, err) // 返回包装后的错误
	}

	span.SetStatus(codes.Ok, "") // 设置 span 状态为成功
	return nil                   // 返回 nil 表示成功
}

// StatObject 暴露底层的StatObject方法，用于测试或特定场景
func (m *MinIO) StatObject(ctx context.Context, bucketName, objectName string, opts minio.StatObjectOptions) (minio.ObjectInfo, error) {
	return m.client.StatObject(ctx, bucketName, objectName, opts) // 直接调用底层客户端的 StatObject 方法
}

// RemoveObject 暴露底层的RemoveObject方法，用于测试或特定场景
func (m *MinIO) RemoveObject(ctx context.Context, bucketName, objectName string, opts minio.RemoveObjectOptions) error {
	return m.client.RemoveObject(ctx, bucketName, objectName, opts) // 直接调用底层客户端的 RemoveObject 方法
}

// getContentType 获取内容类型
func getContentType(ext string) string {
	ext = strings.ToLower(ext) // 将扩展名转换为小写
	switch ext {               // 根据扩展名返回对应的 MIME 类型
	case ".pdf":
		return "application/pdf"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".txt":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	default: // 如果没有匹配的类型
		return "application/octet-stream" // 返回通用的二进制流类型
	}
}

// 使用统一的配置控制日志，移除环境变量检查

// UploadResumeFileStreaming 流式上传简历文件并同时计算MD5
// 返回: objectKey, md5Hex, error
func (m *MinIO) UploadResumeFileStreaming(ctx context.Context, submissionUUID, fileExt string, reader io.Reader, fileSize int64) (string, string, error) {
	// 创建一个名为 "MinIO.UploadResumeFileStreaming" 的 OpenTelemetry 追踪(span)
	ctx, span := minioTracer.Start(ctx, "MinIO.UploadResumeFileStreaming",
		trace.WithSpanKind(trace.SpanKindClient)) // 标记为客户端类型
	defer span.End() // 确保在函数退出时结束该 span

	// 检查 submissionUUID 是否包含可能的桶前缀，避免权限绕过
	if strings.Contains(submissionUUID, "/") {
		parts := strings.SplitN(submissionUUID, "/", 2)
		if len(parts) == 2 && (parts[0] == m.originalBucket || parts[0] == m.parsedBucket || parts[0] == m.cfg.BucketName) {
			span.RecordError(fmt.Errorf("疑似桶前缀绕过"))                                      // 记录错误
			span.SetStatus(codes.Error, "疑似桶前缀绕过")                                       // 设置错误状态
			return "", "", fmt.Errorf("submissionUUID '%s' 可能包含未授权的桶前缀", submissionUUID) // 返回错误
		}
	}

	// 构建对象名称，强制使用 m.originalBucket
	objectName := fmt.Sprintf("resume/%s/original%s", submissionUUID, fileExt)
	contentType := getContentType(fileExt) // 获取内容类型

	// 添加span属性
	span.SetAttributes(
		attribute.String("object.storage.operation", "put_streaming"), // 操作类型：流式上传
		attribute.String("object.storage.bucket", m.originalBucket),   // 桶名称
		attribute.String("object.storage.key", objectName),            // 对象键
		attribute.String("net.peer.name", m.cfg.Endpoint),             // 网络对端名称
		attribute.Int64("object.storage.content_length", fileSize),    // 内容长度
		attribute.String("object.storage.content_type", contentType),  // 内容类型
		attribute.String("resume.submission_uuid", submissionUUID),    // 简历提交的 UUID
		attribute.String("resume.file_ext", fileExt),                  // 简历文件扩展名
	)

	// 创建MD5哈希计算器
	md5Hash := md5.New()

	// 使用TeeReader同时将数据流写入哈希计算器和传递给下一个Reader
	// 这样可以在上传的同时计算MD5，而无需两次读取数据
	teeReader := io.TeeReader(reader, md5Hash)

	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadResumeFileStreaming] Uploading: SubmissionUUID='%s', FileExt='%s', ObjectName='%s', Bucket='%s'",
			submissionUUID, fileExt, objectName, m.originalBucket)
	}

	// 将文件流式上传到MinIO
	info, err := m.client.PutObject(ctx, m.originalBucket, objectName, teeReader,
		fileSize, minio.PutObjectOptions{ContentType: contentType})
	if err != nil { // 如果上传失败
		span.RecordError(err)                                // 在 span 中记录错误
		span.SetStatus(codes.Error, err.Error())             // 设置 span 状态为错误
		return "", "", fmt.Errorf("流式上传文件到MinIO失败: %w", err) // 返回包装后的错误
	}

	// 计算MD5哈希值并编码为十六进制字符串
	md5Hex := hex.EncodeToString(md5Hash.Sum(nil))

	// 添加上传结果属性
	span.SetAttributes(
		attribute.String("object.storage.etag", info.ETag), // 上传对象的 ETag
		attribute.Int64("object.storage.size", info.Size),  // 上传对象的实际大小
		attribute.String("resume.file_md5", md5Hex),        // 文件的 MD5 哈希值
	)
	span.SetStatus(codes.Ok, "") // 设置 span 状态为成功

	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadResumeFileStreaming] Successfully uploaded %s, ETag: %s, Size: %d, MD5: %s",
			objectName, info.ETag, info.Size, md5Hex)
	}

	return objectName, md5Hex, nil // 返回对象键、MD5哈希值和 nil 错误
}
