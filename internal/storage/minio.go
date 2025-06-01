package storage

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/logger"
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
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
}

// 确保MinIO实现了ObjectStorage接口
var _ ObjectStorage = (*MinIO)(nil)

// MinIO 提供对象存储功能
type MinIO struct {
	client         *minio.Client
	cfg            *config.MinIOConfig
	originalBucket string
	parsedBucket   string
}

// NewMinIO 创建MinIO客户端
func NewMinIO(cfg *config.MinIOConfig) (*MinIO, error) {
	if cfg == nil {
		return nil, fmt.Errorf("MinIO配置不能为空")
	}

	// 创建MinIO客户端
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
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
	}

	// 确保存储桶存在
	err = m.ensureBucketExists(originalBucket, cfg.Location)
	if err != nil {
		return nil, fmt.Errorf("确保原始简历存储桶存在失败: %w", err)
	}

	err = m.ensureBucketExists(parsedBucket, cfg.Location)
	if err != nil {
		return nil, fmt.Errorf("确保解析文本存储桶存在失败: %w", err)
	}

	// 设置生命周期规则
	if cfg.OriginalFileExpireDays > 0 || cfg.ParsedTextExpireDays > 0 {
		err = m.setupLifecycleRules(context.Background())
		if err != nil {
			logger.Warn().Err(err).Msg("设置对象生命周期规则失败")
			// 继续执行，不中断启动
		}
	}

	return m, nil
}

// ensureBucketExists 确保存储桶存在
func (m *MinIO) ensureBucketExists(bucketName, location string) error {
	ctx := context.Background()

	// 检查存储桶是否存在
	exists, err := m.client.BucketExists(ctx, bucketName)
	if err != nil {
		return fmt.Errorf("检查存储桶是否存在失败: %w", err)
	}

	// 如果存储桶不存在，则创建
	if !exists {
		err = m.client.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{
			Region: location,
		})
		if err != nil {
			return fmt.Errorf("创建存储桶失败: %w", err)
		}
		logger.Info().Str("bucket", bucketName).Msg("成功创建存储桶")
	} else {
		logger.Info().Str("bucket", bucketName).Msg("存储桶已存在")
	}

	return nil
}

// setupLifecycleRules 设置对象生命周期规则
func (m *MinIO) setupLifecycleRules(ctx context.Context) error {
	// 为原始简历存储桶设置生命周期规则
	if m.cfg.OriginalFileExpireDays > 0 {
		if err := m.setupBucketLifecycle(ctx, m.originalBucket, "original-files-expiry", m.cfg.OriginalFileExpireDays); err != nil {
			return fmt.Errorf("设置原始简历存储桶生命周期规则失败: %w", err)
		}
	}

	// 为解析文本存储桶设置生命周期规则
	if m.cfg.ParsedTextExpireDays > 0 {
		if err := m.setupBucketLifecycle(ctx, m.parsedBucket, "parsed-text-expiry", m.cfg.ParsedTextExpireDays); err != nil {
			return fmt.Errorf("设置解析文本存储桶生命周期规则失败: %w", err)
		}
	}

	return nil
}

// setupBucketLifecycle 为指定存储桶设置生命周期规则
func (m *MinIO) setupBucketLifecycle(ctx context.Context, bucketName, ruleID string, expiryDays int) error {
	config := lifecycle.NewConfiguration()

	// 创建生命周期规则
	rule := lifecycle.Rule{
		ID:     ruleID,
		Status: "Enabled",
		Expiration: lifecycle.Expiration{
			Days: lifecycle.ExpirationDays(expiryDays),
		},
	}

	config.Rules = append(config.Rules, rule)

	// 应用生命周期规则
	return m.client.SetBucketLifecycle(ctx, bucketName, config)
}

// UploadFile 上传文件到指定路径
func (m *MinIO) UploadFile(ctx context.Context, objectName string, reader io.Reader, fileSize int64, contentType string) (string, error) {
	// 确保桶存在 (防御性编程)
	if err := m.ensureBucketExists(m.originalBucket, m.cfg.Location); err != nil {
		return "", err
	}

	// 如果没有提供内容类型，尝试根据扩展名猜测
	if contentType == "" {
		ext := strings.ToLower(filepath.Ext(objectName))
		switch ext {
		case ".pdf":
			contentType = "application/pdf"
		case ".txt":
			contentType = "text/plain"
		case ".json":
			contentType = "application/json"
		case ".doc":
			contentType = "application/msword"
		case ".docx":
			contentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		default:
			contentType = "application/octet-stream"
		}
	}

	// 上传文件
	_, err := m.client.PutObject(ctx, m.originalBucket, objectName, reader, fileSize,
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", fmt.Errorf("上传文件到 %s/%s 失败: %w", m.originalBucket, objectName, err)
	}

	logger.Info().
		Str("bucket", m.originalBucket).
		Str("object", objectName).
		Msg("文件上传成功")
	return objectName, nil // 返回的是包含路径的完整对象键
}

// UploadFileFromBytes 从字节数组上传文件 (私有辅助方法)
func (m *MinIO) uploadFileFromBytes(ctx context.Context, objectName string, data []byte, contentType string) (string, error) {
	reader := bytes.NewReader(data)
	return m.UploadFile(ctx, objectName, reader, int64(len(data)), contentType)
}

// UploadResumeFile 上传简历文件到MinIO
func (m *MinIO) UploadResumeFile(ctx context.Context, uuid, ext string, reader io.Reader, size int64) (string, error) {
	// 生成对象键
	objectKey := fmt.Sprintf("resume/%s/original%s", uuid, ext)

	// 上传文件
	_, err := m.client.PutObject(ctx, m.originalBucket, objectKey, reader, size, minio.PutObjectOptions{
		ContentType: getContentType(ext),
	})
	if err != nil {
		return "", fmt.Errorf("上传文件失败: %w", err)
	}

	return objectKey, nil
}

// UploadParsedText 上传解析后的文本到MinIO
func (m *MinIO) UploadParsedText(ctx context.Context, uuid string, text string) (string, error) {
	// 生成对象键
	objectKey := fmt.Sprintf("resume/%s/parsed.txt", uuid)

	// 上传文本
	_, err := m.client.PutObject(
		ctx,
		m.parsedBucket,
		objectKey,
		strings.NewReader(text),
		int64(len(text)),
		minio.PutObjectOptions{
			ContentType: "text/plain",
		},
	)
	if err != nil {
		return "", fmt.Errorf("上传解析文本失败: %w", err)
	}

	return objectKey, nil
}

// DownloadFile 下载文件
func (m *MinIO) DownloadFile(ctx context.Context, objectName string) ([]byte, error) {
	// 获取文件对象
	object, err := m.client.GetObject(ctx, m.originalBucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("获取文件 %s/%s 失败: %w", m.originalBucket, objectName, err)
	}
	defer object.Close()

	// 读取文件内容
	data, err := io.ReadAll(object)
	if err != nil {
		return nil, fmt.Errorf("读取文件 %s/%s 内容失败: %w", m.originalBucket, objectName, err)
	}

	return data, nil
}

// GetResumeFile 从MinIO获取简历文件
func (m *MinIO) GetResumeFile(ctx context.Context, objectKey string) ([]byte, error) {
	// 获取对象
	obj, err := m.client.GetObject(ctx, m.originalBucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("获取对象失败: %w", err)
	}
	defer obj.Close()

	// 读取对象内容
	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, obj)
	if err != nil {
		return nil, fmt.Errorf("读取对象内容失败: %w", err)
	}

	return buf.Bytes(), nil
}

// GetParsedText 从MinIO获取解析后的文本
func (m *MinIO) GetParsedText(ctx context.Context, objectKey string) (string, error) {
	// 获取对象
	obj, err := m.client.GetObject(ctx, m.parsedBucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return "", fmt.Errorf("获取解析文本对象失败: %w", err)
	}
	defer obj.Close()

	// 读取对象内容
	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, obj)
	if err != nil {
		return "", fmt.Errorf("读取解析文本内容失败: %w", err)
	}

	return buf.String(), nil
}

// GetPresignedURL 获取预签名URL
func (m *MinIO) GetPresignedURL(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	// 生成预签名URL
	url, err := m.client.PresignedGetObject(ctx, m.originalBucket, objectName, expiry, nil)
	if err != nil {
		return "", fmt.Errorf("生成预签名URL %s/%s 失败: %w", m.originalBucket, objectName, err)
	}
	return url.String(), nil
}

// DeleteFile 删除文件
func (m *MinIO) DeleteFile(ctx context.Context, objectName string) error {
	return m.client.RemoveObject(ctx, m.originalBucket, objectName, minio.RemoveObjectOptions{})
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
	default:
		return "application/octet-stream"
	}
}
