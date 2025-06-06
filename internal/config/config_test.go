package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadConfigWithCorrectMapSyntax 验证当 YAML 语法正确时，配置能否被成功加载
func TestLoadConfigWithCorrectMapSyntax(t *testing.T) {
	// 1. 创建一个临时的 YAML 配置文件，内容包含正确的 map 结构
	correctYAMLContent := `
rabbitmq:
  url: "amqp://guest:guest@localhost:5672/"
  prefetch_count: 10
  consumer_workers:
    upload_consumer_workers: 5
    llm_consumer_workers: 3
  batch_timeouts:
    upload_batch_timeout: "5s"
`
	// 创建一个临时目录来存放配置文件
	tmpDir, err := os.MkdirTemp("", "config-test")
	require.NoError(t, err, "无法创建临时目录")
	defer os.RemoveAll(tmpDir) // 测试结束后清理目录

	// 配置文件路径
	configPath := filepath.Join(tmpDir, "config.yaml")
	err = os.WriteFile(configPath, []byte(correctYAMLContent), 0644)
	require.NoError(t, err, "无法写入临时配置文件")

	// 2. 调用 LoadConfig 函数加载配置
	config, err := LoadConfig(configPath)

	// 3. 断言结果
	require.NoError(t, err, "加载具有正确语法的配置不应返回错误")
	require.NotNil(t, config, "配置对象不应为 nil")

	// 验证 consumer_workers
	expectedConsumerWorkers := map[string]int{
		"upload_consumer_workers": 5,
		"llm_consumer_workers":    3,
	}
	assert.Equal(t, expectedConsumerWorkers, config.RabbitMQ.ConsumerWorkers, "RabbitMQ.ConsumerWorkers 的值与预期不符")

	// 验证 batch_timeouts
	expectedBatchTimeouts := map[string]string{
		"upload_batch_timeout": "5s",
	}
	assert.Equal(t, expectedBatchTimeouts, config.RabbitMQ.BatchTimeouts, "RabbitMQ.BatchTimeouts 的值与预期不符")

	// 验证其他字段是否也被加载
	assert.Equal(t, 10, config.RabbitMQ.PrefetchCount, "PrefetchCount 的值与预期不符")
}

// TestLoadConfigWithIncorrectMapSyntax 验证当 YAML 缩进错误时，map 字段无法被正确解析
func TestLoadConfigWithIncorrectMapSyntax(t *testing.T) {
	// 1. 创建一个包含错误缩进的 YAML 配置文件
	incorrectYAMLContent := `
rabbitmq:
  prefetch_count: 10
  consumer_workers: # map类型
  upload_consumer_workers: 5
  llm_consumer_workers: 3
`
	tmpDir, err := os.MkdirTemp("", "config-test-incorrect")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")
	err = os.WriteFile(configPath, []byte(incorrectYAMLContent), 0644)
	require.NoError(t, err)

	// 2. 加载配置
	config, err := LoadConfig(configPath)

	// 3. 断言结果
	// go-yaml/v3 在解析这种格式时不会报错，但会将 consumer_workers 解析为空 map
	require.NoError(t, err, "加载语法错误的配置也不应立即报错")
	require.NotNil(t, config, "配置对象不应为 nil")

	// 关键断言：因为缩进错误，consumer_workers 这个 map 应该是空的 (nil or len 0)
	assert.Empty(t, config.RabbitMQ.ConsumerWorkers, "由于缩进错误，ConsumerWorkers map 应该是空的")
}
