package agent

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/components/model"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ParsedLLMOutput 用于封装从 LLM 输出中解析出来的结构化信息
type ParsedLLMOutput struct {
	Thought           string           // LLM的思考过程
	Action            string           // LLM决定采取的动作 (工具名或 "Final Answer")
	ActionInput       string           // LLM为动作提供的输入 (JSON字符串或最终答案文本)
	FinalAnswer       string           // 如果 Action 是 "Final Answer"，这里是具体的答案内容
	RequiresAction    bool             // 是否需要执行动作 (即 Action 不是 "Final Answer")
	HasToolCall       bool             // LLM的响应是否包含结构化的ToolCall
	ToolCallToExecute *schema.ToolCall // 如果HasToolCall为true，这里是被选中的ToolCall
	RawOutput         string           // LLM原始响应内容, 用于记录和回退解析
}

// ReActStepper 实现了 BaseAgent的 Stepper 接口，用于执行ReAct逻辑。
type ReActStepper struct {
	AvailableTools     map[string]tool.InvokableTool
	currentAction      string
	currentActionInput string
	maxThoughtRetries  int
}

// NewReActStepper 创建一个新的 ReActStepper。
func NewReActStepper(tools map[string]tool.InvokableTool) *ReActStepper {
	return &ReActStepper{
		AvailableTools:    tools,
		maxThoughtRetries: 3,
	}
}

// Step 执行 ReAct 逻辑的一个步骤：思考，然后行动或给出最终答案。
func (rs *ReActStepper) Step(ctx context.Context, agent *BaseAgent) (string, error) {
	log.Printf("[ReActStepper] 步骤 %d: 开始思考...", agent.CurrentStep)

	currentHistory, err := agent.GetHistory()
	if err != nil {
		return "", fmt.Errorf("获取历史记录失败: %w", err)
	}

	parsedOutput, err := rs.think(ctx, agent, currentHistory)
	if err != nil {
		log.Printf("[ReActStepper] 步骤 %d: 思考失败: %v", agent.CurrentStep, err)
		return fmt.Sprintf("思考步骤出错: %v", err), nil
	}

	// 将LLM的思考/行动决策 (AssistantMessage) 添加到历史记录
	assistantMsgContent := parsedOutput.RawOutput
	if parsedOutput.HasToolCall && parsedOutput.ToolCallToExecute != nil {
		agent.AddMessage(&schema.Message{
			Role:      "assistant",
			Content:   parsedOutput.Thought,
			ToolCalls: []schema.ToolCall{*parsedOutput.ToolCallToExecute},
		})
		assistantMsgContent = parsedOutput.Thought
	} else if parsedOutput.RequiresAction {
		agent.AddMessage(&schema.Message{Role: "assistant", Content: parsedOutput.RawOutput})
	} else { // Final Answer
		agent.AddMessage(&schema.Message{Role: "assistant", Content: parsedOutput.FinalAnswer})
		assistantMsgContent = parsedOutput.FinalAnswer
	}
	log.Printf("[ReActStepper] 步骤 %d: LLM 响应 (Thought/Action/FinalAnswer): %s", agent.CurrentStep, assistantMsgContent)

	if parsedOutput.RequiresAction {
		log.Printf("[ReActStepper] 步骤 %d: LLM 决定行动: %s, 输入: %s, 是否有结构化工具调用: %t", agent.CurrentStep, parsedOutput.Action, parsedOutput.ActionInput, parsedOutput.HasToolCall)

		observation, errAct := rs.act(ctx, agent, parsedOutput.Action, parsedOutput.ActionInput, parsedOutput.ToolCallToExecute)
		if errAct != nil {
			log.Printf("[ReActStepper] 步骤 %d: 执行动作 '%s' 失败: %v", agent.CurrentStep, parsedOutput.Action, errAct)
			observation = fmt.Sprintf("执行工具 '%s' 时出错: %v", parsedOutput.Action, errAct)
		}
		log.Printf("[ReActStepper] 步骤 %d: 工具 '%s' 的观察结果: %s", agent.CurrentStep, parsedOutput.Action, observation)

		var toolMsg *schema.Message
		if parsedOutput.HasToolCall && parsedOutput.ToolCallToExecute != nil {
			toolMsg = &schema.Message{Role: "tool", ToolCallID: parsedOutput.ToolCallToExecute.ID, Content: observation}
		} else {
			toolMsg = &schema.Message{Role: "tool", ToolCallID: parsedOutput.Action, Content: observation}
		}
		agent.AddMessage(toolMsg)
		return observation, nil
	}

	log.Printf("[ReActStepper] 步骤 %d: LLM 给出最终答案: %s", agent.CurrentStep, parsedOutput.FinalAnswer)
	return parsedOutput.FinalAnswer, nil
}

// think 调用 LLM 获取思考和决策结果。
func (rs *ReActStepper) think(ctx context.Context, agent *BaseAgent, currentHistory []*schema.Message) (*ParsedLLMOutput, error) {
	promptString := rs.buildReActPrompt(ctx, currentHistory, agent.Name)

	var toolInfos []*schema.ToolInfo
	for _, executor := range rs.AvailableTools {
		toolInfo, err := executor.Info(ctx)
		if err == nil && toolInfo != nil {
			toolInfos = append(toolInfos, toolInfo)
		} else {
			// Try to get name from executor if toolInfo is nil or toolInfo.Name is empty
			name := "unknown"
			if toolInfo != nil && toolInfo.Name != "" {
				name = toolInfo.Name
			} else if invTool, ok := executor.(interface{ GetName() string }); ok { // Example: add a GetName to your tool impl if needed
				name = invTool.GetName()
			}
			log.Printf("[ReActStepper] 获取工具 %s 的信息失败: %v", name, err)
		}
	}

	// Corrected: Use promptString to form the message for LLM
	messagesForLLM := []*schema.Message{
		{Role: "user", Content: promptString},
	}

	llmResponse, err := agent.ChatClient.Generate(ctx, messagesForLLM, model.WithTools(toolInfos))
	if err != nil {
		return nil, fmt.Errorf("LLM Generate 调用失败: %w", err)
	}

	log.Printf("[ReActStepper] LLM 原始响应内容: %s", llmResponse.Content)
	if len(llmResponse.ToolCalls) > 0 {
		log.Printf("[ReActStepper] LLM 响应包含 %d 个结构化工具调用。第一个ToolCall ID: %s, Function: %s, Args: %s",
			len(llmResponse.ToolCalls), llmResponse.ToolCalls[0].ID, llmResponse.ToolCalls[0].Function.Name, llmResponse.ToolCalls[0].Function.Arguments)
	}

	// Re-introduced parseLLMOutput call
	parsedOutput, err := rs.parseLLMOutput(llmResponse)
	if err != nil {
		return nil, fmt.Errorf("解析LLM输出失败: %w", err)
	}
	parsedOutput.RawOutput = llmResponse.Content // Ensure RawOutput is set

	return parsedOutput, nil
}

// parseLLMOutput 解析LLM的响应，优先处理结构化ToolCalls，否则回退到文本解析。
func (rs *ReActStepper) parseLLMOutput(llmResponse *schema.Message) (*ParsedLLMOutput, error) {
	if len(llmResponse.ToolCalls) > 0 {
		log.Println("[ReActStepper] LLM 返回了结构化的工具调用。")
		tc := llmResponse.ToolCalls[0] //  ReAct 每次只执行一个动作

		// 验证工具是否存在
		if _, ok := rs.AvailableTools[tc.Function.Name]; !ok {
			log.Printf("[ReActStepper] 错误: LLM 请求执行未知工具 '%s' (结构化调用)。将尝试通过文本解析回退。", tc.Function.Name)
			// Fallback to text parsing if structured tool call points to an unknown tool
			// This is important because a malformed/hallucinated tool call should not stop the agent.
			return rs.parseReActTextOutput(llmResponse.Content)
		}

		parsedOutput := &ParsedLLMOutput{
			Thought:           strings.TrimSpace(llmResponse.Content), // Content (non-tool part) is considered thought
			Action:            tc.Function.Name,
			ActionInput:       tc.Function.Arguments, // Arguments are already a JSON string
			RequiresAction:    true,
			HasToolCall:       true,
			ToolCallToExecute: &tc,
			RawOutput:         llmResponse.Content,
		}
		log.Printf("[ReActStepper] 解析为结构化工具行动: Thought='%s', Action='%s', Input='%s'", parsedOutput.Thought, parsedOutput.Action, parsedOutput.ActionInput)
		return parsedOutput, nil
	}

	log.Println("[ReActStepper] LLM 未返回结构化的工具调用。尝试从LLM内容中解析 ReAct 文本。")
	return rs.parseReActTextOutput(llmResponse.Content)
}

// buildReActPrompt 根据历史记录和可用工具构建ReAct提示。
func (rs *ReActStepper) buildReActPrompt(ctx context.Context, historyWithSystemPrompt []*schema.Message, agentName string) string {
	promptBuilder := strings.Builder{}
	promptBuilder.WriteString(fmt.Sprintf("你是 %s，一个大型语言模型助手。你需要按步骤思考并决定是否使用工具来回答用户的问题。\n", agentName))
	promptBuilder.WriteString("按以下格式回复：\n")
	promptBuilder.WriteString("Thought: [这里是你的思考过程，一步一步地分析问题，以及你为什么选择某个工具或给出最终答案。]\n")
	promptBuilder.WriteString("Action: [如果你决定使用工具，这里是工具的名称。如果你要给出最终答案，这里是 \"Final Answer\"。]\n")
	promptBuilder.WriteString("Action Input: [如果你选择使用工具，这里是工具的输入参数（JSON格式或纯文本，取决于工具）。如果 Action 是 \"Final Answer\"，这里是最终答案的内容。]\n")
	promptBuilder.WriteString("Observation: [这是在你调用工具后，工具返回的结果。LLM请勿填写这里，由系统填充。]\n")
	promptBuilder.WriteString("Repeat Thought/Action/Action Input/Observation as needed.\n")

	if len(rs.AvailableTools) > 0 {
		promptBuilder.WriteString("\n你可以使用的工具有：\n")
		for name, toolInstance := range rs.AvailableTools { // Renamed tool to toolInstance for clarity
			info, err := toolInstance.Info(ctx)
			if err == nil && info != nil {
				promptBuilder.WriteString(fmt.Sprintf("- 工具名称: %s\n  描述: %s\n", name, info.Desc))
			} else {
				log.Printf("[ReActStepper] 构建提示时获取工具 %s 信息失败: %v", name, err)
				promptBuilder.WriteString(fmt.Sprintf("- 工具名称: %s (描述信息获取失败)\n", name))
			}
		}
		promptBuilder.WriteString("\n")
	}

	promptBuilder.WriteString("这是到目前为止的对话历史和你的思考过程：\n")
	for _, msg := range historyWithSystemPrompt {
		switch msg.Role {
		case "system":
			promptBuilder.WriteString(fmt.Sprintf("System: %s\n", msg.Content))
		case "user":
			promptBuilder.WriteString(fmt.Sprintf("User: %s\n", msg.Content))
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				promptBuilder.WriteString(fmt.Sprintf("Thought: %s\n", msg.Content))
				for _, tc := range msg.ToolCalls {
					promptBuilder.WriteString(fmt.Sprintf("Action: %s\nAction Input: %s\n", tc.Function.Name, tc.Function.Arguments))
				}
			} else {
				promptBuilder.WriteString(fmt.Sprintf("%s\n", msg.Content))
			}
		case "tool":
			promptBuilder.WriteString(fmt.Sprintf("Observation: %s\n", msg.Content))
		default:
			promptBuilder.WriteString(fmt.Sprintf("%s: %s\n", msg.Role, msg.Content))
		}
	}
	promptBuilder.WriteString("\nThought:")
	finalPrompt := promptBuilder.String()
	// Restored log line
	log.Printf("[ReActStepper] 构建的ReAct提示 (部分):\n%s\n[...历史记录可能很长...]\nThought:", strings.Split(finalPrompt, "Thought:")[0])
	return finalPrompt
}

var (
	thoughtRegex     = regexp.MustCompile(`(?is)Thought:\s*(.*?)(?:\nAction:|$)`)
	actionRegex      = regexp.MustCompile(`(?is)Action:\s*(.*?)(?:\nAction Input:|$)`)
	inputRegex       = regexp.MustCompile(`(?is)Action Input:\s*(.*)`)
	finalAnswerRegex = regexp.MustCompile(`(?is)Action:\s*Final Answer(?:[\s\n]*Action Input:\s*(.*))?`)
)

// parseReActTextOutput 从 LLM 的纯文本输出中解析 Thought, Action, 和 Action Input。
func (rs *ReActStepper) parseReActTextOutput(text string) (*ParsedLLMOutput, error) {
	log.Printf("[ReActStepper] 解析 ReAct 文本输出 (原始长度 %d): %s", len(text), truncateString(text, 500))
	text = strings.TrimSpace(text)

	output := ParsedLLMOutput{RawOutput: text} // Store raw text

	finalAnswerMatches := finalAnswerRegex.FindStringSubmatch(text)
	if len(finalAnswerMatches) > 0 {
		output.Action = "Final Answer"
		output.RequiresAction = false
		textBeforeFinalAnswerAction := strings.Split(text, "Action: Final Answer")[0]
		thoughtContentMatches := thoughtRegex.FindStringSubmatch(textBeforeFinalAnswerAction)
		if len(thoughtContentMatches) > 1 && strings.TrimSpace(thoughtContentMatches[1]) != "" {
			output.Thought = strings.TrimSpace(thoughtContentMatches[1])
		} else {
			trimmedTextBefore := strings.TrimSpace(textBeforeFinalAnswerAction)
			if trimmedTextBefore != "" {
				output.Thought = trimmedTextBefore
			}
		}
		if len(finalAnswerMatches) > 1 && strings.TrimSpace(finalAnswerMatches[1]) != "" {
			output.FinalAnswer = strings.TrimSpace(finalAnswerMatches[1])
			log.Printf("[ReActStepper] 从 Action Input 提取到 Final Answer: '%s'", output.FinalAnswer)
		} else {
			log.Println("[ReActStepper] 警告: Action: Final Answer 的 Action Input 为空或未捕获。")
			if output.Thought != "" {
				log.Println("[ReActStepper] Action Input 为空，使用解析到的 Thought 作为 Final Answer 内容。")
				output.FinalAnswer = output.Thought
			}
		}
		if output.FinalAnswer == "" {
			output.FinalAnswer = "(未能从LLM响应中提取明确的最终答案内容)"
			log.Println("[ReActStepper] 警告: 未能提取到明确的 Final Answer 内容。")
		}
		log.Printf("[ReActStepper] 解析为最终答案: Thought='%s', Action='%s', FinalAnswerContent='%s'", output.Thought, output.Action, output.FinalAnswer)
		return &output, nil
	}

	thoughtMatches := thoughtRegex.FindStringSubmatch(text)
	if len(thoughtMatches) > 1 {
		output.Thought = strings.TrimSpace(thoughtMatches[1])
	}

	actionMatches := actionRegex.FindStringSubmatch(text)
	if len(actionMatches) > 1 {
		output.Action = strings.TrimSpace(actionMatches[1])
		if strings.EqualFold(output.Action, "Final Answer") {
			log.Println("[ReActStepper] 在常规解析中发现 Action 为 'Final Answer'。")
			output.RequiresAction = false
			finalAnswerInputMatchesAgain := inputRegex.FindStringSubmatch(text)
			if len(finalAnswerInputMatchesAgain) > 1 && strings.TrimSpace(finalAnswerInputMatchesAgain[1]) != "" {
				output.FinalAnswer = strings.TrimSpace(finalAnswerInputMatchesAgain[1])
			} else if output.Thought != "" {
				log.Println("[ReActStepper] Final Answer 的 Action Input 为空（二次检查），使用 Thought 作为 Final Answer 内容。")
				output.FinalAnswer = output.Thought
			}
			if output.FinalAnswer == "" {
				output.FinalAnswer = "(未能从LLM响应中提取明确的最终答案)"
				log.Println("[ReActStepper] 警告: 在常规解析中发现 Final Answer，但未能提取其内容。")
			}
			log.Printf("[ReActStepper] 解析为最终答案 (二次检查): Thought='%s', Action='%s', FinalAnswerContent='%s'", output.Thought, output.Action, output.FinalAnswer)
			return &output, nil
		}
	} else {
		if output.Thought != "" {
			log.Printf("[ReActStepper] 警告: LLM 输出只有 Thought，没有 Action: '%s'。将其视为需要进一步思考或格式错误。", output.Thought)
		}
	}

	inputMatches := inputRegex.FindStringSubmatch(text)
	if len(inputMatches) > 1 {
		output.ActionInput = strings.TrimSpace(inputMatches[1])
	}

	if output.Action == "" {
		if output.Thought != "" {
			log.Printf("[ReActStepper] LLM 只提供了思考，没有行动。将其视为最终答案。Thought: %s", output.Thought)
			output.Action = "Final Answer"
			output.FinalAnswer = output.Thought
			output.RequiresAction = false
		} else {
			log.Printf("[ReActStepper] 错误: 未能从LLM输出中解析出有效的 Thought, Action 或 Final Answer: '%s'", text)
			return nil, fmt.Errorf("未能从LLM输出中解析出有效的 ReAct 组件: %s", text)
		}
	} else {
		output.RequiresAction = true
		if output.ActionInput == "" {
			log.Printf("[ReActStepper] 警告: 行动 '%s' 的 Action Input 为空。", output.Action)
		}
	}
	log.Printf("[ReActStepper] 解析为工具行动: Thought='%s', Action='%s', Input='%s'", output.Thought, output.Action, output.ActionInput)
	return &output, nil
}

// act 执行给定的动作并返回观察结果。
func (rs *ReActStepper) act(ctx context.Context, agent *BaseAgent, actionName string, actionInputJSONOrText string, toolCall *schema.ToolCall) (string, error) {
	rs.currentAction = actionName
	rs.currentActionInput = actionInputJSONOrText

	log.Printf("[ReActStepper] 准备执行动作: %s, 原始输入: %s", actionName, actionInputJSONOrText)

	toolToUse, ok := rs.AvailableTools[actionName]
	if !ok {
		log.Printf("[ReActStepper] 错误: 未找到名为 '%s' 的工具。可用工具: %v", actionName, rs.getAvailableToolNames())
		return fmt.Sprintf("错误：找不到名为 '%s' 的工具。", actionName), nil
	}

	toolInfo, err := toolToUse.Info(ctx)
	if err != nil {
		log.Printf("[ReActStepper] 获取工具 '%s' 信息失败: %v", actionName, err)
	}
	if toolInfo != nil {
		log.Printf("[ReActStepper] 执行工具: %s (描述: %s)", toolInfo.Name, toolInfo.Desc)
		if toolInfo.ParamsOneOf != nil {
			log.Printf("[ReActStepper] 工具 '%s' 具有参数定义 (ParamsOneOf is not nil)。", actionName)
		}
	}

	argumentsInJSON := actionInputJSONOrText
	if argumentsInJSON == "" && toolCall == nil {
		log.Printf("[ReActStepper] 工具 '%s' 的文本解析输入为空，将使用 '{}' 作为参数。", actionName)
		argumentsInJSON = "{}"
	} else if toolCall != nil {
		argumentsInJSON = toolCall.Function.Arguments
		log.Printf("[ReActStepper] 工具 '%s' 使用结构化调用的参数: %s", actionName, argumentsInJSON)
	}

	log.Printf("[ReActStepper] 调用工具 '%s' 的 InvokableRun，参数: %s", actionName, argumentsInJSON)
	toolOutput, err := toolToUse.InvokableRun(ctx, argumentsInJSON)
	if err != nil {
		log.Printf("[ReActStepper] 工具 '%s' 执行失败: %v", actionName, err)
		return fmt.Sprintf("工具 '%s' 执行时出错: %v", actionName, err), nil
	}

	log.Printf("[ReActStepper] 工具 '%s' 执行成功。", actionName)
	return string(toolOutput), nil
}

// getAvailableToolNames 返回一个包含所有可用工具名称的切片。
func (rs *ReActStepper) getAvailableToolNames() []string {
	names := make([]string, 0, len(rs.AvailableTools))
	for name := range rs.AvailableTools {
		names = append(names, name)
	}
	return names
}

// truncateString 是一个辅助函数，用于截断字符串以便记录日志。
func truncateString(s string, maxLen int) string {
	if len(s) > maxLen {
		// Ensure we don't cut in the middle of a multi-byte character
		// This is a simplified approach; a more robust solution would inspect runes.
		if maxLen > 3 {
			return s[:maxLen-3] + "..."
		}
		return s[:maxLen]
	}
	return s
}
