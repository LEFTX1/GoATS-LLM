package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"ai-agent-go/pkg/config"
)

// AliyunEmbedder 实现embedding服务
type AliyunEmbedder struct {
	apiKey     string
	model      string
	dimensions int // For OpenAI compatible API, dimensions might be part of the request body
	httpClient *http.Client
	baseURL    string
}

// AliyunEmbedderOption is removed as config is primary now.

// NewAliyunEmbedder 创建新的阿里云Embedder (using OpenAI compatible endpoint)
func NewAliyunEmbedder(apiKey string, embeddingCfg config.EmbeddingConfig) (*AliyunEmbedder, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API密钥不能为空")
	}

	model := embeddingCfg.Model
	if model == "" {
		model = "text-embedding-v3" // Fallback default
	}
	dimensions := embeddingCfg.Dimensions
	if dimensions == 0 {
		dimensions = 1024 // Fallback default
	}
	baseURL := embeddingCfg.BaseURL
	if baseURL == "" {
		baseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings" // Fallback default
	}

	embedder := &AliyunEmbedder{
		apiKey:     apiKey,
		model:      model,
		dimensions: dimensions,
		httpClient: &http.Client{},
		baseURL:    baseURL,
	}

	return embedder, nil
}

// GetDimensions 返回嵌入器配置的维度
func (a *AliyunEmbedder) GetDimensions() int {
	return a.dimensions
}

// AliyunEmbeddingRequest 阿里云Embedding请求结构 (OpenAI compatible)
type AliyunOpenAIEmbeddingRequest struct {
	Input          interface{} `json:"input"` // string or []string
	Model          string      `json:"model"`
	Dimensions     int         `json:"dimensions,omitempty"`      // Optional, for text-embedding-v3
	EncodingFormat string      `json:"encoding_format,omitempty"` // Optional, e.g., "float"
}

// AliyunEmbeddingResponse 阿里云Embedding响应结构 (OpenAI compatible)
type AliyunOpenAIEmbeddingResponse struct {
	Object string                  `json:"object"` // e.g., "list"
	Data   []AliyunOpenAIDataEntry `json:"data"`
	Model  string                  `json:"model"`
	Usage  AliyunOpenAIUsage       `json:"usage"`
	ID     string                  `json:"id,omitempty"` // Older versions might not have this or it's called request_id
	// Error field for when HTTP status is OK but API returns an error object
	Error *AliyunOpenAIError `json:"error,omitempty"`
}

// AliyunOpenAIDataEntry part of the response
type AliyunOpenAIDataEntry struct {
	Object    string    `json:"object"` // e.g., "embedding"
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

// AliyunOpenAIUsage part of the response
type AliyunOpenAIUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// AliyunOpenAIError for API-level errors returned with 200 OK
type AliyunOpenAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    string `json:"code"`
}

// EmbedStrings 将文本转换为向量
func (a *AliyunEmbedder) EmbedStrings(ctx context.Context, texts []string) ([][]float64, error) {
	var inputBody interface{}
	if len(texts) == 1 {
		inputBody = texts[0] // API might prefer single string if only one
	} else {
		inputBody = texts
	}

	reqBody := AliyunOpenAIEmbeddingRequest{
		Input: inputBody,
		Model: a.model,
	}

	// Only set dimensions if it's not the default or if explicitly set to something other than 0
	// For text-embedding-v3, 1024 is default. API might infer if not sent.
	// However, to be explicit, if a.dimensions is set (e.g. not 0 or some other indicator of default),
	// we include it.
	if a.dimensions > 0 && a.dimensions != 1024 { // Assuming 1024 is a common default we might not need to send
		// Or always send if a.dimensions is not 0, let API decide if it's default
		reqBody.Dimensions = a.dimensions
	}
	// reqBody.EncodingFormat = "float" // Default is float, so usually not needed

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Try to parse an error response from Aliyun if possible
		var apiError AliyunOpenAIError
		if json.Unmarshal(body, &apiError) == nil && apiError.Message != "" {
			return nil, fmt.Errorf("API调用失败, 状态码: %d, 类型: %s, 错误: %s, Code: %s", resp.StatusCode, apiError.Type, apiError.Message, apiError.Code)
		} else {
			// Fallback if error parsing doesn't work or no specific message
			return nil, fmt.Errorf("API调用失败, 状态码: %d, 响应: %s", resp.StatusCode, string(body))
		}
	}

	var embeddingResp AliyunOpenAIEmbeddingResponse
	if err := json.Unmarshal(body, &embeddingResp); err != nil {
		return nil, fmt.Errorf("解析OpenAI兼容响应失败: %w, 响应体: %s", err, string(body))
	}

	// Check for API level error even if status is 200 OK
	if embeddingResp.Error != nil && embeddingResp.Error.Message != "" {
		return nil, fmt.Errorf("嵌入API调用失败(响应内错误): 类型: %s, 错误: %s, Code: %s", embeddingResp.Error.Type, embeddingResp.Error.Message, embeddingResp.Error.Code)
	}

	if len(embeddingResp.Data) == 0 {
		return nil, fmt.Errorf("API响应不包含嵌入数据, 响应: %s", string(body))
	}

	embeddings := make([][]float64, len(embeddingResp.Data))
	for _, item := range embeddingResp.Data {
		// Ensure data is sorted by index for safety, or map directly if API guarantees order
		if item.Index >= len(embeddings) {
			return nil, fmt.Errorf("嵌入数据索引 %d 超出范围 %d", item.Index, len(embeddings)-1)
		}
		embeddings[item.Index] = item.Embedding
	}

	return embeddings, nil
}
