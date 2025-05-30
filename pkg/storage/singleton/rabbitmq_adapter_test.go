package singleton_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"ai-agent-go/pkg/storage/singleton"
)

// TestRabbitMQOperations 测试RabbitMQ的基本操作
func TestRabbitMQOperations(t *testing.T) {
	// 重置单例确保测试隔离
	singleton.ResetRabbitMQAdapter()

	// 加载测试配置
	configPath := singleton.FindTestConfigFile()
	testCfg, err := singleton.LoadTestConfigNoEnv(configPath)
	if err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}

	// 检查配置是否有效
	if testCfg.RabbitMQ.URL == "" {
		t.Fatalf("RabbitMQ配置无效，请确保config.yaml中包含有效的RabbitMQ配置")
	}

	// 获取RabbitMQ适配器实例
	adapter, err := singleton.GetRabbitMQAdapter(testCfg.RabbitMQ)
	if err != nil {
		t.Fatalf("获取RabbitMQ适配器失败: %v", err)
	}

	// 测试交换机和队列配置
	testExchange := "test-exchange-" + time.Now().Format("20060102150405")
	testQueue := "test-queue-" + time.Now().Format("20060102150405")
	testRoutingKey := "test.key." + time.Now().Format("20060102150405")

	// 确保交换机存在
	if err := adapter.EnsureExchange(testExchange, "topic", true); err != nil {
		t.Fatalf("创建交换机失败: %v", err)
	}

	// 确保队列存在
	queue, err := adapter.EnsureQueue(testQueue, true)
	if err != nil {
		t.Fatalf("创建队列失败: %v", err)
	}

	if queue.Name != testQueue {
		t.Errorf("队列名称不匹配，期望: %s，实际: %s", testQueue, queue.Name)
	}

	// 绑定队列到交换机
	if err := adapter.BindQueue(testQueue, testExchange, testRoutingKey); err != nil {
		t.Fatalf("绑定队列到交换机失败: %v", err)
	}

	// 创建测试上下文
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 测试消息结构
	type TestMessage struct {
		ID      string    `json:"id"`
		Content string    `json:"content"`
		Time    time.Time `json:"time"`
	}

	// 准备测试消息
	testMessage := TestMessage{
		ID:      "test-message-id",
		Content: "这是一个测试消息",
		Time:    time.Now(),
	}

	// 发送消息
	if err := adapter.PublishJSON(ctx, testExchange, testRoutingKey, testMessage, true); err != nil {
		t.Fatalf("发布消息失败: %v", err)
	}

	// 设置接收消息的通道和标志
	messageReceived := make(chan bool, 1)
	var receivedMessage TestMessage

	// 启动消费者
	stopCh, err := adapter.StartConsumer(testQueue, 1, func(body []byte) bool {
		// 反序列化消息
		if err := json.Unmarshal(body, &receivedMessage); err != nil {
			t.Logf("反序列化消息失败: %v", err)
			return false
		}

		// 验证消息内容
		if receivedMessage.ID != testMessage.ID || receivedMessage.Content != testMessage.Content {
			t.Logf("收到的消息内容不匹配")
			return false
		}

		// 标记消息已接收并处理
		messageReceived <- true
		return true
	})
	if err != nil {
		t.Fatalf("启动消费者失败: %v", err)
	}

	// 不能直接关闭stopCh（接收端通道），使用defer将停止逻辑放在函数结束时执行
	defer func() {
		// 因为不能直接关闭接收通道，这里我们什么也不做
		// RabbitMQ适配器内部应该在测试结束时自动清理资源
		t.Log("测试结束，消费者将自动清理")
	}()

	// 等待消息接收或超时
	select {
	case <-messageReceived:
		// 消息成功接收
		t.Logf("成功接收到消息: ID=%s, Content=%s", receivedMessage.ID, receivedMessage.Content)
	case <-time.After(5 * time.Second):
		t.Fatalf("等待接收消息超时")
	case <-stopCh:
		t.Fatalf("消费者意外停止")
	}

	t.Logf("RabbitMQ操作测试成功完成")
}
