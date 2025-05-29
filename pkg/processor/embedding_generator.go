package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const (
	defaultAliyunEmbeddingAPIURL = "https://dashscope.aliyuncs.com/api/v1/services/embeddings/text-embedding/text-embedding"
	defaultAliyunEmbeddingModel  = "text-embedding-v1" // 请根据实际情况修改
)

// AliyunEmbeddingGenerator 使用阿里云服务生成文本嵌入向量
type AliyunEmbeddingGenerator struct {
	apiKey     string
	modelName  string
	apiURL     string
	httpClient *http.Client
}

// NewAliyunEmbeddingGenerator 创建一个新的 AliyunEmbeddingGenerator 实例
func NewAliyunEmbeddingGenerator(apiKey string, modelName string, apiURL string) (*AliyunEmbeddingGenerator, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("API key cannot be empty for AliyunEmbeddingGenerator")
	}

	mn := modelName
	if strings.TrimSpace(mn) == "" {
		mn = defaultAliyunEmbeddingModel
	}

	url := apiURL
	if strings.TrimSpace(url) == "" {
		url = defaultAliyunEmbeddingAPIURL
	}

	log.Printf("Using Aliyun Embedding Generator, API URL: %s, Model: %s", url, mn)

	return &AliyunEmbeddingGenerator{
		apiKey:     apiKey,
		modelName:  mn,
		apiURL:     url,
		httpClient: &http.Client{},
	}, nil
}

// EmbeddingRequestInput 定义了发送给阿里云嵌入服务API的输入结构
type EmbeddingRequestInput struct {
	Texts []string `json:"texts"`
}

// EmbeddingRequest 定义了发送给阿里云嵌入服务API的请求结构
type EmbeddingRequest struct {
	Model string                `json:"model"`
	Input EmbeddingRequestInput `json:"input"`
	// Parameters map[string]interface{} `json:"parameters,omitempty"` // 如果需要其他参数
}

// EmbeddingDetail 包含了单个文本的嵌入向量和文本索引
type EmbeddingDetail struct {
	TextIndex int       `json:"text_index"`
	Embedding []float32 `json:"embedding"` // 根据API响应调整类型，可能是[]float64
}

// EmbeddingOutput 定义了阿里云嵌入服务API的输出结构
type EmbeddingOutput struct {
	Embeddings []EmbeddingDetail `json:"embeddings"`
}

// EmbeddingResponse 定义了阿里云嵌入服务API的完整响应结构
type EmbeddingResponse struct {
	Output    EmbeddingOutput `json:"output"`
	Usage     map[string]int  `json:"usage"`
	RequestID string          `json:"request_id"`
}

// CreateEmbeddings 为一批文本生成嵌入向量
func (g *AliyunEmbeddingGenerator) CreateEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("input texts cannot be empty")
	}

	reqPayload := EmbeddingRequest{
		Model: g.modelName,
		Input: EmbeddingRequestInput{
			Texts: texts,
		},
	}

	jsonData, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize embedding request body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request for embedding: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+g.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	// Dashscope 可能需要 X-DashScope-SSE: enable 或 disable，取决于API特性
	// httpReq.Header.Set("X-DashScope-SSE", "disable")

	log.Printf("[AliyunEmbeddingGenerator] Sending request to %s, Model %s. Texts count: %d", g.apiURL, g.modelName, len(texts))

	httpResp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send HTTP request for embedding: %w", err)
	}
	defer httpResp.Body.Close()

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read embedding response body: %w", err)
	}

	log.Printf("[AliyunEmbeddingGenerator] Received response: Status=%s, Body (first 500 bytes)=%s", httpResp.Status, string(bodyBytes[:min(len(bodyBytes), 500)]))

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API request failed, status %s: %s", httpResp.Status, string(bodyBytes))
	}

	var embeddingResp EmbeddingResponse
	if err := json.Unmarshal(bodyBytes, &embeddingResp); err != nil {
		return nil, fmt.Errorf("failed to deserialize embedding API response: %w. Response body: %s", err, string(bodyBytes))
	}

	if len(embeddingResp.Output.Embeddings) == 0 {
		return nil, fmt.Errorf("received empty embeddings from API: %s", string(bodyBytes))
	}

	// 假设返回的 embeddings 与输入的 texts 顺序一致且数量相同
	// Dashscope API 通常会保证顺序和数量
	embeddings := make([][]float32, len(texts))
	for _, embDetail := range embeddingResp.Output.Embeddings {
		if embDetail.TextIndex >= 0 && embDetail.TextIndex < len(texts) {
			embeddings[embDetail.TextIndex] = embDetail.Embedding
		} else {
			log.Printf("Warning: received embedding with out-of-bounds text_index %d", embDetail.TextIndex)
		}
	}

	// 确保所有文本都有对应的嵌入向量
	for i, emb := range embeddings {
		if emb == nil {
			return nil, fmt.Errorf("missing embedding for text at index %d after processing API response", i)
		}
	}

	return embeddings, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
