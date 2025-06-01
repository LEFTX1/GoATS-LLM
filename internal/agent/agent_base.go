package agent

import (
	"context"
	"fmt"
	"log"

	"github.com/cloudwego/eino/components/model"

	"github.com/cloudwego/eino/schema"
)

// AgentState 表示代理的当前状态
type AgentState int

const (
	AgentStateIdle AgentState = iota
	AgentStateRunning
	AgentStateFinished
	AgentStateError
)

// String 方法使得 AgentState 可以被打印
func (s AgentState) String() string {
	switch s {
	case AgentStateIdle:
		return "IDLE"
	case AgentStateRunning:
		return "RUNNING"
	case AgentStateFinished:
		return "FINISHED"
	case AgentStateError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Stepper 定义了代理执行单步的接口
type Stepper interface {
	Step(ctx context.Context, agent *BaseAgent) (string, error)
}

// BaseAgent 是所有 Agent 的基础实现
type BaseAgent struct {
	Name           string
	SystemPrompt   string // Can be empty if not needed or handled by stepper/LLM client directly
	NextStepPrompt string
	State          AgentState
	CurrentStep    int
	MaxSteps       int
	ChatClient     model.ToolCallingChatModel
	Stepper        Stepper
	ChatMemory     ChatMemory
	SessionId      string
}

// NewBaseAgent 创建一个新的 BaseAgent
func NewBaseAgent(name string, systemPrompt string, maxSteps int, client model.ToolCallingChatModel, stepper Stepper, memory ChatMemory, sessionID string) *BaseAgent {
	if memory == nil {
		log.Println("[BaseAgent] Warning: ChatMemory is nil. Creating default InMemoryChatMemory.")
		memory = NewInMemoryChatMemory() // 提供一个默认的内存实现，以防传入nil
	}
	return &BaseAgent{
		Name:         name,
		SystemPrompt: systemPrompt,
		State:        AgentStateIdle,
		MaxSteps:     maxSteps,
		ChatClient:   client,
		Stepper:      stepper,
		ChatMemory:   memory,
		SessionId:    sessionID,
	}
}

// Run 执行代理的主要逻辑
func (ba *BaseAgent) Run(ctx context.Context, initialUserPrompt string) (string, error) {
	if ba.State == AgentStateRunning {
		return "", fmt.Errorf("代理 '%s' (会话: '%s') 已在运行中", ba.Name, ba.SessionId)
	}
	ba.State = AgentStateRunning
	log.Printf("代理 '%s' (会话: '%s'): 开始执行，最大步数 %d", ba.Name, ba.SessionId, ba.MaxSteps)

	// 添加初始用户提示到历史记录
	userMessage := schema.UserMessage(initialUserPrompt)
	err := ba.ChatMemory.AddMessage(ba.SessionId, userMessage)
	if err != nil {
		ba.State = AgentStateError
		return "", fmt.Errorf("代理 '%s' (会话: '%s'): 添加初始用户消息到内存失败: %w", ba.Name, ba.SessionId, err)
	}

	var finalResult string
	for i := 0; i < ba.MaxSteps; i++ {
		log.Printf("代理 '%s' (会话: '%s'): 执行步骤 %d/%d", ba.Name, ba.SessionId, i+1, ba.MaxSteps)
		stepOutput, err := ba.Stepper.Step(ctx, ba)
		if err != nil {
			ba.State = AgentStateError
			log.Printf("代理 '%s' (会话: '%s'): 步骤 %d 执行错误: %v", ba.Name, ba.SessionId, i+1, err)
			return "", fmt.Errorf("代理 '%s' (会话: '%s'): 步骤 %d 执行错误: %w", ba.Name, ba.SessionId, i+1, err)
		}

		finalResult += fmt.Sprintf("步骤 %d: %s\n", i+1, stepOutput) // 累积每一步的结果作为最终结果的一部分

		if ba.State == AgentStateFinished {
			log.Printf("代理 '%s' (会话: '%s') 在步骤 %d 完成。", ba.Name, ba.SessionId, i+1)
			// 如果 Stepper 将状态设置为 Finished，通常 stepOutput 已经是最终答案了
			// 此时 finalResult 可能包含了思考过程，这取决于 Stepper 的输出
			// 检查 stepOutput 是否可以直接作为最终答案返回，或者 finalResult 本身是否合适
			// 对于ReAct，stepOutput通常是最后一步的Thought+FinalAnswer
			// 这里我们假设，如果状态是Finished，stepOutput是需要返回给用户的最终内容。
			// 或者，如果stepOutput是像 "Final Answer: xxx" 这样的，我们也可以解析它。
			// 为了简单起见，如果 finished，我们直接使用最新的 stepOutput 作为最终答案。
			// 但 ReActStepper.Step 在 Final Answer 时返回的是包含 "Final Answer:" 的内容，由其调用者（本函数）处理。
			// 之前的逻辑是累加所有步骤的输出，包括思考过程。对于ReAct来说，这可能是合理的，因为它显示了推理链。
			// 我们将保留累积的 finalResult。如果 Stepper 有一个更明确的最终答案，它应该在 stepOutput 中返回它，
			// 并且 BaseAgent.Run 会在最后一次迭代中将其包含在 finalResult 中。
			break
		}
	}

	if ba.State != AgentStateFinished {
		log.Printf("代理 '%s' (会话: '%s') 在 %d 步后达到最大步数限制但未完成。", ba.Name, ba.SessionId, ba.MaxSteps)
		ba.State = AgentStateFinished // 即使是达到最大步数，也标记为完成
	}

	log.Printf("代理 '%s' (会话: '%s') 执行完毕并清理。", ba.Name, ba.SessionId)
	return finalResult, nil
}

// AddMessage 将消息添加到当前会话的聊天记录中
func (ba *BaseAgent) AddMessage(msg *schema.Message) error {
	return ba.ChatMemory.AddMessage(ba.SessionId, msg)
}

// GetHistory 获取当前会话的聊天记录
func (ba *BaseAgent) GetHistory() ([]*schema.Message, error) {
	return ba.ChatMemory.GetHistory(ba.SessionId)
}

// GetSystemPrompt 获取系统提示
func (ba *BaseAgent) GetSystemPrompt() string {
	return ba.SystemPrompt
}

// SetState 设置代理状态
func (ba *BaseAgent) SetState(state AgentState) {
	ba.State = state
}

// GetState 获取代理状态
func (ba *BaseAgent) GetState() AgentState {
	return ba.State
}
