package parser

import (
	"ai-agent-go/internal/config"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

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
	logger     *log.Logger // Added logger
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
		logger:     log.New(os.Stderr, "[AliyunEmbedder] ", log.LstdFlags|log.Lshortfile), // Initialize logger
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
	a.logger.Printf("EmbedStrings called with %d texts. First text (if any): %.100s...", len(texts), firstText(texts))

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
	a.logger.Printf("Effective model: %s, Effective dimensions: %d", effectiveModel, effectiveDimensions)

	if len(texts) == 0 {
		a.logger.Println("EmbedStrings: No texts to embed, returning empty.")
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

	// Log input text details
	if len(texts) == 1 {
		a.logger.Printf("[AliyunEmbedder] Embedding 1 text (first 100 chars): %.100s", texts[0])
	} else if len(texts) > 1 {
		a.logger.Printf("[AliyunEmbedder] Embedding %d texts. First text (first 100 chars): %.100s", len(texts), texts[0])
	}
	a.logger.Printf("[AliyunEmbedder] AliyunOpenAIEmbeddingRequest details: Model=%s, Dimensions=%d, InputType=%T", reqBody.Model, reqBody.Dimensions, reqBody.Input)

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		err = fmt.Errorf("序列化请求失败: %w", err)
		a.logger.Printf("[AliyunEmbedder] Error marshalling request: %v", err)
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}
	a.logger.Printf("[AliyunEmbedder] Marshalled JSON request: %s", string(jsonData))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		err = fmt.Errorf("创建HTTP请求失败: %w", err)
		a.logger.Printf("[AliyunEmbedder] Error creating HTTP request: %v", err)
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	a.logger.Printf("[AliyunEmbedder] Sending HTTP request to %s with Authorization Bearer token (length: %d)", a.baseURL, len(a.apiKey))

	resp, err := a.httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("发送HTTP请求失败: %w", err)
		a.logger.Printf("[AliyunEmbedder] Error sending HTTP request: %v", err)
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}
	defer resp.Body.Close()

	a.logger.Printf("[AliyunEmbedder] Received HTTP response. Status: %s, Code: %d", resp.Status, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("读取响应体失败: %w", err)
		a.logger.Printf("[AliyunEmbedder] Error reading response body: %v", err)
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}
	// a.logger.Printf("[AliyunEmbedder] Response body: %s", string(body)) // 旧的直接打印方式

	if resp.StatusCode != http.StatusOK {
		var apiError AliyunOpenAIError
		detailedError := fmt.Errorf("API调用失败, 状态码: %d, 响应: %s", resp.StatusCode, string(body))
		// 尝试从body中解析更详细的错误信息
		if json.Unmarshal(body, &apiError) == nil && apiError.Message != "" {
			detailedError = fmt.Errorf("API调用失败, 状态码: %d, 类型: %s, 错误: %s, Code: %s", resp.StatusCode, apiError.Type, apiError.Message, apiError.Code)
		} else {
			// 如果无法解析详细错误，或者解析出的错误信息为空，则也记录原始响应体
			a.logger.Printf("[AliyunEmbedder] API call failed. Raw response body: %s", string(body))
		}
		// ctx = cbManager.OnError(ctx, runInfo, detailedError) // (Commented out)
		a.logger.Printf("[AliyunEmbedder] API call failed: %v", detailedError)
		return nil, detailedError
	}

	// 如果状态码是 OK，则解析响应并记录（带截断的向量）
	var parsedResp AliyunOpenAIEmbeddingResponse
	if err := json.Unmarshal(body, &parsedResp); err != nil {
		err = fmt.Errorf("解析响应JSON失败: %w. Body: %s", err, string(body))
		a.logger.Printf("[AliyunEmbedder] Error unmarshalling response JSON: %v", err)
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}

	// 记录解析后的响应，向量将被截断打印
	logParsedResponseWithTruncatedEmbeddings(a.logger, &parsedResp)

	// 检查响应中是否包含API级别的错误 (例如，输入文本过多)
	if parsedResp.Error != nil && parsedResp.Error.Message != "" {
		err = fmt.Errorf("API返回错误: 类型=%s, 消息='%s', Code=%s", parsedResp.Error.Type, parsedResp.Error.Message, parsedResp.Error.Code)
		a.logger.Printf("[AliyunEmbedder] Parsed response contains API error: %v", err)
		// ctx = cbManager.OnError(ctx, runInfo, err) // (Commented out)
		return nil, err
	}

	// 从响应中提取嵌入向量
	outputEmbeddings := make([][]float64, len(parsedResp.Data))
	for i, dataEntry := range parsedResp.Data {
		outputEmbeddings[i] = dataEntry.Embedding
	}

	// 5. OnEnd callback (Commented out)
	// tokenUsage := &embedding.TokenUsage{
	// 	PromptTokens: int64(parsedResp.Usage.PromptTokens),
	// 	TotalTokens:  int64(parsedResp.Usage.TotalTokens),
	// }
	// cbOutput := &embedding.CallbackOutput{
	// 	Embeddings: outputEmbeddings,
	// 	Config:     &embedding.Config{Model: effectiveModel},
	// 	TokenUsage: tokenUsage,
	// 	Extra:      cbInput.Extra, // Preserve any extra data from input
	// }
	// ctx = cbManager.OnEnd(ctx, runInfo, cbOutput)
	a.logger.Printf("[AliyunEmbedder] Successfully embedded %d texts. First embedding dim (if any): %d. Prompt tokens: %d, Total tokens: %d",
		len(texts), firstEmbeddingDim(outputEmbeddings), parsedResp.Usage.PromptTokens, parsedResp.Usage.TotalTokens)

	return outputEmbeddings, nil
}

// Helper function to safely get the first text for logging
func firstText(texts []string) string {
	if len(texts) > 0 {
		return texts[0]
	}
	return ""
}

// Helper function to safely get the dimension of the first embedding for logging
func firstEmbeddingDim(embeddings [][]float64) int {
	if len(embeddings) > 0 && len(embeddings[0]) > 0 {
		return len(embeddings[0])
	}
	return 0
}

// logParsedResponseWithTruncatedEmbeddings 记录解析后的响应，并截断嵌入向量的打印
func logParsedResponseWithTruncatedEmbeddings(logger *log.Logger, resp *AliyunOpenAIEmbeddingResponse) {
	if resp == nil {
		logger.Printf("[AliyunEmbedder] Parsed response is nil")
		return
	}

	var embeddingsSummaries []string
	for i, entry := range resp.Data {
		summary := fmt.Sprintf("Entry %d: Index=%d, Object='%s', EmbeddingDim=%d",
			i, entry.Index, entry.Object, len(entry.Embedding))
		if len(entry.Embedding) > 0 {
			summary += fmt.Sprintf(", EmbeddingValuePreview=%s", truncateEmbedding(entry.Embedding))
		}
		embeddingsSummaries = append(embeddingsSummaries, summary)
	}

	logger.Printf("[AliyunEmbedder] Parsed Response: Object='%s', Model='%s', ID='%s', Usage={PromptTokens:%d, TotalTokens:%d}, DataEntries=%d. Data: [%s]",
		resp.Object, resp.Model, resp.ID, resp.Usage.PromptTokens, resp.Usage.TotalTokens, len(resp.Data), strings.Join(embeddingsSummaries, "; ")) // 使用 strings.Join

	if resp.Error != nil {
		logger.Printf("[AliyunEmbedder] Parsed Response Error: Message='%s', Type='%s', Param='%s', Code='%s'",
			resp.Error.Message, resp.Error.Type, resp.Error.Param, resp.Error.Code)
	}
}

// truncateEmbedding 截断嵌入向量的字符串表示形式
func truncateEmbedding(vector []float64) string {
	const maxLen = 6       // 如果向量长度大于此值，则截断
	const showEachSide = 3 // 截断时每边显示多少元素

	if len(vector) <= maxLen {
		return fmt.Sprintf("%v", vector)
	}

	var truncated []string
	for i := 0; i < showEachSide; i++ {
		truncated = append(truncated, fmt.Sprintf("%.4f", vector[i]))
	}
	truncated = append(truncated, "...")
	for i := len(vector) - showEachSide; i < len(vector); i++ {
		truncated = append(truncated, fmt.Sprintf("%.4f", vector[i]))
	}
	return fmt.Sprintf("[%s]", strings.Join(truncated, ", "))
}

// GetCallbackHandler (Commented out)
// func (a *AliyunEmbedder) GetCallbackHandler() callbacks.Handler {
// return nil
// }
