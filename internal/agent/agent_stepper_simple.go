package agent

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/cloudwego/eino/schema"
)

// SimpleStepper 是一个简单的 Stepper 实现，它直接调用 LLM 并返回结果
type SimpleStepper struct{}

// NewSimpleStepper 创建一个新的 SimpleStepper 实例
func NewSimpleStepper() *SimpleStepper {
	return &SimpleStepper{}
}

// Step 实现 Stepper 接口
// 这个简单的实现直接使用 BaseAgent 的 ChatClient 来生成回复
func (s *SimpleStepper) Step(ctx context.Context, agent *BaseAgent) (string, error) {
	if agent.ChatClient == nil {
		return "", errors.New("chat client is not initialized in BaseAgent for SimpleStepper")
	}
	if agent.ChatMemory == nil {
		return "", errors.New("chat memory is not initialized in BaseAgent for SimpleStepper")
	}

	currentHistory, err := agent.GetHistory()
	if err != nil {
		return "", fmt.Errorf("SimpleStepper: failed to get chat history: %w", err)
	}

	messagesToLLM := make([]*schema.Message, 0, len(currentHistory)+1)
	if agent.SystemPrompt != "" {
		messagesToLLM = append(messagesToLLM, schema.SystemMessage(agent.SystemPrompt))
	}
	messagesToLLM = append(messagesToLLM, currentHistory...)

	resp, err := agent.ChatClient.Generate(ctx, messagesToLLM)
	if err != nil {
		return "", fmt.Errorf("SimpleStepper: LLM generation failed: %w", err)
	}

	assistantMsg := schema.AssistantMessage(resp.Content, nil)
	if err := agent.AddMessage(assistantMsg); err != nil {
		log.Printf("SimpleStepper: Warning - failed to add assistant message to chat memory for session %s: %v", agent.SessionId, err)
	}

	return resp.Content, nil
}
