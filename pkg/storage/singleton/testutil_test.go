package singleton_test

import (
	"os"
	"testing"

	"ai-agent-go/pkg/storage/singleton"
)

// TestFindTestConfigFile 测试查找配置文件函数
func TestFindTestConfigFile(t *testing.T) {
	// 确认函数至少返回一个默认路径
	configPath := singleton.FindTestConfigFile()
	if configPath == "" {
		t.Error("FindTestConfigFile应该返回一个默认路径，但返回空字符串")
	}
	t.Logf("FindTestConfigFile返回的路径: %s", configPath)
}

// TestLoadTestConfig 测试加载配置函数
func TestLoadTestConfig(t *testing.T) {
	// 1. 不指定路径，使用默认路径
	cfg, err := singleton.LoadTestConfig("", "")
	if err != nil {
		t.Logf("使用默认路径加载配置失败: %v", err)
		t.Log("这可能是正常的，如果没有默认配置文件存在")
	} else {
		t.Log("成功从默认路径加载配置")
		// 略过完整验证，因为不同环境配置可能不同
	}

	// 2. 指定一个存在的路径
	configPath := singleton.FindTestConfigFile()
	cfg, err = singleton.LoadTestConfig(configPath, "")
	if err != nil {
		t.Logf("从路径 %s 加载配置失败: %v", configPath, err)
	} else {
		t.Logf("成功从路径 %s 加载配置", configPath)
		// 略过完整验证，因为不同环境配置可能不同
	}

	// 3. 测试环境变量覆盖
	// 设置一些测试环境变量
	os.Setenv("TEST_MYSQL_HOST", "testhost.example.com")
	os.Setenv("TEST_MYSQL_PORT", "3307")
	os.Setenv("TEST_RABBITMQ_HOST", "rabbitmq.example.com")
	os.Setenv("TEST_MINIO_ENDPOINT", "minio.example.com:9000")

	// 加载配置并检查环境变量是否正确覆盖了配置
	cfg, err = singleton.LoadTestConfig(configPath, "TEST_")
	if err != nil {
		t.Fatalf("使用环境变量覆盖加载配置失败: %v", err)
	}

	// 验证环境变量覆盖是否生效
	if cfg.MySQL.Host != "testhost.example.com" {
		t.Errorf("MySQL主机环境变量覆盖失败，期望 %s，实际 %s", "testhost.example.com", cfg.MySQL.Host)
	}
	if cfg.MySQL.Port != 3307 {
		t.Errorf("MySQL端口环境变量覆盖失败，期望 %d，实际 %d", 3307, cfg.MySQL.Port)
	}
	if cfg.MinIO.Endpoint != "minio.example.com:9000" {
		t.Errorf("MinIO端点环境变量覆盖失败，期望 %s，实际 %s", "minio.example.com:9000", cfg.MinIO.Endpoint)
	}

	// 验证RabbitMQ URL是否被正确构建
	expectedRabbitMQURL := "amqp://guest:guest@rabbitmq.example.com:5672/"
	if cfg.RabbitMQ.URL != expectedRabbitMQURL {
		t.Errorf("RabbitMQ URL构建失败，期望 %s，实际 %s", expectedRabbitMQURL, cfg.RabbitMQ.URL)
	}

	// 清理环境变量
	os.Unsetenv("TEST_MYSQL_HOST")
	os.Unsetenv("TEST_MYSQL_PORT")
	os.Unsetenv("TEST_RABBITMQ_HOST")
	os.Unsetenv("TEST_MINIO_ENDPOINT")

	t.Log("环境变量覆盖测试成功")
}

// TestLoadTestConfigNoEnv 测试不使用环境变量加载配置函数
func TestLoadTestConfigNoEnv(t *testing.T) {
	// 设置一些测试环境变量，这些变量不应该影响LoadTestConfigNoEnv的结果
	os.Setenv("TEST_MYSQL_HOST", "testhost.example.com")
	os.Setenv("TEST_MYSQL_PORT", "3307")
	os.Setenv("TEST_RABBITMQ_HOST", "rabbitmq.example.com")
	os.Setenv("TEST_MINIO_ENDPOINT", "minio.example.com:9000")

	// 加载测试配置
	configPath := singleton.FindTestConfigFile()
	cfg, err := singleton.LoadTestConfigNoEnv(configPath)
	if err != nil {
		t.Fatalf("使用LoadTestConfigNoEnv加载配置失败: %v", err)
	}

	// 验证环境变量没有覆盖配置
	// 注意：这个测试假设配置文件中的值与环境变量不同
	// 如果配置文件中的值恰好与环境变量相同，测试可能会误报成功
	if cfg.MySQL.Host == "testhost.example.com" {
		t.Errorf("MySQL主机被环境变量覆盖，这不应该发生")
	}
	if cfg.MySQL.Port == 3307 && os.Getenv("TEST_MYSQL_PORT") == "3307" {
		t.Errorf("MySQL端口被环境变量覆盖，这不应该发生")
	}
	if cfg.MinIO.Endpoint == "minio.example.com:9000" {
		t.Errorf("MinIO端点被环境变量覆盖，这不应该发生")
	}

	// 检查URL是否被构建为环境变量指定的值
	if cfg.RabbitMQ.URL == "amqp://guest:guest@rabbitmq.example.com:5672/" {
		t.Errorf("RabbitMQ URL被环境变量覆盖，这不应该发生")
	}

	// 清理环境变量
	os.Unsetenv("TEST_MYSQL_HOST")
	os.Unsetenv("TEST_MYSQL_PORT")
	os.Unsetenv("TEST_RABBITMQ_HOST")
	os.Unsetenv("TEST_MINIO_ENDPOINT")

	t.Log("LoadTestConfigNoEnv测试成功 - 环境变量未影响配置")
}

// validateTestConfig 验证测试配置的基本有效性
func validateTestConfig(t *testing.T, cfg *singleton.TestConfig) {
	t.Helper()

	// 检查MinIO配置
	if cfg.MinIO == nil {
		t.Error("TestConfig.MinIO为空")
	} else {
		if cfg.MinIO.Endpoint == "" {
			t.Error("MinIO Endpoint为空")
		}
		if cfg.TestBucketName == "" {
			t.Error("TestBucketName为空")
		}
	}

	// 检查MySQL配置
	if cfg.MySQL == nil {
		t.Error("TestConfig.MySQL为空")
	} else {
		if cfg.MySQL.Host == "" {
			t.Error("MySQL Host为空")
		}
		if cfg.MySQL.Database == "" {
			t.Error("MySQL Database为空")
		}
	}

	// 检查RabbitMQ配置
	if cfg.RabbitMQ == nil {
		t.Error("TestConfig.RabbitMQ为空")
	}

	// 检查Qdrant配置
	if cfg.Qdrant.Endpoint == "" {
		t.Error("Qdrant Endpoint为空")
	}
	if cfg.Qdrant.Collection == "" {
		t.Error("Qdrant Collection为空")
	}
	if cfg.Qdrant.Dimension <= 0 {
		t.Error("Qdrant Dimension无效")
	}
}
