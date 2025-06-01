package parser_test

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/parser"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	envAliyunAPIKey = "ALIYUN_API_KEY"
)

// loadAliyunTestConfig loads the configuration for Aliyun tests.
// It attempts to find config.yaml relative to the test file first,
// then falls back to default test config if file is not found.
func loadAliyunTestConfig(t *testing.T) *config.Config {
	// 获取当前文件路径
	_, b, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("无法获取当前文件路径")
	}

	// 构建到配置文件的相对路径
	// embedding_aliyun_test.go 在 internal/parser/ 目录，配置文件在 internal/config/
	basepath := filepath.Dir(b)
	configPath := filepath.Join(basepath, "..", "config", "config.yaml")

	t.Logf("尝试从以下路径加载配置: %s", configPath)

	cfg, err := config.LoadConfigFromFileOnly(configPath)
	if err != nil {
		t.Logf("加载配置失败: %v，使用默认测试配置", err)
		// 如果找不到配置文件，创建测试用的默认配置
		return createDefaultTestConfig()
	}

	require.NotNil(t, cfg, "加载的配置不应为空")
	return cfg
}

// 创建默认测试配置
func createDefaultTestConfig() *config.Config {
	return &config.Config{
		Aliyun: struct {
			APIKey     string                 `yaml:"api_key"`
			APIURL     string                 `yaml:"api_url"`
			Model      string                 `yaml:"model"`
			TaskModels map[string]string      `yaml:"task_models"` // 任务专用模型
			Embedding  config.EmbeddingConfig `yaml:"embedding"`   // Embedding specific config
		}{
			APIKey: "test_api_key",
			Embedding: config.EmbeddingConfig{
				Model:      "text-embedding-v3",
				Dimensions: 1536,
				BaseURL:    "https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings",
			},
		},
	}
}

// TestAliyunEmbedder_EmbedStrings_Success 测试成功获取嵌入
func TestAliyunEmbedder_EmbedStrings_Success(t *testing.T) {
	cfg := loadAliyunTestConfig(t)
	apiKey := cfg.Aliyun.APIKey
	embeddingCfg := cfg.Aliyun.Embedding // Get the embedding specific config

	if apiKey == "" || apiKey == "your_api_key_here" || apiKey == "test_api_key" { // test_api_key is from createDefaultConfig if config.yaml not found
		t.Skipf("Skipping Aliyun embedder test because Aliyun API key is not configured in config.yaml or is a placeholder/default test key. Found: '%s'", apiKey)
	}

	// 使用配置中的模型和维度
	embedder, err := parser.NewAliyunEmbedder(apiKey, embeddingCfg)
	require.NoError(t, err, "NewAliyunEmbedder should not return an error with a valid API key and embedding config from config")
	require.NotNil(t, embedder, "Embedder should not be nil")

	textsToEmbed := []string{
		"你好，世界！",
		"这是一个测试文本。",
	}

	ctx := context.Background()
	embeddings, err := embedder.EmbedStrings(ctx, textsToEmbed, []embedding.Option{}...)

	require.NoError(t, err, "EmbedStrings should not return an error for valid input")
	require.NotNil(t, embeddings, "Embeddings should not be nil")
	require.Len(t, embeddings, len(textsToEmbed), "Number of embeddings should match number of input texts")

	for i, emb := range embeddings {
		assert.NotEmpty(t, emb, "Embedding vector for text %d should not be empty", i)
		expectedDimension := embedder.GetDimensions()

		assert.Len(t, emb, expectedDimension, "Embedding vector for text %d should have the expected dimension", i)

		// Safely log first few elements
		logLength := 3
		if len(emb) < 3 {
			logLength = len(emb)
		}
		t.Logf("Text %d: '%s', Embedding dim: %d (First %d: %v...)", i, textsToEmbed[i], len(emb), logLength, emb[:logLength])
	}
}

// TestAliyunEmbedder_EmbedStrings_EmptyInput 测试输入空文本数组
func TestAliyunEmbedder_EmbedStrings_EmptyInput(t *testing.T) {
	cfg := loadAliyunTestConfig(t)
	apiKey := cfg.Aliyun.APIKey
	embeddingCfg := cfg.Aliyun.Embedding // Get the embedding specific config

	// For this test, we need an embedder instance. If the configured API key is not real,
	// use a dummy one that allows NewAliyunEmbedder to initialize.
	// The test focuses on EmbedStrings behavior with empty input, not API key validity itself.
	if apiKey == "" || apiKey == "your_api_key_here" || apiKey == "test_api_key" {
		apiKey = "dummy_api_key_for_empty_input_test" // Must be non-empty
	}

	// If embeddingCfg is empty (e.g. from a minimal default test config where Aliyun.Embedding might not be fully populated by createDefaultConfig initially)
	// provide minimal valid defaults for the test to proceed with the dummy key.
	if embeddingCfg.Model == "" {
		embeddingCfg.Model = "text-embedding-v3"
	}
	if embeddingCfg.BaseURL == "" {
		embeddingCfg.BaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings"
	}
	// Dimensions can be 0 or default (1024), NewAliyunEmbedder handles 0 as default.

	embedder, err := parser.NewAliyunEmbedder(apiKey, embeddingCfg)
	require.NoError(t, err, "NewAliyunEmbedder with a (potentially dummy) non-empty API key and embedding config should not fail initialization for this test")
	require.NotNil(t, embedder, "Embedder should not be nil for empty input test")

	var textsToEmbed []string
	ctx := context.Background()
	embeddings, err := embedder.EmbedStrings(ctx, textsToEmbed, []embedding.Option{}...)

	// 注意：该行为已更改，空输入时不应返回错误而是返回空切片
	require.NoError(t, err, "EmbedStrings现在应处理空输入并返回空切片而非错误")
	require.NotNil(t, embeddings, "返回的embeddings应该是一个空切片而非nil")
	require.Empty(t, embeddings, "对于空输入，应返回空嵌入向量切片")
}

// TestAliyunEmbedder_NewAliyunEmbedder_NoAPIKey 测试没有API Key时初始化
// This test remains unchanged as it tests the behavior of NewAliyunEmbedder
// when the apiKey argument is empty AND the environment variable is not set,
// which is independent of config file loading for API key.
func TestAliyunEmbedder_NewAliyunEmbedder_NoAPIKey(t *testing.T) {
	// 确保环境变量未设置
	originalApiKey := os.Getenv(envAliyunAPIKey)
	os.Unsetenv(envAliyunAPIKey)
	defer os.Setenv(envAliyunAPIKey, originalApiKey) // 恢复环境变量

	// Create a default-like empty EmbeddingConfig for this specific test,
	// as NewAliyunEmbedder now requires it.
	// The test is about the apiKey parameter being empty.
	emptyEmbeddingCfg := config.EmbeddingConfig{}

	_, err := parser.NewAliyunEmbedder("", emptyEmbeddingCfg) // 传入空字符串 apiKey
	require.Error(t, err, "NewAliyunEmbedder should return an error if API key is not provided and not in env")
	assert.Contains(t, err.Error(), "API密钥不能为空", "Error message should indicate API key is missing")
}
