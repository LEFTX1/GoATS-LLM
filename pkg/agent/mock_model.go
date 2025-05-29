package agent

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// MockResponse 定义了 MockChatClient 的单次预期响应
type MockResponse struct {
	Content   string
	ToolCalls []*schema.ToolCall
	Error     error
}

// MockChatClient 是一个用于测试的 model.ChatModel 的模拟实现
type MockChatClient struct {
	// For single, repeatable response
	ExpectedResponse string
	ExpectedError    error

	// For sequential, different responses
	SequentialResponses []MockResponse
	ResponseIndex       int
	IsSequential        bool

	ReceivedMessages []*schema.Message
}

// NewMockChatClient 创建一个返回固定响应的 MockChatClient
func NewMockChatClient(expectedResponse string, expectedError error) *MockChatClient {
	return &MockChatClient{
		ExpectedResponse: expectedResponse,
		ExpectedError:    expectedError,
		IsSequential:     false,
		ReceivedMessages: make([]*schema.Message, 0),
	}
}

// NewMockChatClientSequential 创建一个按顺序返回不同响应的 MockChatClient
func NewMockChatClientSequential(responses []MockResponse) *MockChatClient {
	if len(responses) == 0 {
		// 为了避免panic，如果responses为空，则返回一个总是报错的客户端
		log.Println("[MockChatClient] Warning: NewMockChatClientSequential called with empty responses. Mock will always error.")
		return &MockChatClient{
			IsSequential:        true,
			SequentialResponses: []MockResponse{{Error: errors.New("mock client has no responses configured")}},
			ReceivedMessages:    make([]*schema.Message, 0),
		}
	}
	return &MockChatClient{
		SequentialResponses: responses,
		IsSequential:        true,
		ReceivedMessages:    make([]*schema.Message, 0),
	}
}

// Generate 模拟 LLM 的 Generate 方法
func (m *MockChatClient) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	log.Printf("[MockChatClient] Received Generate request with %d messages:", len(input))
	currentReceived := make([]*schema.Message, len(input))
	copy(currentReceived, input)
	m.ReceivedMessages = append(m.ReceivedMessages, currentReceived...) // 记录所有调用收到的消息

	for i, msg := range input {
		log.Printf("  Message %d: Role=%s, Content='%s', ToolCalls=%d", i+1, msg.Role, msg.Content, len(msg.ToolCalls))
	}

	if m.IsSequential {
		if m.ResponseIndex >= len(m.SequentialResponses) {
			log.Println("[MockChatClient] Error: No more sequential responses available.")
			finalError := errors.New("mock client has run out of sequential responses")
			return nil, finalError
		}
		resp := m.SequentialResponses[m.ResponseIndex]
		m.ResponseIndex++

		if resp.Error != nil {
			log.Printf("[MockChatClient] Sequential response %d: Returning predefined error: %v", m.ResponseIndex, resp.Error)
			return nil, resp.Error
		}
		// For Eino v0.3.37, it seems AssistantMessage might not take toolCalls directly in the way documented
		// for later versions. Passing nil for now to ensure compilation.
		responseMessage := schema.AssistantMessage(resp.Content, nil) // Forcing nil for toolCalls
		log.Printf("[MockChatClient] Sequential response %d: Returning: Content='%s' (ToolCalls omitted in history for now)", m.ResponseIndex, resp.Content)
		return responseMessage, nil
	}

	// Legacy single response behavior
	if m.ExpectedError != nil {
		log.Printf("[MockChatClient] Single response: Returning predefined error: %v", m.ExpectedError)
		return nil, m.ExpectedError
	}
	responseMessage := schema.AssistantMessage(m.ExpectedResponse, nil) // Assume no tool calls for simple mock
	log.Printf("[MockChatClient] Single response: Returning predefined response: '%s'", m.ExpectedResponse)
	return responseMessage, nil
}

// Stream 模拟 LLM 的 Stream 方法
func (m *MockChatClient) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	log.Printf("[MockChatClient] Received Stream request. Streaming not implemented in mock.")
	// 即使不支持stream，也记录一下收到的消息
	currentReceived := make([]*schema.Message, len(input))
	copy(currentReceived, input)
	m.ReceivedMessages = append(m.ReceivedMessages, currentReceived...)
	return nil, fmt.Errorf("streaming not implemented in MockChatClient")
}

// BindTools 模拟绑定工具的方法
func (m *MockChatClient) BindTools(tools []*schema.ToolInfo) error {
	log.Printf("[MockChatClient] BindTools called with %d tools. Mock does nothing useful with this yet.", len(tools))
	return nil
}

// GetReceivedMessages 返回所有调用中累积的已接收消息
func (m *MockChatClient) GetReceivedMessages() []*schema.Message {
	return m.ReceivedMessages
}
