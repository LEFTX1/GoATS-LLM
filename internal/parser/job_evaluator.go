package parser

import (
	"ai-agent-go/internal/types"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	einoschema "github.com/cloudwego/eino/schema"
)

// LLMJobMatchEvaluation 定义LLM评估结果的结构体
type LLMJobMatchEvaluation struct {
	MatchScore         int      `json:"match_score"`
	MatchHighlights    []string `json:"match_highlights"`
	PotentialGaps      []string `json:"potential_gaps"`
	ResumeSummaryForJD string   `json:"resume_summary_for_jd"`
}

// LLMJobEvaluator 结构体 (封装LLM客户端和Prompt逻辑)
type LLMJobEvaluator struct {
	llmModel        model.ToolCallingChatModel
	promptTemplate  string      // 专门用于JD-简历匹配的Prompt模板
	fewShotExamples string      // (可选) 专门用于JD-简历匹配的Few-shot示例
	logger          *log.Logger // 新增 logger 字段
}

// LLMJobEvaluatorOption 是LLM评估器的配置选项
type LLMJobEvaluatorOption func(*LLMJobEvaluator)

// WithCustomPromptTemplate 设置自定义提示词模板
func WithCustomPromptTemplate(template string) LLMJobEvaluatorOption {
	return func(e *LLMJobEvaluator) {
		e.promptTemplate = template
	}
}

// WithFewShotExamples 设置少样本示例
func WithFewShotExamples(examples string) LLMJobEvaluatorOption {
	return func(e *LLMJobEvaluator) {
		e.fewShotExamples = examples
	}
}

// NewLLMJobEvaluator 创建一个新的评估器实例
func NewLLMJobEvaluator(llmModel model.ToolCallingChatModel, logger *log.Logger, options ...LLMJobEvaluatorOption) *LLMJobEvaluator {
	if logger == nil {
		logger = log.New(io.Discard, "", 0) // 默认使用丢弃型记录器
	}
	evaluator := &LLMJobEvaluator{
		llmModel: llmModel,
		logger:   logger, // 保存 logger
	}

	// 生成默认模板
	evaluator.generatePromptTemplate()

	// 应用选项
	for _, opt := range options {
		opt(evaluator)
	}

	// 生成少样本示例（如果没有自定义设置）
	if evaluator.fewShotExamples == "" {
		evaluator.generateFewShotExamples()
	}

	return evaluator
}

// job_evaluator.go
func (e *LLMJobEvaluator) generatePromptTemplate() {
	e.promptTemplate = `你是一位极其资深的AI招聘专家，具备精准识别人岗匹配度的火眼金睛。你的核心任务是基于下面提供的【岗位描述】和【候选人简历】（简历文本通常已包含基本的换行和分段结构），进行深度、细致的对比分析，并严格按照指定的JSON格式输出有区分度的匹配度评估。

**请严格遵循以下JSON输出格式规范：**
1.  **"match_score"**: 整数 (0-100)，反映整体匹配程度。
2.  **"match_highlights"**: 字符串数组 (建议3-5项，如果确实没有亮点，可以少于3项)，必须是候选人与岗位高度匹配的**具体关键点**。优先列出超出岗位基础要求或特别突出的优势。避免空泛描述。
3.  **"potential_gaps"**: 字符串数组 (建议1-3项，如果确实完美匹配，此数组可为空)，必须是候选人相对于岗位的**具体潜在不足**、与JD要求不符之处、或需要进一步考察的方面。即使整体匹配度高，也请尝试指出可提升点或与"理想候选人"的细微差距。
4.  **"resume_summary_for_jd"**: 字符串 (严格控制在150字以内)，针对此【岗位描述】生成的【候选人简历】核心摘要，务必突出与该岗位最相关的技能、经验和优势。

**JSON格式细节要求：**
- 完整输出必须是一个合法的JSON对象。
- 所有字段名和字符串值都必须使用双引号。
- 字符串值内部如果包含双引号(")，必须使用反斜杠(\\")进行转义。例如："description": "这是一个包含\\"引号\\"的示例字符串。"
- 数组元素必须是字符串。
- 禁止在JSON结构之外输出任何额外文本、解释或Markdown标记。
- 请再次确认：所有JSON字符串值内部的双引号都必须像这样转义：` + `"{\\"key\\": \\"value with \\\\\\\"inner quotes\\\\\\\"\\"}"` + `这样的形式。

**评分核心原则与权重（请务必严格遵守，以确保评估的专业性和一致性）：**

*   **一票否决项 (若不满足，match_score 通常应低于40分，甚至更低)：**
    *   【岗位描述】中明确的**硬性学历要求** (例如："必须硕士及以上")。
    *   【岗位描述】中明确的**特定毕业年份限制** (例如："仅限2026届毕业生")。
    *   【岗位描述】中明确声明的**"必须具备/精通"的核心技术或经验**，而简历完全缺失或严重不符 (例如，JD强调"必须精通Go语言5年以上"，简历只有1年Java经验)。
*   **高权重因素 (显著影响分数)：**
    *   **核心技能匹配度：** 简历中展现的技能与【岗位描述】核心技能要求的吻合程度、熟练度（精通/熟悉/了解）和实际应用经验。
    *   **相关工作/项目经验：** 经验的直接相关性、年限（是否满足JD要求）、项目的规模、复杂度、候选人在其中的具体职责和可量化成果。
    *   **岗位职责契合度：** 候选人过往经历是否能直接胜任【岗位描述】中的核心职责。
*   **中权重因素 (影响分数，但非决定性)：**
    *   【岗位描述】中"熟悉"、"了解"级别的技能掌握情况。
    *   相关行业背景知识或经验。
    *   软技能（如沟通、团队合作、学习能力，需从项目描述和成就中间接判断）。
*   **低权重/加分项 (在核心能力满足前提下)：**
    *   名校/名企背景。
    *   与岗位相关的权威证书、竞赛奖项。
    *   超出【岗位描述】基础要求的、且对岗位有价值的额外技能或经验。

**评分参考区间（目标是提供有区分度的评估）：**
- 95-100分: 完美候选人，所有关键要求均出色满足，并有显著亮点。
- 85-94分: 非常优秀，核心要求高度匹配，综合能力强。强烈推荐。
- 70-84分: 良好匹配，大部分核心要求满足，具备胜任潜力。值得面试。
- 50-69分: 一般匹配，部分核心要求满足，但存在一些明显差距或不足。需谨慎考虑。
- 30-49分: 匹配度较低，一项或多项决定性因素不符，或核心能力差距较大。
- 0-29分: 基本不匹配或完全不相关。

【岗位描述】:
"""
%s
"""

【候选人简历】:
"""
%s
"""

请基于以上所有指令，仔细评估并输出JSON结果。`
}

// job_evaluator.go

// generateFewShotExamples 内部方法，生成评估的少样本示例
func (e *LLMJobEvaluator) generateFewShotExamples() {
	e.fewShotExamples = `以下是一些岗位–简历匹配度评估的示例，请学习其中的评估逻辑和区分度：

示例1 (演示：简历中含有双引号，JSON 里需要正确转义)
【岗位描述】:
"市场运营专员：要求具备良好的沟通能力，能够撰写"创意文案"，并有项目策划经验。"

【候选人简历】:
"""
李华
市场运营 | 4 年工作经验

工作亮点:
- 曾主导"春季新品推广"活动，撰写了数篇富有"创意"的文案。
- 与设计团队密切配合，实现活动阅读量突破 50 万。
"""

示例输出:
{
  "match_score": 85,
  "match_highlights": [
    "4 年市场运营经验，符合岗位基础要求。",
    "简历中多次提到撰写\"创意\"文案，与 JD 要求高度契合，且在\"春季新品推广\"活动中效果显著。"
  ],
  "potential_gaps": [
    "简历中未提及具体的投放渠道或 KOL 合作经验，可进一步沟通。"
  ],
  "resume_summary_for_jd": "李华在市场推广方面有 4 年经验，尤其擅长撰写\"创意\"文案并带来显著流量增长，与岗位要求高度契合。"
}

示例2 (演示：决定性因素不符导致低分，即使其他方面尚可)
【岗位描述】:
"数据分析师（限2026届应届生）：本科及以上学历，数学、统计或计算机相关专业。熟悉Python、SQL进行数据分析和可视化。有相关实习经验者优先。"

【候选人简历】:
"""
李明
2023年毕业于某大学计算机专业，本科学历。

工作经验:
A公司 数据分析师 2023.07 - 至今
 - 负责用户行为分析和报表制作。
 - 使用Python, SQL, Tableau。

项目经历:
电商用户流失预警模型
 - 作为核心成员参与模型搭建与验证。
"""

示例输出:
{
  "match_score": 35,
  "match_highlights": [
    "计算机专业背景，与岗位专业要求相符。",
    "熟练掌握Python、SQL等数据分析工具，技能匹配。",
    "拥有实际数据分析项目经验（电商用户流失预警模型）。"
  ],
  "potential_gaps": [
    "【决定性因素不符】候选人已于2023年毕业并有两年工作经验，不符合岗位\"限2026届应届生\"的硬性要求。",
    "虽然技能和经验相关，但毕业年份是此岗位的关键筛选条件。"
  ],
  "resume_summary_for_jd": "李明具备计算机专业背景和相关数据分析技能及项目经验，但其2023年的毕业年份与岗位明确要求的\"2026届应届生\"不符，此为决定性不匹配因素。"
}

示例3 (演示：高分但仍有细微差距，注重区分度)
【岗位描述】:
"高级前端工程师：负责公司核心产品的前端开发。要求5年以上前端开发经验，精通React，熟悉TypeScript，有大型SPA应用开发经验，了解性能优化，具备良好的团队协作能力。"

【候选人简历】:
"""
张伟
计算机科学学士，7年前端开发经验。

核心技能:
 - 精通React生态系统 (React Hooks, Redux, React Router)。
 - 熟练使用TypeScript进行大型项目开发。
 - 熟悉Webpack, Babel等构建工具，了解Vite。
 - 深入理解前端性能优化方案。

工作经历:
某大型互联网公司 高级前端工程师 2018.05 - 至今
 - 主导过3个大型SPA项目（用户量百万级）的前端架构设计与核心模块开发。
 - 成功将某核心产品首屏加载时间从4.2秒优化至1.8秒。
 - 定期组织团队内技术分享会，推动前端技术栈升级。
"""

示例输出:
{
  "match_score": 92,
  "match_highlights": [
    "7年前端开发经验，超出岗位要求的5年，经验非常丰富。",
    "精通React生态和TypeScript，与岗位核心技术要求高度吻合。",
    "主导过3个大型SPA项目，证明了其大型应用开发与架构能力。",
    "有显著的性能优化成果（首屏加载时间减少40%+）。",
    "展现出良好的技术领导力和团队协作能力（组织技术分享，推动技术升级）。"
  ],
  "potential_gaps": [
    "简历中未明确提及CI/CD流程或自动化测试方面的经验，这在大型项目交付中通常很重要。",
    "对于新兴前端技术或框架的探索和应用可以更具体地阐述，以体现持续学习能力。"
  ],
  "resume_summary_for_jd": "张伟是一位经验非常丰富的高级前端工程师，其7年经验、精通React/TypeScript以及在大型SPA项目和性能优化方面的显著成就，使其与岗位要求高度契合。具备技术领导潜力。"
}

示例4 (演示：两个候选人都不错，但能明确区分优劣)
【岗位描述】:
"Java后端开发工程师：负责公司电商系统的后端开发。需求3年以上Java开发经验，熟悉Spring Boot、微服务架构、MySQL优化，了解消息队列和缓存技术。有高并发系统经验优先。"

【候选人A简历】:
"""
王强
4年Java开发经验。

专业技能:
熟练使用Spring Boot。
使用过MySQL、Redis、RabbitMQ。

项目经验:
参与开发过微服务电商系统。
负责订单模块部分接口开发。
"""

【候选人A评估结果】:
{
  "match_score": 78,
  "match_highlights": [
    "4年Java开发经验，满足岗位年限要求。",
    "熟练使用Spring Boot，并有微服务电商系统开发经验，与岗位技术方向一致。",
    "了解常用的中间件如Redis和RabbitMQ。"
  ],
  "potential_gaps": [
    "简历中未明确展示MySQL优化的具体案例或深度。",
    "未提及\"高并发系统\"相关的实践经验，这对于电商系统是一个加分项。",
    "项目描述较为简略，个人在项目中的具体贡献和挑战解决不够突出。"
  ],
  "resume_summary_for_jd": "王强拥有4年Java开发背景和电商系统经验，掌握岗位要求的大部分核心技术栈。在MySQL优化和高并发方面的经验尚不明确，整体匹配度良好，建议进一步沟通确认相关经验深度。"
}

【候选人B简历】:
"""
刘芳
5年Java开发经验。

教育背景:
某名牌大学 计算机硕士

专业技能:
精通Spring全家桶 (Boot, Cloud, Data)。
深入理解MySQL性能调优，有分库分表经验。
熟练使用Kafka进行消息解耦，Redis作为分布式缓存。

工作经历:
大型电商平台 高级Java工程师 2019.07 - 至今
 - 主导设计并实现电商交易核心系统的微服务架构改造。
 - 负责订单处理流程优化，将MySQL查询性能提升30%，系统支持日均10万订单。
 - 引入Kafka处理订单洪峰，使用Redis构建多级缓存体系，有效减轻数据库压力。
"""

【候选人B评估结果】:
{
  "match_score": 94,
  "match_highlights": [
    "5年Java开发经验，且有硕士学历背景，超出岗位基本要求。",
    "精通Spring生态，主导过电商交易系统微服务改造，经验高度相关且深入。",
    "有明确的MySQL优化经验和可量化的性能提升成果（提升30%）。",
    "具备高并发系统处理经验（日均10万订单），并熟练运用Kafka和Redis解决实际业务问题。",
    "技术栈与岗位要求高度吻合，并展现出架构设计和问题解决能力。"
  ],
  "potential_gaps": [
    "简历中未明确提及单元测试、集成测试或CI/CD方面的实践经验，这些对保障系统质量很重要。"
  ],
  "resume_summary_for_jd": "刘芳是一位经验丰富的Java后端工程师，拥有5年电商系统开发经验，在微服务架构、数据库优化和高并发处理方面有深厚的实践和显著成果。技术栈与岗位需求高度匹配，是该职位的理想人选。"
}
`
}

// Evaluate 函数执行JD与简历的匹配评估
func (e *LLMJobEvaluator) Evaluate(
	ctx context.Context,
	jobDescriptionText string,
	resumeText string,
) (*LLMJobMatchEvaluation, error) {
	if e.llmModel == nil {
		return nil, fmt.Errorf("LLMJobEvaluator: llmModel is not initialized")
	}
	if e.promptTemplate == "" {
		return nil, fmt.Errorf("LLMJobEvaluator: promptTemplate is not initialized")
	}

	// 1. 构建Prompt
	var sb strings.Builder // For user message

	// 构建核心的prompt内容 (user message)
	promptContent := fmt.Sprintf(e.promptTemplate, jobDescriptionText, resumeText)
	sb.WriteString(promptContent)
	userMsgContent := sb.String()

	// 2. 构建System Message
	systemBaseMessage := "你是一位资深的AI招聘助手，专注于分析岗位描述和候选人简历的匹配度。"
	finalSystemMessage := systemBaseMessage
	if e.fewShotExamples != "" {
		// 将 few-shot 示例添加到 system message
		var systemSb strings.Builder
		systemSb.WriteString(e.fewShotExamples)
		systemSb.WriteString("\n\n") // Separator
		systemSb.WriteString(systemBaseMessage)
		finalSystemMessage = systemSb.String()
	}

	// 3. 调用LLM
	systemMsg := einoschema.SystemMessage(finalSystemMessage)
	userMsg := einoschema.UserMessage(userMsgContent)

	messages := []*einoschema.Message{systemMsg, userMsg}

	e.logger.Printf("[LLMJobEvaluator] System Prompt (first 500 chars): %.500s", finalSystemMessage)
	e.logger.Printf("[LLMJobEvaluator] User Prompt (Job Description part, first 300 chars): %.300s", jobDescriptionText)
	e.logger.Printf("[LLMJobEvaluator] User Prompt (Resume Text part, first 300 chars): %.300s", resumeText)

	response, err := e.llmModel.Generate(ctx, messages)
	if err != nil {
		e.logger.Printf("[LLMJobEvaluator] LLM call error: %v", err)
		return nil, fmt.Errorf("LLMJobEvaluator: LLM call failed: %w", err)
	}

	if response == nil || response.Content == "" {
		e.logger.Printf("[LLMJobEvaluator] LLM returned empty response")
		return nil, fmt.Errorf("LLMJobEvaluator: LLM returned empty response")
	}
	e.logger.Printf("[LLMJobEvaluator] LLM Response: %s", response.Content)

	// Pre-process response.Content: remove BOM
	processedContent := strings.TrimPrefix(response.Content, "\uFEFF")

	// 4. 解析LLM响应
	jsonStr := extractJSONFromEvaluatorResponse(processedContent)
	if jsonStr == "" {
		return nil, fmt.Errorf("LLMJobEvaluator: failed to extract JSON from LLM response. Original content (after BOM strip): %s", processedContent)
	}

	// Ensure jsonStr is valid UTF-8, replacing invalid sequences with an empty string.
	if !utf8.ValidString(jsonStr) {
		jsonStr = strings.ToValidUTF8(jsonStr, "")
	}

	var evaluationResult LLMJobMatchEvaluation
	// ① 正常解析
	if err := json.Unmarshal([]byte(jsonStr), &evaluationResult); err != nil {
		// ② 解析失败 -> 自动修复再试一次
		fixedJsonStr := sanitizeJSON(jsonStr)
		if jsonErr := json.Unmarshal([]byte(fixedJsonStr), &evaluationResult); jsonErr != nil {
			// 如果修复后仍然失败，返回原始错误和原始 (清理BOM后) 及修复后的JSON字符串，方便排查
			return nil, fmt.Errorf("LLMJobEvaluator: failed to unmarshal LLM JSON response after attempting sanitization. Original error: %w. Sanitization error: %v. Original JSON string: %s. Sanitized JSON string: %s", err, jsonErr, jsonStr, fixedJsonStr)
		}
	}

	// 5. 验证结果
	if err := validateEvaluationResult(&evaluationResult); err != nil {
		return nil, fmt.Errorf("LLMJobEvaluator: invalid evaluation result: %w", err)
	}

	return &evaluationResult, nil
}

// EvaluateMatch 实现processor.JobMatchEvaluator接口，评估简历与岗位的匹配度
func (e *LLMJobEvaluator) EvaluateMatch(ctx context.Context, jobDescription string, resumeText string) (*types.JobMatchEvaluation, error) {
	// 调用内部评估方法
	result, err := e.Evaluate(ctx, jobDescription, resumeText)
	if err != nil {
		return nil, err
	}

	// 转换为通用评估结果
	return &types.JobMatchEvaluation{
		MatchScore:         result.MatchScore,
		MatchHighlights:    result.MatchHighlights,
		PotentialGaps:      result.PotentialGaps,
		ResumeSummaryForJD: result.ResumeSummaryForJD,
		EvaluatedAt:        time.Now().Unix(),
	}, nil
}

// validateEvaluationResult 验证评估结果是否符合要求
func validateEvaluationResult(result *LLMJobMatchEvaluation) error {
	// 验证分数范围
	if result.MatchScore < 0 || result.MatchScore > 100 {
		return fmt.Errorf("match_score must be between 0 and 100, got %d", result.MatchScore)
	}

	// 验证摘要长度
	if result.ResumeSummaryForJD == "" {
		return fmt.Errorf("resume_summary_for_jd must not be empty")
	}

	// 检查摘要长度（允许一定程度的超出）
	if len([]rune(result.ResumeSummaryForJD)) > 200 {
		return fmt.Errorf("resume_summary_for_jd is too long, got %d characters (max allowed: 200)",
			len([]rune(result.ResumeSummaryForJD)))
	}

	return nil
}

// extractJSONFromEvaluatorResponse 从文本中提取JSON字符串
func extractJSONFromEvaluatorResponse(text string) string {
	start := strings.Index(text, "{")
	if start == -1 {
		return ""
	}
	level := 0
	for i := start; i < len(text); i++ {
		if text[i] == '{' {
			level++
		} else if text[i] == '}' {
			level--
			if level == 0 {
				return text[start : i+1]
			}
		}
	}
	return "" // Should not happen if LLM output is somewhat structured
}

// sanitizeJSON 会遍历 src，将任何位于字符串字面量内部但并非"真正结束"的双引号写成 \,
// 以保证整个 JSON 在 Go 端能够正常反序列化。
// 它通过检查下一个非空白字符是否为 :, ], }, 或 , 来判断该 " 是否为字符串的结束。
// 反斜杠转义逻辑则正常处理 \\ 和 \"。
func sanitizeJSON(src string) string {
	var b strings.Builder
	inStr := false
	escaped := false

	for i := 0; i < len(src); i++ {
		c := src[i]

		if c == '"' && !escaped {
			if !inStr {
				// 遇到非转义的 "，并且当前不在字符串里 -> 开始一个新字符串
				inStr = true
				b.WriteByte(c)
			} else {
				// 当前在字符串里，检查这是不是字符串的真正结束
				j := i + 1
				// 跳过所有空白字符
				for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
					j++
				}
				// 如果下一个非空白字符是 JSON 语法里的 :, ], }, 或 ,，说明这才是真正的 string-end
				if j < len(src) && (src[j] == ':' || src[j] == ',' || src[j] == ']' || src[j] == '}') {
					inStr = false
					b.WriteByte(c)
				} else {
					// 否则认为这是字符串内部的 "，需要改成 \"
					b.WriteString("\\\"")
				}
			}
			escaped = false

		} else if c == '\\' && !escaped {
			// 遇到第一个反斜杠，标记 escaped，然后把它写进去
			escaped = true
			b.WriteByte(c)

		} else {
			// 普通字符，或者是被转义的字符
			b.WriteByte(c)
			escaped = false
		}
	}

	return b.String()
}
