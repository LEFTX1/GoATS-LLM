package singleton

import (
	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/storage"
	"sync"
)

var (
	minioInstance *storage.MinIOAdapter
	minioOnce     sync.Once
	minioMutex    sync.Mutex
)

// GetMinIOAdapter 获取MinIO适配器的单例实例
// 如果实例不存在则创建，存在则返回已有实例
func GetMinIOAdapter(cfg *config.MinIOConfig) (*storage.MinIOAdapter, error) {
	if minioInstance != nil {
		return minioInstance, nil
	}

	minioMutex.Lock()
	defer minioMutex.Unlock()

	if minioInstance != nil {
		return minioInstance, nil
	}

	var err error
	minioOnce.Do(func() {
		minioInstance, err = storage.NewMinIOAdapter(cfg)
	})

	return minioInstance, err
}

// ResetMinIOAdapter 重置MinIO适配器单例（主要用于测试）
func ResetMinIOAdapter() {
	minioMutex.Lock()
	defer minioMutex.Unlock()
	minioInstance = nil
	minioOnce = sync.Once{}
}
