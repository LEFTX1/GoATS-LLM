package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"path/filepath"
	"strings"
	"time"

	"ai-agent-go/pkg/config"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIOAdapter 提供MinIO对象存储的封装
type MinIOAdapter struct {
	client       *minio.Client
	cfg          *config.MinIOConfig
	bucketExists map[string]bool // 缓存已确认存在的桶
}

// NewMinIOAdapter 创建MinIO适配器
func NewMinIOAdapter(cfg *config.MinIOConfig) (*MinIOAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("MinIO配置不能为空")
	}

	// 初始化MinIO客户端
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("初始化MinIO客户端失败: %w", err)
	}

	adapter := &MinIOAdapter{
		client:       client,
		cfg:          cfg,
		bucketExists: make(map[string]bool),
	}

	// 确保配置中定义的单个主存储桶存在
	if err := adapter.ensureBucketExists(context.Background(), cfg.BucketName); err != nil {
		return nil, fmt.Errorf("确保存储桶 '%s' 存在失败: %w", cfg.BucketName, err)
	}

	log.Printf("成功连接到MinIO服务器: %s，并确保桶 '%s' 存在", cfg.Endpoint, cfg.BucketName)
	return adapter, nil
}

// ensureBucketExists 确保桶存在
func (a *MinIOAdapter) ensureBucketExists(ctx context.Context, bucketName string) error {
	if bucketName == "" {
		return fmt.Errorf("存储桶名称不能为空")
	}
	if exists, ok := a.bucketExists[bucketName]; ok && exists {
		return nil // 已确认桶存在
	}

	// 检查桶是否存在
	exists, err := a.client.BucketExists(ctx, bucketName)
	if err != nil {
		return fmt.Errorf("检查桶 %s 是否存在时出错: %w", bucketName, err)
	}

	// 如果桶不存在，创建它
	if !exists {
		loc := a.cfg.Location // 使用配置中的Location
		if loc == "" {
			// MinIO的MakeBucket对于本地部署通常不需要指定Region，但为了通用性，可以留空或设为默认
			// 对于某些云提供商的S3兼容服务，可能需要指定Region
			log.Printf("未指定MinIO存储桶区域，将使用服务默认区域创建桶 '%s'", bucketName)
		}
		if err = a.client.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: loc}); err != nil {
			return fmt.Errorf("创建桶 %s (区域: '%s') 失败: %w", bucketName, loc, err)
		}
		log.Printf("已创建桶: %s (区域: '%s')", bucketName, loc)
	}

	a.bucketExists[bucketName] = true
	return nil
}

// UploadFile 上传文件到MinIO的指定路径 (objectName 包含路径前缀)
func (a *MinIOAdapter) UploadFile(ctx context.Context, objectName string, reader io.Reader, fileSize int64, contentType string) (string, error) {
	bucketName := a.cfg.BucketName
	// 确保桶存在 (虽然在New时已检查，但作为防御性编程)
	if err := a.ensureBucketExists(ctx, bucketName); err != nil {
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
	_, err := a.client.PutObject(ctx, bucketName, objectName, reader, fileSize,
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", fmt.Errorf("上传文件到 %s/%s 失败: %w", bucketName, objectName, err)
	}

	log.Printf("文件 %s 已成功上传到桶 %s", objectName, bucketName)
	return objectName, nil // 返回的是包含路径的完整对象键
}

// UploadFileFromBytes 从字节数组上传文件 (objectName 包含路径前缀)
func (a *MinIOAdapter) UploadFileFromBytes(ctx context.Context, objectName string, data []byte, contentType string) (string, error) {
	reader := bytes.NewReader(data)
	return a.UploadFile(ctx, objectName, reader, int64(len(data)), contentType)
}

// UploadResumeFile 上传简历文件到原始桶下的 originals/ 目录
func (a *MinIOAdapter) UploadResumeFile(ctx context.Context, submissionUUID, fileExt string, reader io.Reader, fileSize int64) (string, error) {
	// 构建对象名称，包含路径前缀
	objectName := fmt.Sprintf("originals/%s%s", submissionUUID, fileExt)

	// 根据扩展名确定内容类型 (UploadFile内部也会尝试确定，这里可以更精确)
	contentType := ""
	switch strings.ToLower(fileExt) {
	case ".pdf":
		contentType = "application/pdf"
	case ".doc":
		contentType = "application/msword"
	case ".docx":
		contentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		// 可以添加更多类型
	}

	return a.UploadFile(ctx, objectName, reader, fileSize, contentType)
}

// UploadParsedText 上传解析后的简历文本到 parsed_text/ 目录
func (a *MinIOAdapter) UploadParsedText(ctx context.Context, submissionUUID string, text string) (string, error) {
	// 构建对象名称，包含路径前缀
	objectName := fmt.Sprintf("parsed_text/%s.txt", submissionUUID)

	// 上传文本内容
	return a.UploadFileFromBytes(ctx, objectName, []byte(text), "text/plain")
}

// DownloadFile 从MinIO下载文件 (objectName 包含路径前缀)
func (a *MinIOAdapter) DownloadFile(ctx context.Context, objectName string) ([]byte, error) {
	bucketName := a.cfg.BucketName
	// 获取文件对象
	object, err := a.client.GetObject(ctx, bucketName, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("获取文件 %s/%s 失败: %w", bucketName, objectName, err)
	}
	defer object.Close()

	// 读取文件内容
	data, err := ioutil.ReadAll(object)
	if err != nil {
		return nil, fmt.Errorf("读取文件 %s/%s 内容失败: %w", bucketName, objectName, err)
	}

	return data, nil
}

// GetResumeFile 获取原始简历文件 (objectName 即为 originals/{uuid}.ext)
func (a *MinIOAdapter) GetResumeFile(ctx context.Context, objectName string) ([]byte, error) {
	return a.DownloadFile(ctx, objectName)
}

// GetParsedText 获取解析后的简历文本 (objectName 即为 parsed_text/{uuid}.txt)
func (a *MinIOAdapter) GetParsedText(ctx context.Context, objectName string) (string, error) {
	data, err := a.DownloadFile(ctx, objectName)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// GetPresignedURL 获取预签名URL用于直接访问或下载 (objectName 包含路径前缀)
func (a *MinIOAdapter) GetPresignedURL(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	bucketName := a.cfg.BucketName
	// 生成预签名URL
	url, err := a.client.PresignedGetObject(ctx, bucketName, objectName, expiry, nil)
	if err != nil {
		return "", fmt.Errorf("生成预签名URL %s/%s 失败: %w", bucketName, objectName, err)
	}
	return url.String(), nil
}

// DeleteFile 删除MinIO中的文件 (objectName 包含路径前缀)
func (a *MinIOAdapter) DeleteFile(ctx context.Context, objectName string) error {
	bucketName := a.cfg.BucketName
	return a.client.RemoveObject(ctx, bucketName, objectName, minio.RemoveObjectOptions{})
}
