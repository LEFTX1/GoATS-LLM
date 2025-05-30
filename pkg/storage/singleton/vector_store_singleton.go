package singleton

import (
	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/storage"
	"sync"
)

var (
	vectorStoreInstance *storage.ResumeVectorStore
	vectorStoreOnce     sync.Once
	vectorStoreMutex    sync.Mutex
)

// GetResumeVectorStore 获取ResumeVectorStore的单例实例
// 如果实例不存在则创建，存在则返回已有实例
func GetResumeVectorStore(cfg *config.Config) (*storage.ResumeVectorStore, error) {
	if vectorStoreInstance != nil {
		return vectorStoreInstance, nil
	}

	vectorStoreMutex.Lock()
	defer vectorStoreMutex.Unlock()

	if vectorStoreInstance != nil {
		return vectorStoreInstance, nil
	}

	var err error
	vectorStoreOnce.Do(func() {
		vectorStoreInstance, err = storage.NewResumeVectorStore(cfg)
	})

	return vectorStoreInstance, err
}

// ResetResumeVectorStore 重置ResumeVectorStore单例（主要用于测试）
func ResetResumeVectorStore() {
	vectorStoreMutex.Lock()
	defer vectorStoreMutex.Unlock()
	vectorStoreInstance = nil
	vectorStoreOnce = sync.Once{}
}
