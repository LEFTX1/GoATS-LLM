package singleton_test

import (
	"strings"
	"testing"

	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/storage"
	"ai-agent-go/pkg/storage/singleton"
)

// TestGetResumeVectorStore 测试向量存储单例模式
func TestGetResumeVectorStore(t *testing.T) {
	// 重置单例确保测试隔离
	singleton.ResetResumeVectorStore()

	// 加载测试配置
	configPath := singleton.FindTestConfigFile()
	testCfg, err := singleton.LoadTestConfigNoEnv(configPath)
	if err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}

	// 检查配置是否有效
	if testCfg.Qdrant.Endpoint == "" || testCfg.Qdrant.Collection == "" || testCfg.Qdrant.Dimension <= 0 {
		t.Fatalf("Qdrant配置无效，请确保config.yaml中包含有效的Qdrant配置")
	}

	// 创建标准配置对象
	cfg := &config.Config{}
	// 设置Qdrant配置
	cfg.Qdrant.Endpoint = testCfg.Qdrant.Endpoint
	cfg.Qdrant.Collection = testCfg.Qdrant.Collection
	cfg.Qdrant.Dimension = testCfg.Qdrant.Dimension

	// 测试获取实例
	var firstInstance, secondInstance *storage.ResumeVectorStore
	var err2 error

	// 第一次获取实例
	firstInstance, err = singleton.GetResumeVectorStore(cfg)
	if err != nil {
		// 检查是否是连接错误
		if strings.Contains(err.Error(), "connection") ||
			strings.Contains(err.Error(), "refused") ||
			strings.Contains(err.Error(), "timeout") {
			t.Skipf("无法连接到Qdrant服务器，请检查配置或确保服务已启动: %v", err)
		}
		t.Fatalf("第一次获取向量存储实例失败: %v", err)
	}

	if firstInstance == nil {
		t.Fatal("向量存储实例不应为nil")
	}

	// 第二次获取实例
	secondInstance, err2 = singleton.GetResumeVectorStore(cfg)
	if err2 != nil {
		t.Fatalf("第二次获取向量存储实例失败: %v", err2)
	}

	// 验证是否是同一个实例
	if firstInstance != secondInstance {
		t.Fatal("单例模式失败：两次获取的实例不是同一个")
	} else {
		t.Log("单例模式验证成功：两次获取的是同一个实例")
	}

	// 重置单例并验证是否真的重置了
	singleton.ResetResumeVectorStore()
	t.Log("向量存储单例已重置")

	// 再次获取实例，应该是新的实例
	newInstance, err := singleton.GetResumeVectorStore(cfg)
	if err != nil {
		t.Fatalf("重置后获取向量存储实例失败: %v", err)
	}

	if newInstance == firstInstance {
		t.Fatal("重置单例失败：获取的仍然是旧实例")
	} else {
		t.Log("重置单例验证成功：获取了新的实例")
	}
}
