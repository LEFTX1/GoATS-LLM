package agent

import (
	"fmt"
	"sync"

	"github.com/cloudwego/eino/schema"
)

// ChatMemory 定义了聊天记忆存储的接口
type ChatMemory interface {
	// GetHistory 获取指定会话ID的聊天历史记录。
	// 如果会话不存在，应返回一个空的 Message 切片和 nil 错误。
	GetHistory(sessionId string) ([]*schema.Message, error)

	// AddMessage AddMessage向指定会话ID的聊天历史记录中添加一条消息。
	AddMessage(sessionId string, message *schema.Message) error

	// AddMessages 向指定会话ID的聊天历史记录中批量添加多条消息。
	AddMessages(sessionId string, messages []*schema.Message) error

	// ClearHistory 清除指定会话ID的所有聊天历史记录。
	// 如果会话不存在，此操作应静默成功。
	ClearHistory(sessionId string) error
}

// InMemoryChatMemory 是 ChatMemory 接口的一个简单内存实现。
// 注意：此实现不是持久化的，仅用于测试和简单场景。
type InMemoryChatMemory struct {
	//使用读写锁以支持并发访问
	mu sync.RWMutex
	// histories map 的键是 sessionId，值是该会话的消息列表
	histories map[string][]*schema.Message
}

// NewInMemoryChatMemory 创建一个新的 InMemoryChatMemory 实例。
func NewInMemoryChatMemory() *InMemoryChatMemory {
	return &InMemoryChatMemory{
		histories: make(map[string][]*schema.Message),
	}
}

// GetHistory 实现 ChatMemory 接口
func (m *InMemoryChatMemory) GetHistory(sessionId string) ([]*schema.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	history, ok := m.histories[sessionId]
	if !ok {
		// 如果会话不存在，返回空切片而不是 nil，以方便调用者处理
		return []*schema.Message{}, nil
	}
	// 返回历史记录的副本，以防止外部修改内部存储
	// 对于指针切片，浅拷贝通常足够，因为 Message 结构体通常在添加后不应被修改
	// 如果 Message 本身可能被修改，则需要深拷贝
	// 这里我们假设调用者不会修改返回的 Message 指针指向的内容
	cpy := make([]*schema.Message, len(history))
	copy(cpy, history)
	return cpy, nil
}

// AddMessage 实现 ChatMemory 接口
func (m *InMemoryChatMemory) AddMessage(sessionId string, message *schema.Message) error {
	if message == nil {
		return fmt.Errorf("cannot add nil message to chat history for session %s", sessionId)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.histories[sessionId]
	if !ok {
		m.histories[sessionId] = make([]*schema.Message, 0)
	}
	m.histories[sessionId] = append(m.histories[sessionId], message)
	return nil
}

// AddMessages 实现 ChatMemory 接口
func (m *InMemoryChatMemory) AddMessages(sessionId string, messages []*schema.Message) error {
	if len(messages) == 0 {
		return nil // 没有消息要添加
	}
	for _, msg := range messages {
		if msg == nil {
			return fmt.Errorf("cannot add nil message in a batch to chat history for session %s", sessionId)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.histories[sessionId]
	if !ok {
		m.histories[sessionId] = make([]*schema.Message, 0, len(messages))
	}
	m.histories[sessionId] = append(m.histories[sessionId], messages...)
	return nil
}

// ClearHistory 实现 ChatMemory 接口
func (m *InMemoryChatMemory) ClearHistory(sessionId string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.histories, sessionId) // 删除map中的条目即可
	return nil
}
