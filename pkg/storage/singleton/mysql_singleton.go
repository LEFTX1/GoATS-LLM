package singleton

import (
	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/storage"
	"sync"
)

var (
	mysqlInstance *storage.MySQLAdapter
	mysqlOnce     sync.Once
	mysqlMutex    sync.Mutex
)

// GetMySQLAdapter 获取MySQL适配器的单例实例
// 如果实例不存在则创建，存在则返回已有实例
func GetMySQLAdapter(cfg *config.MySQLConfig) (*storage.MySQLAdapter, error) {
	if mysqlInstance != nil {
		return mysqlInstance, nil
	}

	mysqlMutex.Lock()
	defer mysqlMutex.Unlock()

	if mysqlInstance != nil {
		return mysqlInstance, nil
	}

	var err error
	mysqlOnce.Do(func() {
		mysqlInstance, err = storage.NewMySQLAdapter(cfg)
	})

	return mysqlInstance, err
}

// ResetMySQLAdapter 重置MySQL适配器单例（主要用于测试）
func ResetMySQLAdapter() {
	mysqlMutex.Lock()
	defer mysqlMutex.Unlock()
	mysqlInstance = nil
	mysqlOnce = sync.Once{}
}
