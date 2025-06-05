package storage

import (
	"ai-agent-go/internal/config"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
)

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

	// ResumeSpecific 简历特定操作
	UploadResumeFile(ctx context.Context, submissionUUID, fileExt string, reader io.Reader, fileSize int64) (string, error)
	UploadParsedText(ctx context.Context, submissionUUID string, text string) (string, error)
	GetResumeFile(ctx context.Context, objectName string) ([]byte, error)
	GetParsedText(ctx context.Context, objectName string) (string, error)

	// 新增: 流式上传并计算MD5
	UploadResumeFileStreaming(ctx context.Context, submissionUUID, fileExt string, reader io.Reader, fileSize int64) (string, string, error)
}

// 确保MinIO实现了ObjectStorage接口
var _ ObjectStorage = (*MinIO)(nil)

// MinIO 提供对象存储功能
type MinIO struct {
	client         *minio.Client
	cfg            *config.MinIOConfig
	originalBucket string
	parsedBucket   string
	logger         *log.Logger
}

// NewMinIO 创建MinIO客户端
func NewMinIO(cfg *config.MinIOConfig, logger *log.Logger) (*MinIO, error) {
	if cfg == nil {
		return nil, fmt.Errorf("MinIO配置不能为空")
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	logger.Printf("[MinIO] Initializing MinIO client with endpoint: %s, originalBucket: %s, parsedBucket: %s", cfg.Endpoint, cfg.OriginalsBucket, cfg.ParsedTextBucket)

	// 创建MinIO客户端
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		logger.Printf("[MinIO] Initialization failed: %v", err)
		return nil, fmt.Errorf("创建MinIO客户端失败: %w", err)
	}

	// 设置存储桶名称
	originalBucket := cfg.OriginalsBucket
	if originalBucket == "" {
		originalBucket = cfg.BucketName // 向后兼容
	}

	parsedBucket := cfg.ParsedTextBucket
	if parsedBucket == "" {
		parsedBucket = "parsed-text" // 默认值
	}

	m := &MinIO{
		client:         client,
		cfg:            cfg,
		originalBucket: originalBucket,
		parsedBucket:   parsedBucket,
		logger:         logger,
	}

	// 确保存储桶存在
	err = m.ensureBucketExists(originalBucket, cfg.Location)
	if err != nil {
		logger.Printf("[MinIO] Failed to ensure original bucket %s exists: %v", originalBucket, err)
		return nil, fmt.Errorf("确保原始简历存储桶 %s 存在失败: %w", originalBucket, err)
	}

	err = m.ensureBucketExists(parsedBucket, cfg.Location)
	if err != nil {
		logger.Printf("[MinIO] Failed to ensure parsed bucket %s exists: %v", parsedBucket, err)
		return nil, fmt.Errorf("确保解析文本存储桶 %s 存在失败: %w", parsedBucket, err)
	}

	// 设置生命周期规则
	if cfg.OriginalFileExpireDays > 0 || cfg.ParsedTextExpireDays > 0 {
		err = m.setupLifecycleRules(context.Background())
		if err != nil {
			logger.Printf("[MinIO] Warning: Failed to set up lifecycle rules: %v", err)
		}
	}

	logger.Printf("[MinIO] Client initialized successfully for endpoint: %s", cfg.Endpoint)
	return m, nil
}

// ensureBucketExists 确保存储桶存在
func (m *MinIO) ensureBucketExists(bucketName, location string) error {
	m.logger.Printf("[MinIO] Ensuring bucket exists: %s (Location: %s)", bucketName, location)
	exists, err := m.client.BucketExists(context.Background(), bucketName)
	if err != nil {
		m.logger.Printf("[MinIO] Error checking if bucket %s exists: %v", bucketName, err)
		return fmt.Errorf("检查存储桶 %s 是否存在时出错: %w", bucketName, err)
	}
	if !exists {
		m.logger.Printf("[MinIO] Bucket %s does not exist, attempting to create...", bucketName)
		err = m.client.MakeBucket(context.Background(), bucketName, minio.MakeBucketOptions{Region: location})
		if err != nil {
			m.logger.Printf("[MinIO] Error creating bucket %s: %v", bucketName, err)
			return fmt.Errorf("创建存储桶 %s 失败: %w", bucketName, err)
		}
		m.logger.Printf("[MinIO] Bucket %s created successfully.", bucketName)
	} else {
		m.logger.Printf("[MinIO] Bucket %s already exists.", bucketName)
	}
	return nil
}

// setupLifecycleRules 设置对象生命周期规则
func (m *MinIO) setupLifecycleRules(ctx context.Context) error {
	m.logger.Printf("[MinIO] Setting up lifecycle rules...")
	if m.cfg.OriginalFileExpireDays > 0 {
		if err := m.setupBucketLifecycle(ctx, m.originalBucket, "expire-originals", m.cfg.OriginalFileExpireDays); err != nil {
			return fmt.Errorf("为原始文件存储桶 %s 设置生命周期失败: %w", m.originalBucket, err)
		}
	}
	if m.cfg.ParsedTextExpireDays > 0 {
		if err := m.setupBucketLifecycle(ctx, m.parsedBucket, "expire-parsed-text", m.cfg.ParsedTextExpireDays); err != nil {
			return fmt.Errorf("为解析文本存储桶 %s 设置生命周期失败: %w", m.parsedBucket, err)
		}
	}
	m.logger.Printf("[MinIO] Lifecycle rules setup completed.")
	return nil
}

// setupBucketLifecycle 为指定存储桶设置生命周期规则
func (m *MinIO) setupBucketLifecycle(ctx context.Context, bucketName, ruleID string, expiryDays int) error {
	m.logger.Printf("[MinIO] Setting lifecycle rule for bucket %s: ID=%s, ExpiryDays=%d", bucketName, ruleID, expiryDays)
	config := lifecycle.NewConfiguration()
	config.Rules = []lifecycle.Rule{
		{
			ID:     ruleID,
			Status: "Enabled",
			Expiration: lifecycle.Expiration{
				Days: lifecycle.ExpirationDays(expiryDays),
			},
		},
	}

	err := m.client.SetBucketLifecycle(ctx, bucketName, config)
	if err != nil {
		m.logger.Printf("[MinIO] Error setting lifecycle for bucket %s: %v", bucketName, err)
		return err
	}
	m.logger.Printf("[MinIO] Successfully set lifecycle for bucket %s.", bucketName)
	return nil
}

// UploadFile 上传文件到指定路径
func (m *MinIO) UploadFile(ctx context.Context, objectName string, reader io.Reader, fileSize int64, contentType string) (string, error) {
	m.logger.Printf("[MinIO] Uploading file: ObjectName=%s, Size=%d, ContentType=%s", objectName, fileSize, contentType)

	// 默认使用配置中的主 bucketName，如果 objectName 包含 bucket 信息，则优先使用 objectName 中的
	bucketToUse := m.cfg.BucketName
	actualObjectName := objectName
	if strings.Contains(objectName, "/") {
		parts := strings.SplitN(objectName, "/", 2)
		if len(parts) == 2 {
			// Check if parts[0] is one of the known buckets to avoid accidental bucket creation via objectName
			if parts[0] == m.originalBucket || parts[0] == m.parsedBucket || parts[0] == m.cfg.BucketName {
				bucketToUse = parts[0]
				actualObjectName = parts[1]
				m.logger.Printf("[MinIO] Using bucket '%s' and object key '%s' from provided objectName.", bucketToUse, actualObjectName)
			} else {
				m.logger.Printf("[MinIO] Warning: ObjectName '%s' contains a potential bucket name '%s' that is not a configured primary/originals/parsed bucket. Defaulting to bucket '%s'.", objectName, parts[0], bucketToUse)
			}
		}
	}

	// 如果配置允许，并且提供了logger，则记录输入
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadFile] Attempting to upload: ObjectName='%s', FileSize=%d, ContentType='%s', Bucket='%s'", actualObjectName, fileSize, contentType, bucketToUse)
	}

	uploadInfo, err := m.client.PutObject(ctx, bucketToUse, actualObjectName, reader, fileSize, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		// 如果配置允许，并且提供了logger，则记录错误
		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-UploadFile] Error uploading %s: %v", actualObjectName, err)
		}
		return "", fmt.Errorf("上传对象 %s/%s 失败: %w", bucketToUse, actualObjectName, err)
	}

	// 如果配置允许，并且提供了logger，则记录成功信息
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadFile] Successfully uploaded %s, ETag: %s, Size: %d, Path: %s", actualObjectName, uploadInfo.ETag, uploadInfo.Size, actualObjectName)
	}
	return actualObjectName, nil
}

// UploadFileFromBytes 从字节数组上传文件 (私有辅助方法)
func (m *MinIO) uploadFileFromBytes(ctx context.Context, objectName string, data []byte, contentType string) (string, error) {
	return m.UploadFile(ctx, objectName, bytes.NewReader(data), int64(len(data)), contentType)
}

// UploadResumeFile 上传原始简历文件到MinIO的originalsBucket
// 返回MinIO中的对象键 (不含bucket前缀)
func (m *MinIO) UploadResumeFile(ctx context.Context, submissionUUID, fileExt string, reader io.Reader, fileSize int64) (string, error) {
	// 构建对象名称，例如: resume/submissionUUID/original.pdf
	objectName := fmt.Sprintf("resume/%s/original%s", submissionUUID, fileExt)
	contentType := getContentType(fileExt)

	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadResumeFile] Uploading: SubmissionUUID='%s', FileExt='%s', ObjectName='%s', Bucket='%s'", submissionUUID, fileExt, objectName, m.originalBucket)
	}

	// 调用通用的 UploadFile 方法，它会将文件上传到 m.originalBucket
	// UploadFile 方法已被修正为仅返回对象键 (objectName)
	uploadedObjectName, err := m.UploadFile(ctx, objectName, reader, fileSize, contentType)
	if err != nil {
		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-UploadResumeFile] Error during UploadFile call: %v", err)
		}
		return "", err // Propagate error
	}

	// 确认 UploadFile 返回的是预期的 objectName (在启用了测试日志时)
	// 正常情况下，uploadedObjectName 应该等于 objectName
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard && uploadedObjectName != objectName {
		m.logger.Printf("[MinIO-UploadResumeFile] Warning: UploadFile returned '%s' but expected '%s'", uploadedObjectName, objectName)
	}

	// Log success
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadResumeFile] Successfully processed and initiated upload for %s to bucket %s", objectName, m.originalBucket)
	}

	return objectName, nil // 确保返回的是最初构建的 objectName
}

// UploadParsedText 上传解析后的文本到MinIO
func (m *MinIO) UploadParsedText(ctx context.Context, submissionUUID string, text string) (string, error) {
	objectName := fmt.Sprintf("resume/%s/parsed_text.txt", submissionUUID)

	// 如果配置允许，并且提供了logger，则记录输入
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadParsedText] Uploading: SubmissionUUID='%s', ObjectName='%s', Bucket='%s', TextLength=%d", submissionUUID, objectName, m.parsedBucket, len(text))
	}

	_, err := m.client.PutObject(ctx, m.parsedBucket, objectName, strings.NewReader(text), int64(len(text)), minio.PutObjectOptions{ContentType: "text/plain"})
	if err != nil {
		// 如果配置允许，并且提供了logger，则记录错误
		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-UploadParsedText] Error uploading parsed text for %s: %v", submissionUUID, err)
		}
		return "", fmt.Errorf("上传解析文本 %s 到存储桶 %s 失败: %w", objectName, m.parsedBucket, err)
	}
	// 如果配置允许，并且提供了logger，则记录成功信息
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-UploadParsedText] Successfully uploaded parsed text for %s to %s", submissionUUID, objectName)
	}
	return objectName, nil
}

// DownloadFile 下载文件
func (m *MinIO) DownloadFile(ctx context.Context, objectName string) ([]byte, error) {
	m.logger.Printf("[MinIO] Downloading file: ObjectName=%s", objectName)
	bucketName := m.originalBucket // 假设DownloadFile主要用于原始存储桶
	actualObjectName := objectName

	if strings.Contains(objectName, "/") {
		parts := strings.SplitN(objectName, "/", 2)
		if len(parts) == 2 {
			bucketName = parts[0]
			actualObjectName = parts[1]
			m.logger.Printf("[MinIO] Using bucket '%s' and object key '%s' from provided objectName for download.", bucketName, actualObjectName)
		}
	}

	// 如果配置允许，并且提供了logger，则记录操作
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-DownloadFile] Downloading: ObjectName='%s', Bucket='%s'", actualObjectName, bucketName)
	}

	obj, err := m.client.GetObject(ctx, bucketName, actualObjectName, minio.GetObjectOptions{})
	if err != nil {
		// 如果配置允许，并且提供了logger，则记录错误
		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-DownloadFile] Error getting object %s: %v", actualObjectName, err)
		}
		return nil, fmt.Errorf("获取对象 %s/%s 失败: %w", bucketName, actualObjectName, err)
	}
	defer obj.Close()

	// 检查对象状态，这对于了解对象是否存在或是否有权限访问很有用
	stat, err := obj.Stat()
	if err != nil {
		m.logger.Printf("[MinIO] Failed to stat object %s/%s after GetObject: %v", bucketName, actualObjectName, err)
		return nil, fmt.Errorf("获取对象 %s/%s 状态失败: %w", bucketName, actualObjectName, err)
	}
	m.logger.Printf("[MinIO] Object %s/%s stats: Size=%d, ContentType=%s", bucketName, actualObjectName, stat.Size, stat.ContentType)

	data, err := io.ReadAll(obj)
	if err != nil {
		// 如果配置允许，并且提供了logger，则记录错误
		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-DownloadFile] Error reading object data for %s: %v", actualObjectName, err)
		}
		return nil, fmt.Errorf("读取对象 %s/%s 数据失败: %w", bucketName, actualObjectName, err)
	}
	// 如果配置允许，并且提供了logger，则记录成功信息
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-DownloadFile] Successfully downloaded %d bytes from %s/%s.", len(data), bucketName, actualObjectName)
	}
	return data, nil
}

// GetResumeFile 从MinIO获取简历文件
func (m *MinIO) GetResumeFile(ctx context.Context, objectKey string) ([]byte, error) {
	m.logger.Printf("[MinIO] Getting resume file: Bucket=%s, ObjectKey=%s", m.originalBucket, objectKey)
	// 如果配置允许，并且提供了logger，则记录操作
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-GetResumeFile] Getting: ObjectKey='%s', Bucket='%s'", objectKey, m.originalBucket)
	}
	return m.DownloadFile(ctx, fmt.Sprintf("%s/%s", m.originalBucket, objectKey))
}

// GetParsedText 从MinIO获取解析后的文本
func (m *MinIO) GetParsedText(ctx context.Context, objectKey string) (string, error) {
	m.logger.Printf("[MinIO] Getting parsed text: Bucket=%s, ObjectKey=%s", m.parsedBucket, objectKey)
	// 如果配置允许，并且提供了logger，则记录操作
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-GetParsedText] Getting: ObjectKey='%s', Bucket='%s'", objectKey, m.parsedBucket)
	}

	data, err := m.DownloadFile(ctx, fmt.Sprintf("%s/%s", m.parsedBucket, objectKey))
	if err != nil {
		return "", err // DownloadFile already logs and formats the error
	}
	text := string(data)
	// 如果配置允许，并且提供了logger，则记录成功信息
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-GetParsedText] Successfully downloaded parsed text %s, Size: %d bytes", objectKey, len(text))
	}
	return text, nil
}

// GetPresignedURL 获取预签名URL
func (m *MinIO) GetPresignedURL(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	m.logger.Printf("[MinIO] Generating presigned URL for: %s, Expiry: %s", objectName, expiry)
	// Implement logic to split bucket and key from objectName if needed
	bucketName := m.originalBucket // 假设GetPresignedURL主要用于原始存储桶

	// 如果配置允许，并且提供了logger，则记录操作
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-GetPresignedURL] Generating for: ObjectName='%s', Bucket='%s', Expiry=%s", objectName, bucketName, expiry)
	}

	presignedURL, err := m.client.PresignedGetObject(ctx, bucketName, objectName, expiry, nil)
	if err != nil {
		// 如果配置允许，并且提供了logger，则记录错误
		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-GetPresignedURL] Error generating for %s: %v", objectName, err)
		}
		return "", fmt.Errorf("生成MinIO预签名URL失败: %w", err)
	}
	// 如果配置允许，并且提供了logger，则记录成功信息
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-GetPresignedURL] Successfully generated for %s: %s", objectName, presignedURL.String())
	}
	return presignedURL.String(), nil
}

// DeleteFile 删除文件
func (m *MinIO) DeleteFile(ctx context.Context, objectName string) error {
	m.logger.Printf("[MinIO] Deleting object: %s", objectName)
	bucketName := m.originalBucket // 假设DeleteFile主要用于原始存储桶

	// 如果配置允许，并且提供了logger，则记录操作
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-DeleteFile] Deleting: ObjectName='%s', Bucket='%s'", objectName, bucketName)
	}

	err := m.client.RemoveObject(ctx, bucketName, objectName, minio.RemoveObjectOptions{})
	if err != nil {
		// 如果配置允许，并且提供了logger，则记录错误
		if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
			m.logger.Printf("[MinIO-DeleteFile] Error deleting %s: %v", objectName, err)
		}
		return fmt.Errorf("删除对象 %s 失败: %w", objectName, err)
	}
	// 如果配置允许，并且提供了logger，则记录成功信息
	if m.cfg.EnableTestLogging && m.logger != nil && m.logger.Writer() != io.Discard {
		m.logger.Printf("[MinIO-DeleteFile] Successfully deleted %s", objectName)
	}
	return nil
}

// StatObject 暴露底层的StatObject方法，用于测试或特定场景
func (m *MinIO) StatObject(ctx context.Context, bucketName, objectName string, opts minio.StatObjectOptions) (minio.ObjectInfo, error) {
	return m.client.StatObject(ctx, bucketName, objectName, opts)
}

// RemoveObject 暴露底层的RemoveObject方法，用于测试或特定场景
func (m *MinIO) RemoveObject(ctx context.Context, bucketName, objectName string, opts minio.RemoveObjectOptions) error {
	return m.client.RemoveObject(ctx, bucketName, objectName, opts)
}

// 获取内容类型
func getContentType(ext string) string {
	ext = strings.ToLower(ext)
	switch ext {
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
	default:
		return "application/octet-stream"
	}
}

// Temporary helper for conditional test logging in MinIO methods
func minioTestLoggingEnabled() bool {
	return os.Getenv("AI_AGENT_MINIO_TEST_LOGGING") == "true"
}

// UploadResumeFileStreaming 流式上传简历文件并同时计算MD5
// 返回: objectKey, md5Hex, error
func (m *MinIO) UploadResumeFileStreaming(ctx context.Context, submissionUUID, fileExt string, reader io.Reader, fileSize int64) (string, string, error) {
	objectName := fmt.Sprintf("resume/%s/original%s", submissionUUID, fileExt)
	contentType := getContentType(fileExt)

	// 创建MD5哈希计算器
	md5Hash := md5.New()

	// 使用TeeReader同时读取到哈希计算器
	teeReader := io.TeeReader(reader, md5Hash)

	if minioTestLoggingEnabled() {
		m.logger.Printf("[MinIO-UploadResumeFileStreaming] Uploading: SubmissionUUID='%s', FileExt='%s', ObjectName='%s', Bucket='%s'",
			submissionUUID, fileExt, objectName, m.originalBucket)
	}

	// 将文件流式上传到MinIO
	info, err := m.client.PutObject(ctx, m.originalBucket, objectName, teeReader,
		fileSize, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", "", fmt.Errorf("流式上传文件到MinIO失败: %w", err)
	}

	// 计算MD5哈希值
	md5Hex := hex.EncodeToString(md5Hash.Sum(nil))

	if minioTestLoggingEnabled() {
		m.logger.Printf("[MinIO-UploadResumeFileStreaming] Successfully uploaded %s, ETag: %s, Size: %d, MD5: %s",
			objectName, info.ETag, info.Size, md5Hex)
	}

	return objectName, md5Hex, nil
}
