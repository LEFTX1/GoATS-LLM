package singleton_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"ai-agent-go/pkg/storage/singleton"
)

// TestMinIOOperations 测试MinIO的基本操作
func TestMinIOOperations(t *testing.T) {
	// 重置单例确保测试隔离
	singleton.ResetMinIOAdapter()

	// 加载测试配置
	configPath := singleton.FindTestConfigFile()
	testCfg, err := singleton.LoadTestConfigNoEnv(configPath)
	if err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}

	// 检查配置是否有效
	if testCfg.MinIO.Endpoint == "" || testCfg.MinIO.AccessKeyID == "" || testCfg.MinIO.SecretAccessKey == "" {
		t.Fatalf("MinIO配置无效，请确保config.yaml中包含有效的MinIO配置")
	}

	// 修改MinIO配置使用测试专用桶名
	testCfg.MinIO.BucketName = testCfg.TestBucketName

	// 获取MinIO适配器实例
	adapter, err := singleton.GetMinIOAdapter(testCfg.MinIO)
	if err != nil {
		t.Fatalf("获取MinIO适配器失败: %v", err)
	}

	// 创建测试上下文
	ctx := context.Background()

	// 测试文件上传
	testContent := []byte("这是一个测试文件内容")
	objectName := "test-folder/test-file.txt"

	// 上传文件
	uploadedObject, err := adapter.UploadFileFromBytes(ctx, objectName, testContent, "text/plain")
	if err != nil {
		t.Fatalf("上传文件失败: %v", err)
	}

	// 验证上传的对象名称
	if uploadedObject != objectName {
		t.Errorf("返回的对象名称错误，期望: %s，实际: %s", objectName, uploadedObject)
	}

	// 测试文件下载
	downloadedContent, err := adapter.DownloadFile(ctx, objectName)
	if err != nil {
		t.Fatalf("下载文件失败: %v", err)
	}

	// 验证下载的内容是否与上传的相同
	if !bytes.Equal(testContent, downloadedContent) {
		t.Errorf("下载的内容与上传的内容不匹配")
	}

	// 测试获取预签名URL
	_, err = adapter.GetPresignedURL(ctx, objectName, 1*time.Hour)
	if err != nil {
		t.Fatalf("获取预签名URL失败: %v", err)
	}

	// 测试删除文件
	err = adapter.DeleteFile(ctx, objectName)
	if err != nil {
		t.Fatalf("删除文件失败: %v", err)
	}

	// 尝试再次下载以验证文件已被删除
	_, err = adapter.DownloadFile(ctx, objectName)
	if err == nil {
		t.Errorf("文件应该已被删除，但仍能下载")
	}

	t.Logf("MinIO操作测试成功完成")
}
