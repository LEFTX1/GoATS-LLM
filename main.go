package main

import (
	"context"
	"log"

	"ai-agent-go/pkg/agent" // Our custom AliyunQwenChatModel and ExampleTool

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

const (
	ALIYUN_QWEN_API_KEY = "sk-e0d7dda93eb14ff5bcbf43f484fe6736" // Replace with your actual key or load from env
)

func main() {
	dashscopeAPIKey := ALIYUN_QWEN_API_KEY

	if dashscopeAPIKey == "" {
		log.Fatal("错误: ALIYUN_QWEN_API_KEY 常量为空。请在 main.go 中设置它或从环境变量加载。")
	}
	log.Println("ALIYUN_QWEN_API_KEY (硬编码) 加载成功。")

	ctx := context.Background()

	// 1. 初始化 LLM 客户端 (阿里云通义千问)
	// Ensure your AliyunQwenChatModel implements eino's model.ChatModel interface correctly.
	llm, err := agent.NewAliyunQwenChatModel(dashscopeAPIKey, "qwen-plus", "") // Using qwen-plus
	if err != nil {
		log.Fatalf("创建 AliyunQwenChatModel 失败: %v", err)
	}
	log.Println("阿里云通义千问 LLM 客户端初始化完毕。")

	// 2. 初始化工具
	// Ensure your ExampleTool implements eino's tool.BaseTool and tool.InvokableTool interfaces.
	exampleTool := agent.NewExampleTool()
	log.Printf("工具初始化完毕: %s", exampleTool.Name)

	// 3. 配置 Eino ToolsNode
	toolsNodeCfg := compose.ToolsNodeConfig{
		Tools: []tool.BaseTool{exampleTool}, // Pass the tool instance
	}
	log.Println("Eino ToolsNodeConfig 配置完毕。")

	// 4. (可选) 定义 MessageModifier 来添加系统提示等
	msgModifier := func(ctxMod context.Context, input []*schema.Message) []*schema.Message {
		// Create a new slice with capacity for the system message and existing messages.
		res := make([]*schema.Message, 0, len(input)+1)
		// Add system message at the beginning.
		res = append(res, &schema.Message{Role: schema.RoleType("system"), Content: "你是一个天气查询助手。你需要思考如何使用工具来回答用户关于天气的多轮问题。"})
		// Append original messages.
		res = append(res, input...)
		log.Printf("[MessageModifier] 系统提示已添加。处理前 %d 条消息，处理后 %d 条消息。", len(input), len(res))
		return res
	}

	// 5. 创建 Eino React Agent
	reactAgent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: llm,          // Your ChatModel implementation
		ToolsConfig:      toolsNodeCfg, // Your tools configuration
		MessageModifier:  msgModifier,  // Optional: function to modify messages before calling LLM
		MaxStep:          12,           // Optional: Max steps for the agent. Default is usually okay.
		// ToolReturnDirectly: nil,      // Optional: map[string]struct{}{ "tool_name_to_return_directly": {}}
		// StreamToolCallChecker: nil, // Optional: custom function to check for tool calls in stream
	})
	if err != nil {
		log.Fatalf("创建 Eino React Agent 失败: %v", err)
	}
	log.Println("Eino React Agent 初始化完毕。")

	// 6. 运行 Agent
	userInput := "旧金山今天和明天的天气怎么样？还有上海后天呢？"
	log.Printf("向 Eino React Agent 发送用户输入: %s", userInput)

	// Eino React Agent's Generate method takes a slice of *schema.Message
	initialMessages := []*schema.Message{
		{Role: schema.RoleType("user"), Content: userInput},
	}

	// Call Generate on the reactAgent
	finalMessage, err := reactAgent.Generate(ctx, initialMessages)
	if err != nil {
		log.Fatalf("Eino React Agent 执行失败: %v", err)
	}

	log.Printf("Eino React Agent 执行成功。最终答案:")
	if finalMessage != nil {
		log.Printf("  Role: %s", finalMessage.Role)
		log.Printf("  Content: %s", finalMessage.Content)
		if len(finalMessage.ToolCalls) > 0 {
			// For a final answer from React agent, ToolCalls should ideally be empty.
			log.Printf("  警告: 最终消息中包含 %d 个工具调用 (通常不应出现)。", len(finalMessage.ToolCalls))
			for i, tc := range finalMessage.ToolCalls {
				log.Printf("    ToolCall %d: ID=%s, Type=%s, Function=%s, Args=%s", i, tc.ID, tc.Type, tc.Function.Name, tc.Function.Arguments)
			}
		}
	} else {
		// This case might occur if the agent logic doesn't produce a final message under some conditions.
		log.Println("最终消息为 nil。")
	}

	log.Println("----- Eino React Agent 执行结束 ----- Logging complete history via callbacks would be typical here if needed.")
}
