package singleton

import (
	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/storage"
	"sync"
)

var (
	qdrantClientInstance *storage.QdrantClient
	qdrantClientOnce     sync.Once
	qdrantClientMutex    sync.Mutex
)

// GetQdrantClient 获取QdrantClient的单例实例
// 如果实例不存在则创建，存在则返回已有实例
func GetQdrantClient(cfg *config.Config) (*storage.QdrantClient, error) {
	if qdrantClientInstance != nil {
		return qdrantClientInstance, nil
	}

	qdrantClientMutex.Lock()
	defer qdrantClientMutex.Unlock()

	if qdrantClientInstance != nil {
		return qdrantClientInstance, nil
	}

	var err error
	qdrantClientOnce.Do(func() {
		qdrantClientInstance, err = storage.NewQdrantClient(cfg)
	})

	return qdrantClientInstance, err
}

// ResetQdrantClient 重置QdrantClient单例（主要用于测试）
func ResetQdrantClient() {
	qdrantClientMutex.Lock()
	defer qdrantClientMutex.Unlock()
	qdrantClientInstance = nil
	qdrantClientOnce = sync.Once{}
}
