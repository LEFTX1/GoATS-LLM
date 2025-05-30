package singleton

import (
	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/storage"
	"sync"
)

var (
	rabbitmqInstance *storage.RabbitMQAdapter
	rabbitmqOnce     sync.Once
	rabbitmqMutex    sync.Mutex
)

// GetRabbitMQAdapter 获取RabbitMQ适配器的单例实例
// 如果实例不存在则创建，存在则返回已有实例
func GetRabbitMQAdapter(cfg *config.RabbitMQConfig) (*storage.RabbitMQAdapter, error) {
	if rabbitmqInstance != nil {
		return rabbitmqInstance, nil
	}

	rabbitmqMutex.Lock()
	defer rabbitmqMutex.Unlock()

	if rabbitmqInstance != nil {
		return rabbitmqInstance, nil
	}

	var err error
	rabbitmqOnce.Do(func() {
		rabbitmqInstance, err = storage.NewRabbitMQAdapter(cfg)
	})

	return rabbitmqInstance, err
}

// ResetRabbitMQAdapter 重置RabbitMQ适配器单例（主要用于测试）
func ResetRabbitMQAdapter() {
	rabbitmqMutex.Lock()
	defer rabbitmqMutex.Unlock()

	// 如果实例存在，先关闭连接
	if rabbitmqInstance != nil {
		_ = rabbitmqInstance.Close()
	}

	rabbitmqInstance = nil
	rabbitmqOnce = sync.Once{}
}
