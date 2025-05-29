package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// AliyunEmbedder 实现embedding服务
type AliyunEmbedder struct {
	apiKey     string
	model      string
	dimensions int
	httpClient *http.Client
	baseURL    string
}

// AliyunEmbedderOption 阿里云Embedder选项
type AliyunEmbedderOption func(e *AliyunEmbedder)

// WithAliyunModel 设置模型名称
func WithAliyunModel(model string) AliyunEmbedderOption {
	return func(e *AliyunEmbedder) {
		e.model = model
	}
}

// WithAliyunDimensions 设置向量维度
func WithAliyunDimensions(dimensions int) AliyunEmbedderOption {
	return func(e *AliyunEmbedder) {
		e.dimensions = dimensions
	}
}

// WithAliyunBaseURL 设置API基础URL
func WithAliyunBaseURL(baseURL string) AliyunEmbedderOption {
	return func(e *AliyunEmbedder) {
		e.baseURL = baseURL
	}
}

// NewAliyunEmbedder 创建新的阿里云Embedder
func NewAliyunEmbedder(apiKey string, options ...AliyunEmbedderOption) (*AliyunEmbedder, error) {
	if apiKey == "" {
		apiKey = os.Getenv("ALIYUN_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("API密钥不能为空")
		}
	}

	embedder := &AliyunEmbedder{
		apiKey:     apiKey,
		model:      "text-embedding-v3",
		dimensions: 1024,
		httpClient: &http.Client{},
		baseURL:    "https://dashscope.aliyuncs.com/api/v1/services/embeddings/text-embedding/text-embedding",
	}

	// 应用选项
	for _, opt := range options {
		opt(embedder)
	}

	return embedder, nil
}

// AliyunEmbeddingRequest 阿里云Embedding请求结构
type AliyunEmbeddingRequest struct {
	Model     string   `json:"model"`
	Input     []string `json:"input"`
	Dimension int      `json:"dimension"`
}

// AliyunEmbeddingResponse 阿里云Embedding响应结构
type AliyunEmbeddingResponse struct {
	StatusCode int    `json:"status_code"`
	RequestID  string `json:"request_id"`
	Output     struct {
		Embeddings []struct {
			Embedding []float64 `json:"embedding"`
			TextIndex int       `json:"text_index"`
		} `json:"embeddings"`
	} `json:"output"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// EmbedStrings 将文本转换为向量
func (a *AliyunEmbedder) EmbedStrings(ctx context.Context, texts []string) ([][]float64, error) {
	// 构建请求
	reqBody := AliyunEmbeddingRequest{
		Model:     a.model,
		Input:     texts,
		Dimension: a.dimensions,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	// 发送请求
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	// 处理非成功状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API调用失败, 状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var embeddingResp AliyunEmbeddingResponse
	if err := json.Unmarshal(body, &embeddingResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w, 响应体: %s", err, string(body))
	}

	// 检查状态码
	if embeddingResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("嵌入API调用失败: 状态码 %d", embeddingResp.StatusCode)
	}

	// 构建返回结果
	embeddings := make([][]float64, len(texts))
	for _, item := range embeddingResp.Output.Embeddings {
		embeddings[item.TextIndex] = item.Embedding
	}

	return embeddings, nil
}
