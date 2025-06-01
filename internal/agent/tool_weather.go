package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ExampleTool 是一个示例工具，用于获取天气信息。
// 它现在实现了 eino 的 tool.BaseTool 和 tool.InvokableTool 接口。
type ExampleTool struct {
	Name string
}

// NewExampleTool 创建一个新的 ExampleTool。
func NewExampleTool() *ExampleTool {
	return &ExampleTool{
		Name: "get_weather",
	}
}

// Info 返回工具的元信息，符合 tool.BaseTool 接口。
func (t *ExampleTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.Name,
		Desc: "获取一个地点的指定日期的天气信息。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"location": {
				Type:     "string",
				Desc:     "要查询天气的地点，例如：北京，San Francisco",
				Required: true,
			},
			"date": {
				Type: "string",
				Desc: "要查询的日期，例如：today, tomorrow, 2024-07-15。如果未指定，默认为 'today'。",
			},
		}),
	}, nil
}

// InvokableRun 执行工具的逻辑，符合 tool.InvokableTool 接口。
func (t *ExampleTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	log.Printf("[示例工具] 开始执行工具 '%s'，原始输入 JSON: %s", t.Name, argumentsInJSON)

	var args struct {
		Location string `json:"location"`
		Date     string `json:"date,omitempty"`
	}

	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		log.Printf("[示例工具] 解析输入 JSON '%s' 失败: %v。将尝试直接使用原始输入作为位置。", argumentsInJSON, err)
		if argumentsInJSON != "" {
			args.Location = argumentsInJSON
			args.Date = "today"
		} else {
			return "", fmt.Errorf("工具 '%s' 的输入JSON解析失败且原始输入为空: %w", t.Name, err)
		}
	}

	log.Printf("[示例工具] 解析后的参数: Location=%s, Date=%s", args.Location, args.Date)

	location := strings.ToLower(strings.TrimSpace(args.Location))
	date := strings.TrimSpace(args.Date)
	if date == "" {
		date = "today"
	}

	var weather string
	switch location {
	case "california", "加州", "加利福尼亚", "san francisco", "旧金山":
		if date == "today" || date == "今天" {
			weather = "旧金山今天天气晴朗温暖。"
		} else if date == "tomorrow" || date == "明天" {
			weather = "旧金山明天预计多云，可能有微风。"
		} else {
			weather = fmt.Sprintf("旧金山在 %s 的天气是模拟数据：阳光明媚。", date)
		}
	case "shanghai", "上海", "上海市":
		if date == "tomorrow" || date == "明天" {
			weather = "上海明天预计有雨，请注意带伞。"
		} else if date == "today" || date == "今天" {
			weather = "上海今天多云转晴。"
		} else if date == "day after tomorrow" || date == "后天" {
			weather = "上海后天天气不错，晴间多云。"
		} else {
			weather = fmt.Sprintf("上海在 %s 的天气是模拟数据：可能有阵雨。", date)
		}
	default:
		weather = fmt.Sprintf("抱歉，我还没有 '%s' 在 '%s' 的天气信息。这是一个通用的模拟结果。", location, date)
	}

	result := fmt.Sprintf("工具 '%s' 已成功为地点 '%s' (%s) 执行。结果: %s", t.Name, args.Location, date, weather)
	log.Printf("[示例工具] 工具 '%s' 执行完毕，结果: %s", t.Name, result)
	return result, nil
}

// 确保 ExampleTool 实现了必要的接口 (编译时检查)
var _ tool.BaseTool = (*ExampleTool)(nil)
var _ tool.InvokableTool = (*ExampleTool)(nil)

/*
// 旧的 ToolInput, ToolOutput, ToolExecutor (已被移除)

// ToolInput 定义了工具的输入参数类型
// type ToolInput map[string]any

// ToolOutput 定义了工具的输出结果类型
// type ToolOutput string

// ToolExecutor 定义了工具执行器的接口
// type ToolExecutor interface {
// Execute(ctx context.Context, input ToolInput) (ToolOutput, error)
// GetInfo() *schema.ToolInfo
// }
*/
