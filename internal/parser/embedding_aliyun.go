package parser

import (
	"ai-agent-go/internal/config"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	// "github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/embedding"
)

/*
// AliyunEmbedderOptions defines options for the AliyunEmbedder's EmbedStrings method
type AliyunEmbedderOptions struct {
	*embedding.Options      // Embed common options
	RequestDimensions *int // Specific: allow overriding dimensions per call for OpenAI compatible API
}

// WithRequestDimensions sets the dimensions for the embedding request.
// This is an implementation-specific option for AliyunEmbedder.
func WithRequestDimensions(dim int) embedding.Option {
	// return embedding.WrapEmbeddingImplSpecificOptFn(func(o interface{}) { // Assuming WrapEmbeddingImplSpecificOptFn is unavailable
	// 	if opt, ok := o.(*AliyunEmbedderOptions); ok {
	// 		opt.RequestDimensions = &dim
	// 	}
	// })
	return func(o *embedding.Options) { // Fallback: Modify common options directly if possible, or log if not supported
		// This is a placeholder. Actual implementation depends on how cloudwego/eino handles unknown options.
		// For now, this custom option will not be usable if WrapEmbeddingImplSpecificOptFn is missing.
	}

}
*/

// AliyunEmbedder 实现 embedding.Embedder 接口
type AliyunEmbedder struct {
	apiKey     string
	model      string // Default model
	dimensions int    // Default dimensions
	httpClient *http.Client
	baseURL    string
}

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

// GetDimensions 返回嵌入器配置的维度 (This is a helper, not part of eino.Embedder)
func (a *AliyunEmbedder) GetDimensions() int {
	return a.dimensions
}

// AliyunOpenAIEmbeddingRequest AliyunEmbeddingRequest 阿里云Embedding请求结构 (OpenAI compatible)
type AliyunOpenAIEmbeddingRequest struct {
	Input          interface{} `json:"input"` // string or []string
	Model          string      `json:"model"`
	Dimensions     int         `json:"dimensions,omitempty"`      // Optional, for text-embedding-v3
	EncodingFormat string      `json:"encoding_format,omitempty"` // Optional, e.g., "float"
}

// AliyunOpenAIEmbeddingResponse AliyunEmbeddingResponse 阿里云Embedding响应结构 (OpenAI compatible)
type AliyunOpenAIEmbeddingResponse struct {
	Object string                  `json:"object"` // e.g., "list"
	Data   []AliyunOpenAIDataEntry `json:"data"`
	Model  string                  `json:"model"`
	Usage  AliyunOpenAIUsage       `json:"usage"`
	ID     string                  `json:"id,omitempty"`
	Error  *AliyunOpenAIError      `json:"error,omitempty"`
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

// EmbedStrings 将文本转换为向量, 实现 cloudwego/eino embedding.Embedder 接口
func (a *AliyunEmbedder) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	// 1. Handle options
	options := &embedding.Options{}
	embedding.GetCommonOptions(options, opts...)
	// embeddings.GetImplSpecificOptions(options, opts...) // Commented out as AliyunEmbedderOptions is commented out

	// 2. Get callback manager and prepare RunInfo (Commented out due to undefined functions)
	// cbManager := callbacks.ManagerFromContext(ctx)
	// runInfo := &callbacks.RunInfo{
	// 	Name: "AliyunEmbedder.EmbedStrings",
	// 	Type: "Embedding",
	// }

	// 3. Prepare CallbackInput (Commented out)
	effectiveModel := a.model
	if options.Model != nil && *options.Model != "" {
		effectiveModel = *options.Model
	}

	effectiveDimensions := a.dimensions
	// if options.RequestDimensions != nil { // From commented out AliyunEmbedderOptions
	// 	effectiveDimensions = *options.RequestDimensions
	// }

	// cbInput := &embedding.CallbackInput{
	// 	Texts: texts,
	// 	Config: &embedding.Config{
	// 		Model: effectiveModel,
	// 	},
	// 	Extra: make(map[string]any),
	// }
	// if effectiveDimensions > 0 {
	// 	cbInput.Extra["dimensions"] = effectiveDimensions
	// }

	// 4. OnStart callback (Commented out)
	// ctx = cbManager.OnStart(ctx, runInfo, cbInput)

	if len(texts) == 0 {
		outputEmbeddings := [][]float64{}
		// cbOutput := &embedding.CallbackOutput{ // (Commented out)
		// 	Embeddings: outputEmbeddings,
		// 	Config:     &embedding.Config{Model: effectiveModel},
		// 	TokenUsage: &embedding.TokenUsage{}, // No tokens used
		// 	Extra:      cbInput.Extra,
		// }
		// ctx = cbManager.OnEnd(ctx, runInfo, cbOutput) // (Commented out)
		return outputEmbeddings, nil
	}

	var inputBody interface{}
	if len(texts) == 1 {
		inputBody = texts[0]
	} else {
		inputBody = texts
	}

	reqBody := AliyunOpenAIEmbeddingRequest{
		Input: inputBody,
		Model: effectiveModel,
	}
	if effectiveDimensions > 0 {
		reqBody.Dimensions = effectiveDimensions
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		err = fmt.Errorf("序列化请求失败: %w", err)
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		err = fmt.Errorf("创建HTTP请求失败: %w", err)
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("发送HTTP请求失败: %w", err)
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("读取响应体失败: %w", err)
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		var apiError AliyunOpenAIError
		detailedError := fmt.Errorf("API调用失败, 状态码: %d, 响应: %s", resp.StatusCode, string(body))
		if json.Unmarshal(body, &apiError) == nil && apiError.Message != "" {
			detailedError = fmt.Errorf("API调用失败, 状态码: %d, 类型: %s, 错误: %s, Code: %s", resp.StatusCode, apiError.Type, apiError.Message, apiError.Code)
		}
		// ctx = cbManager.OnError(ctx, runInfo, detailedError) // (Commented out)
		return nil, detailedError
	}

	var embeddingResp AliyunOpenAIEmbeddingResponse
	if err := json.Unmarshal(body, &embeddingResp); err != nil {
		err = fmt.Errorf("解析OpenAI兼容响应失败: %w, 响应体: %s", err, string(body))
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}

	if embeddingResp.Error != nil && embeddingResp.Error.Message != "" {
		err = fmt.Errorf("嵌入API调用失败(响应内错误): 类型: %s, 错误: %s, Code: %s", embeddingResp.Error.Type, embeddingResp.Error.Message, embeddingResp.Error.Code)
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}

	if len(embeddingResp.Data) == 0 && len(texts) > 0 {
		err = fmt.Errorf("API响应不包含嵌入数据, 响应: %s", string(body))
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}
	if len(texts) > 0 && len(embeddingResp.Data) != len(texts) {
		err = fmt.Errorf("mismatched embedding count: input %d, output %d. Body: %s", len(texts), len(embeddingResp.Data), string(body))
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}

	outputEmbeddings := make([][]float64, len(texts))
	for i := range texts {
		if i < len(embeddingResp.Data) {
			entry := embeddingResp.Data[i]
			if entry.Index >= 0 && entry.Index < len(outputEmbeddings) {
				outputEmbeddings[entry.Index] = entry.Embedding
			} else {
				err = fmt.Errorf("嵌入数据索引 %d 超出范围 %d", entry.Index, len(outputEmbeddings)-1)
				// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
				return nil, err
			}
		}
	}

	for i, emb := range outputEmbeddings {
		if emb == nil && len(texts[i]) > 0 {
			err = fmt.Errorf("missing embedding for text at index %d: \"%s\"", i, texts[i])
			// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
			return nil, err
		}
	}

	// tokenUsage := &embedding.TokenUsage{ // (Commented out)
	// 	PromptTokens:     embeddingResp.Usage.PromptTokens,
	// 	TotalTokens:      embeddingResp.Usage.TotalTokens,
	// }

	// cbOutput := &embedding.CallbackOutput{ // (Commented out)
	// 	Embeddings: outputEmbeddings,
	// 	Config:     &embedding.Config{Model: effectiveModel},
	// 	TokenUsage: tokenUsage,
	// 	Extra:      cbInput.Extra,
	// }

	// ctx = cbManager.OnEnd(ctx, runInfo, cbOutput) // (Commented out)

	return outputEmbeddings, nil
}
