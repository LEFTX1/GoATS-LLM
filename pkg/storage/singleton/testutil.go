package singleton

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ai-agent-go/pkg/config"
)

// TestConfig 测试配置，包含所有可能需要的依赖配置
type TestConfig struct {
	MinIO    *config.MinIOConfig
	MySQL    *config.MySQLConfig
	RabbitMQ *config.RabbitMQConfig
	Qdrant   struct {
		Endpoint   string
		Collection string
		Dimension  int
	}
	TestBucketName string // 用于MinIO测试的桶名（自动生成随机后缀）
}

// LoadTestConfig 从配置文件和环境变量加载测试配置
// configPath: 配置文件路径，如果为空则使用默认路径
// prefix: 环境变量前缀，用于覆盖配置，如TEST_
func LoadTestConfig(configPath string, prefix string) (*TestConfig, error) {
	// 加载基础配置
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("加载基础配置失败: %w", err)
	}
	// 创建测试配置
	testCfg := &TestConfig{
		MinIO:    &cfg.MinIO,
		MySQL:    &cfg.MySQL,
		RabbitMQ: &cfg.RabbitMQ,
		Qdrant: struct {
			Endpoint   string
			Collection string
			Dimension  int
		}{
			Endpoint:   cfg.Qdrant.Endpoint,
			Collection: cfg.Qdrant.Collection,
			Dimension:  cfg.Qdrant.Dimension,
		},
		TestBucketName: fmt.Sprintf("test-bucket-%d", time.Now().UnixNano()),
	}
	return testCfg, nil
}

// LoadTestConfigNoEnv 从配置文件加载测试配置，不使用环境变量覆盖
// configPath: 配置文件路径，如果为空则使用默认路径
func LoadTestConfigNoEnv(configPath string) (*TestConfig, error) {
	// 加载基础配置，确保不从环境变量读取
	cfg, err := config.LoadConfigFromFileOnly(configPath)
	if err != nil {
		return nil, fmt.Errorf("加载基础配置失败 (LoadConfigFromFileOnly): %w", err)
	}

	// 创建测试配置
	testCfg := &TestConfig{
		MinIO:    &cfg.MinIO,
		MySQL:    &cfg.MySQL,
		RabbitMQ: &cfg.RabbitMQ,
		Qdrant: struct {
			Endpoint   string
			Collection string
			Dimension  int
		}{
			Endpoint:   cfg.Qdrant.Endpoint,
			Collection: cfg.Qdrant.Collection,
			Dimension:  cfg.Qdrant.Dimension,
		},
		TestBucketName: fmt.Sprintf("test-bucket-%d", time.Now().UnixNano()),
	}

	return testCfg, nil
}

// FindTestConfigFile 查找测试配置文件
func FindTestConfigFile() string {
	// 可能的配置文件路径
	// 优先检查相对于当前目录更深层级的路径，以期找到项目根目录的config.yaml
	candidatePaths := []string{
		"../../../config.yaml", // 例如从 pkg/storage/singleton 到项目根目录
		"../../../config.test.yaml",
		"../../config.yaml",
		"../../config.test.yaml",
		"../config.yaml",
		"../config.test.yaml",
		"config.yaml",
		"config.test.yaml",
	}

	// 获取工作目录
	workDir, err := os.Getwd()
	if err == nil {
		// 添加从工作目录开始查找的路径
		for _, candidate := range candidatePaths {
			candidatePaths = append(candidatePaths, filepath.Join(workDir, candidate))
		}
	}

	// 获取可执行文件目录
	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)
		// 添加从可执行文件目录开始查找的路径
		for _, candidate := range candidatePaths {
			candidatePaths = append(candidatePaths, filepath.Join(execDir, candidate))
		}
	}

	// 查找第一个存在的文件
	for _, path := range candidatePaths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// 如果没有找到配置文件，返回默认路径供LoadConfig创建默认配置
	return "config.yaml"
}
