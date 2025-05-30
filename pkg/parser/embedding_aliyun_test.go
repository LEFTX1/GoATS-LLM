package parser_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/parser"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	envAliyunAPIKey = "ALIYUN_API_KEY"
)

// loadAliyunTestConfig loads the configuration for Aliyun tests.
// It attempts to find config.yaml relative to the test file first,
// then falls back to LoadConfigFromFileOnly("") which has its own search logic
// and provides a default config for test environments if no file is found.
func loadAliyunTestConfig(t *testing.T) *config.Config {
	// Get current file path
	_, b, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("Failed to get caller information to locate config file")
	}
	basepath := filepath.Dir(b)
	// config.yaml should be at ../../config.yaml from pkg/parser/
	configPath := filepath.Join(basepath, "..", "..", "config.yaml")

	cfg, err := config.LoadConfigFromFileOnly(configPath)
	if err != nil {
		// Fallback strategy: try loading with an empty path, which allows LoadConfigFromFileOnly to search standard paths.
		// This is useful if tests are run from a different working directory (e.g., project root).
		cfg, err = config.LoadConfigFromFileOnly("")
		if err != nil {
			t.Fatalf("Failed to load config using relative path (%s) or standard search: %v. Ensure config.yaml is accessible or default test config is intended.", configPath, err)
		}
	}
	require.NotNil(t, cfg, "Loaded config should not be nil")
	return cfg
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
	embeddings, err := embedder.EmbedStrings(ctx, textsToEmbed)

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

	textsToEmbed := []string{}
	ctx := context.Background()
	embeddings, err := embedder.EmbedStrings(ctx, textsToEmbed)

	require.Error(t, err, "EmbedStrings should return an error for empty input text array")
	require.Nil(t, embeddings, "Embeddings should be nil when an error occurs for empty input")
	if err != nil {
		// Expecting an error related to empty input for OpenAI compatible API.
		// e.g., "You must provide an HPOpenAIEmbeddingRequest.Input that is not empty."
		// The actual error message might vary slightly.
		assert.Contains(t, strings.ToLower(err.Error()), "input", "Error message should indicate an issue with the input being empty or invalid")
		t.Logf("Received expected error for empty input: %v", err)
	}
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
