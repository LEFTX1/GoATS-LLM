package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	// OpenAI-compatible API endpoint for DashScope
	openAICompatibleQwenAPIURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	defaultQwenModelName       = "qwen-plus" // Changed to qwen-plus for better tool calling support
)

// --- OpenAI Compatible Structures ---

type OpenAIToolFunctionParamsProperty struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"` // For enum types
}

type OpenAIToolFunctionParams struct {
	Type       string                                      `json:"type"` // Typically "object"
	Properties map[string]OpenAIToolFunctionParamsProperty `json:"properties"`
	Required   []string                                    `json:"required,omitempty"`
}

type OpenAIFunction struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Parameters  OpenAIToolFunctionParams `json:"parameters"`
}

type OpenAITool struct {
	Type     string         `json:"type"` // Must be "function"
	Function OpenAIFunction `json:"function"`
}

// AliyunQwenChatModel 实现了 model.ChatModel 和 model.ToolCallingChatModel (通过添加 WithTools) 接口，
// 用于与阿里云通义千问模型交互。
type AliyunQwenChatModel struct {
	apiKey           string
	modelName        string
	apiURL           string
	httpClient       *http.Client
	boundOpenAITools []OpenAITool // Storing tools in OpenAI-compatible format
}

// NewAliyunQwenChatModel 创建一个新的 AliyunQwenChatModel 实例。
func NewAliyunQwenChatModel(apiKey string, modelName string, apiURL string) (*AliyunQwenChatModel, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("API 密钥不能为空")
	}

	mn := modelName
	if strings.TrimSpace(mn) == "" {
		mn = defaultQwenModelName
	}

	url := apiURL
	if strings.TrimSpace(url) == "" {
		url = openAICompatibleQwenAPIURL // Default to OpenAI-compatible URL
	}

	log.Printf("使用阿里云通义千问 LLM 客户端，API URL: %s, 模型: %s", url, mn)

	return &AliyunQwenChatModel{
		apiKey:           apiKey,
		modelName:        mn,
		apiURL:           url,
		httpClient:       &http.Client{},
		boundOpenAITools: make([]OpenAITool, 0),
	}, nil
}

// --- OpenAI Compatible Request/Response Structures ---

type OpenAIChatCompletionRequest struct {
	Model    string            `json:"model"`
	Messages []*schema.Message `json:"messages"` // Eino schema.Message is compatible enough for role/content
	Tools    []OpenAITool      `json:"tools,omitempty"`
	// ToolChoice interface{}       `json:"tool_choice,omitempty"` // Can be "none", "auto", or {"type": "function", "function": {"name": "my_function"}}
	// Add other parameters like temperature, stream, etc., if needed
}

type OpenAIMessage struct {
	Role      string               `json:"role"`
	Content   *string              `json:"content"` // Content can be null if tool_calls are present
	ToolCalls []OpenAIToolCallData `json:"tool_calls,omitempty"`
}

type OpenAIToolCallData struct {
	Id       string `json:"id"`
	Type     string `json:"type"` // Should be "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string of arguments
	} `json:"function"`
}

type OpenAIChatChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type OpenAICompletionResponse struct {
	Id      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []OpenAIChatChoice `json:"choices"`
	// Usage   OpenAIUsage        `json:"usage"` // Define if needed
}

// Generate 实现 model.ChatModel 接口
func (aq *AliyunQwenChatModel) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	// Process options if any. model.Option is likely a function type.
	// For this custom model, we assume core tool configuration happens via WithTools -> BindTools.
	// Other generic options might be handled by a more complex model config struct if needed.
	for _, opt := range options {
		// Example: if opt is func(cfg *MyModelConfig), then call opt(&aq.config)
		// As we don't have a MyModelConfig modified by generic options here, we just acknowledge them.
		_ = opt // Use opt to avoid unused variable error if not processed.
	}

	// Filter messages before sending to the API
	var llmMessagesForAPI []*schema.Message
	for i, currentMsg := range messages {
		if currentMsg.Role == schema.RoleType("tool") {
			// Check if this tool message should be sent based on the previous message.
			// It should only be sent if the preceding assistant message *actually* made a structured tool_call.
			if i > 0 {
				prevMsgInHistory := messages[i-1]
				if prevMsgInHistory.Role == schema.RoleType("assistant") && len(prevMsgInHistory.ToolCalls) == 0 {
					// The previous assistant message did NOT have structured tool calls.
					// This means the current 'tool' message likely resulted from ReAct text parsing.
					// Do not send this 'tool' message to the OpenAI API, as the observation
					// should already be in the ReAct prompt.
					log.Printf("[阿里云通义千问模型] 因前一条助手消息缺少 tool_calls，跳过 API 调用的工具消息 (ToolCallID: %s, Name: %s)。", currentMsg.ToolCallID, currentMsg.Name)
					continue // Skip adding this tool message
				}
			}
			// If prevMsgInHistory.Role == schema.RoleType("assistant") and len(prevMsgInHistory.ToolCalls) > 0,
			// then this tool message is a valid response to a structured tool call and should be sent.
			// Also, if i == 0 (tool message is the first message, highly unlikely) or prevMsgInHistory.Role is not schema.RoleType("assistant"),
			// we'll include it, though such scenarios are not typical for ReAct.
		}
		llmMessagesForAPI = append(llmMessagesForAPI, currentMsg)
	}

	reqPayload := OpenAIChatCompletionRequest{
		Model:    aq.modelName,
		Messages: llmMessagesForAPI, // Use the filtered list
	}

	if len(aq.boundOpenAITools) > 0 {
		reqPayload.Tools = aq.boundOpenAITools
		// Potentially set ToolChoice to "auto" or a specific tool if needed
		// reqPayload.ToolChoice = "auto"
	}

	jsonData, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("序列化请求体失败: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, aq.apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建 HTTP 请求失败: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+aq.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	log.Printf("[阿里云通义千问模型] 发送请求到 %s，模型 %s。请求体: %s", aq.apiURL, aq.modelName, string(jsonData))

	httpResp, err := aq.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("发送 HTTP 请求失败: %w", err)
	}
	defer httpResp.Body.Close()

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	log.Printf("[阿里云通义千问模型]收到响应: Status=%s, Body=%s", httpResp.Status, string(bodyBytes))

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API 请求失败，状态 %s: %s", httpResp.Status, string(bodyBytes))
	}

	var openAIResp OpenAICompletionResponse
	if err := json.Unmarshal(bodyBytes, &openAIResp); err != nil {
		return nil, fmt.Errorf("反序列化 API 响应失败: %w。响应体: %s", err, string(bodyBytes))
	}

	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("从 API 收到空选项: %s", string(bodyBytes))
	}

	apiMessage := openAIResp.Choices[0].Message
	responseContent := ""
	if apiMessage.Content != nil {
		responseContent = *apiMessage.Content
	}

	// Convert OpenAIMessage to schema.Message
	// Eino's schema.RoleType expects string literals like "system", "user", "assistant", "tool"
	// which match OpenAI's roles.
	resultMessage := &schema.Message{
		Role:    schema.RoleType(apiMessage.Role), // Directly use the role
		Content: responseContent,
	}

	if len(apiMessage.ToolCalls) > 0 {
		// Adjusting to value types for ToolCall and FunctionCall based on persistent linter errors for Eino v0.3.37
		resultMessage.ToolCalls = make([]schema.ToolCall, len(apiMessage.ToolCalls)) // Slice of values
		for i, tc := range apiMessage.ToolCalls {
			resultMessage.ToolCalls[i] = schema.ToolCall{ // Value assignment
				ID: tc.Id,
				Function: schema.FunctionCall{ // Value assignment
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}
		}
		// According to OpenAI spec, if tool_calls are present, content is often null or empty.
		// The ReActStepper's think method handles this by using content as thought.
	}

	// Ensure role is valid for Eino, though direct mapping should usually work.
	// For example, if OpenAI returns "function" as role for a function call response, map it if necessary.
	// But here, message.Role is "assistant" when tool_calls are made by the assistant.
	if resultMessage.Role == "" { // Fallback if role somehow empty
		resultMessage.Role = schema.RoleType("assistant") // Use string literal "assistant"
	}

	return resultMessage, nil
}

// Stream 实现 model.ChatModel 接口 (placeholder)
func (aq *AliyunQwenChatModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	log.Println("[阿里云通义千问模型] Stream 方法被调用，但尚未针对 OpenAI 兼容 API 完全实现。")
	return nil, fmt.Errorf("AliyunQwenChatModel (OpenAI 兼容) 的 Stream 方法未实现")
}

// BindTools 实现 model.ChatModel 接口
// 由于无法可靠地从 schema.ParamsOneOf 外部访问参数的详细信息，
// 我们暂时回退到为已知工具（如 get_weather）硬编码其 OpenAI 参数 schema。
// 理想情况下，Eino 的 schema.ParamsOneOf 应该提供一种方式来导出其内部参数映射。
func (aq *AliyunQwenChatModel) BindTools(tools []*schema.ToolInfo) error {
	aq.boundOpenAITools = make([]OpenAITool, 0, len(tools))
	for _, toolInfo := range tools {
		if toolInfo == nil {
			continue
		}

		var params OpenAIToolFunctionParams
		// Manually construct OpenAI function parameters schema based on toolInfo.Name
		switch toolInfo.Name {
		case "get_weather": // Hardcode parameters for get_weather
			params = OpenAIToolFunctionParams{
				Type: "object",
				Properties: map[string]OpenAIToolFunctionParamsProperty{
					"location": {Type: "string", Description: "要查询天气的地点，例如：北京，San Francisco"},
					"date":     {Type: "string", Description: "要查询的日期，例如：today, tomorrow, 2024-07-15。如果未指定，默认为 'today'。"},
				},
				Required: []string{"location"}, // 'location' is required as per tool.go
			}
		default:
			// For unknown tools or tools assumed to have no parameters if not handled above.
			// schema.ToolInfo.Desc is used for description.
			// If a tool has parameters but is not listed in the switch, the API call might be incomplete for that tool.
			log.Printf("[阿里云通义千问模型] 工具 '%s' 的参数 schema 未在 BindTools 中显式定义，将使用空对象。", toolInfo.Name)
			params = OpenAIToolFunctionParams{Type: "object", Properties: map[string]OpenAIToolFunctionParamsProperty{}}
		}

		aq.boundOpenAITools = append(aq.boundOpenAITools, OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        toolInfo.Name,
				Description: toolInfo.Desc, // Use description from ToolInfo
				Parameters:  params,
			},
		})
	}

	if len(aq.boundOpenAITools) > 0 {
		log.Printf("[阿里云通义千问模型] 已绑定 %d 个工具。第一个工具名称: %s", len(aq.boundOpenAITools), aq.boundOpenAITools[0].Function.Name)
	} else {
		log.Println("[阿里云通义千问模型] 未绑定任何工具。")
	}
	return nil
}

// WithTools 方法是为了满足 model.ToolCallingChatModel 接口（如果 Eino react.Agent 需要）。
// 它确保工具信息被模型内部处理（通过调用 BindTools）。
// react.Agent 可能会在调用 Generate/Stream 之前调用此方法。
// 返回 model.Option (nil 在这里表示此方法本身不添加额外的调用选项，因为工具已通过 BindTools 内部处理)。
func (aq *AliyunQwenChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	log.Printf("[阿里云通义千问模型] WithTools 方法被调用，包含 %d 个工具。将调用 BindTools 处理。", len(tools))
	err := aq.BindTools(tools) // Re-use the existing binding logic
	if err != nil {
		// Log the error, but the interface requires returning a model.Option.
		// Depending on how model.Option is defined, returning nil might be acceptable.
		log.Printf("[阿里云通义千问模型] WithTools 调用 BindTools 时出错: %v", err)
		return nil, err // Or an appropriate empty/error option if model.Option allows
	}
	// This specific implementation of WithTools doesn't produce a distinct model.Option itself,
	// as the tool binding is handled internally and reflected in aq.boundOpenAITools used by Generate.
	return aq, nil
}

var _ model.ChatModel = (*AliyunQwenChatModel)(nil)

// Ensure AliyunQwenChatModel also fulfills ToolCallingChatModel if WithTools is part of it.
// The linter for main.go will confirm this. If model.ToolCallingChatModel is not a real interface
// or WithTools is not its method, this explicit check might be problematic.
// For now, we assume ToolCallingChatModel is model.ChatModel plus WithTools based on the linter error.
var _ model.ToolCallingChatModel = (*AliyunQwenChatModel)(nil)

// Helper function to convert schema.Message role to OpenAI role string (if necessary, though they seem compatible)
func toOpenAIRole(role schema.RoleType) string {
	return string(role)
}

// Helper function to convert OpenAI role string to schema.Message role (if necessary)
func fromOpenAIRole(role string) schema.RoleType {
	return schema.RoleType(role)
}
