package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	llmModel        model.ChatModel
	promptTemplate  string // 专门用于JD-简历匹配的Prompt模板
	fewShotExamples string // (可选) 专门用于JD-简历匹配的Few-shot示例
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
func NewLLMJobEvaluator(llmModel model.ChatModel, options ...LLMJobEvaluatorOption) *LLMJobEvaluator {
	evaluator := &LLMJobEvaluator{
		llmModel: llmModel,
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

// generatePromptTemplate 内部方法，生成JD-简历匹配的Prompt模板
func (e *LLMJobEvaluator) generatePromptTemplate() {
	e.promptTemplate = `你是一位极其资深的AI招聘专家，具备精准识别人岗匹配度的火眼金睛。你的任务是基于提供的【岗位描述】和【候选人简历】，进行深度对比分析，并给出细致的、有区分度的匹配度评估。

请严格按照以下JSON格式输出你的评估结果：
1.  "match_score": 整数 (0-100)，整体匹配度。
2.  "match_highlights": 字符串数组 (3-5项)，候选人与岗位高度匹配的关键亮点，**优先列出超出岗位基础要求的或特别突出的优势**。
3.  "potential_gaps": 字符串数组 (1-3项，可为空)，候选人相对于岗位的潜在不足、缺失或需进一步考察的方面。**即使整体匹配度高，也请指出可以提升或与"理想候选人"相比的细微差距。**
4.  "resume_summary_for_jd": 字符串 (不超过150字)，针对此JD的简历核心摘要，突出最相关的优势。

**JSON格式要求：**
- 输出必须是标准JSON格式，字段名请使用双引号。
- 如果字符串内容中包含双引号(")，必须用反斜杠进行转义，例如 "这是一个包含\"引号\"的字符串"
- 数组元素必须是合法的JSON值（如字符串、数字、布尔值、对象或嵌套数组）。
- 不要在JSON之外添加任何文本。

**评分核心原则与权重（请严格遵守）：**

*   **决定性因素 (不满足则分数不超过40分，甚至更低)：**
    *   岗位描述中明确的**硬性学历要求** (如：必须本科及以上)。
    *   岗位描述中明确的**特定毕业年份要求** (如：限2026届毕业生)。
    *   岗位描述中明确列出的**"必须具备/精通"的核心技能/经验** (例如，若JD强调"必须精通Go语言"，而简历完全未提及，则为重大缺陷)。
*   **高权重因素 (显著影响分数)：**
    *   **核心技能匹配度：** 简历中体现的技能与JD核心技能要求的吻合程度和深度。
    *   **相关工作/项目经验年限与质量：** 经验是否直接相关，项目规模、复杂度、个人职责。
    *   **与岗位职责的契合度：** 候选人过往经历是否能直接胜任JD描述的核心职责。
*   **中权重因素 (影响分数，但非决定性)：**
    *   JD中"熟悉"、"了解"级别的技能掌握情况。
    *   相关行业的经验。
    *   软技能（如沟通、团队合作，需从项目描述中间接判断）。
*   **低权重/加分项：**
    *   名校/名企背景（在核心能力满足前提下的加分）。
    *   与JD相关的证书、奖项。
    *   超出JD基础要求的额外技能或经验。

**评分参考区间（调整后，更强调区分度）：**
- 95-100分: 完美契合，所有决定性、高权重因素均优秀满足，且有显著加分项。凤毛麟角。
- 85-94分: 非常优秀，满足所有决定性因素，高权重因素大部分优秀，可能有少量中权重不足。重点推荐。
- 70-84分: 良好匹配，满足所有决定性因素，高权重因素基本满足，可能在中权重或部分高权重上有不足。值得考虑。
- 50-69分: 一般匹配，可能勉强满足决定性因素，或在多个高权重因素上有明显差距。需谨慎评估。
- 30-49分: 匹配度较低，一个或多个决定性因素不满足，或多个高权重因素严重不足。
- 0-29分: 基本不匹配。

【岗位描述】:
"""
%s
"""

【候选人简历】:
"""
%s
"""

请仔细评估，确保评分的区分度，并给出有价值的分析。输出必须是纯JSON，不要包含任何非JSON内容。**再次强调：如果任何字符串值（如亮点、不足、摘要）内部需要出现双引号，请务必使用反斜杠(\")进行转义，否则JSON将无法解析。**`
}

// generateFewShotExamples 内部方法，生成评估的少样本示例
func (e *LLMJobEvaluator) generateFewShotExamples() {
	e.fewShotExamples = `以下是一些岗位-简历匹配度评估的示例，请学习其中的评估逻辑和区分度：

示例1 (演示：决定性因素不符导致低分，即使其他方面尚可)
【岗位描述】:
"数据分析师（限2026届应届生）：本科及以上学历，数学、统计或计算机相关专业。熟悉Python、SQL进行数据分析和可视化。有相关实习经验者优先。"

【候选人简历】:
"李明，2023年毕业于某大学计算机专业，本科学历。两年工作经验，在A公司任数据分析师，熟练使用Python, SQL, Tableau。负责用户行为分析和报表制作。项目：电商用户流失预警模型。"

示例输出:
{
  "match_score": 35,
  "match_highlights": [
    "计算机专业背景，与岗位要求相符",
    "熟练掌握Python、SQL等数据分析工具，技能匹配",
    "拥有实际数据分析项目经验（用户流失预警模型）"
  ],
  "potential_gaps": [
    "【决定性因素不符】候选人已于2023年毕业并有两年工作经验，不符合岗位\"限2026届应届生\"的硬性要求",
    "虽然技能和经验相关，但毕业年份是硬性门槛"
  ],
  "resume_summary_for_jd": "李明具备计算机专业背景和相关数据分析技能及项目经验，但其毕业年份(2023年)与岗位明确要求的\"2026届应届生\"不符，此为决定性不匹配因素。"
}

示例2 (演示：高分但仍有细微差距，注重区分度)
【岗位描述】:
"高级前端工程师：负责公司核心产品的前端开发。要求5年以上前端开发经验，精通React，熟悉TypeScript，有大型SPA应用开发经验，了解性能优化，具备良好的团队协作能力。"

【候选人简历】:
"张伟，计算机科学学士，7年前端开发经验。精通React生态，熟练使用TypeScript，Redux等。主导过3个大型SPA项目，优化首屏加载时间减少40%。主持前端架构升级，组织技术分享会。"

示例输出:
{
  "match_score": 92,
  "match_highlights": [
    "7年前端开发经验，超出岗位要求的5年经验",
    "精通React生态，且有丰富的TypeScript实践经验",
    "主导过3个大型SPA项目，符合岗位\"核心要求\"",
    "有明确的性能优化实践经验，成功减少首屏加载时间40%",
    "具有团队协作和技术领导能力，组织技术分享会"
  ],
  "potential_gaps": [
    "简历中未详细说明CI/CD和自动化测试经验，这些在大型项目中很有价值",
    "可以更详细地描述对前端\"新技术\"的探索和学习情况"
  ],
  "resume_summary_for_jd": "张伟是一位经验丰富的前端工程师，拥有7年开发经验和扎实的React、TypeScript技术栈，有明确的大型SPA项目实践和性能优化成果，技术领导力突出，非常契合高级前端工程师岗位需求。"
}

示例3 (演示：两个候选人都不错，但能明确区分优劣)
【岗位描述】:
"Java后端开发工程师：负责公司电商系统的后端开发。需求3年以上Java开发经验，熟悉Spring Boot、微服务架构、MySQL优化，了解消息队列和缓存技术。有高并发系统经验优先。"

【候选人A简历】:
"王强，4年Java开发经验。熟练使用Spring Boot，参与开发过微服务电商系统，使用过MySQL、Redis、RabbitMQ。"

【候选人A评估结果】:
{
  "match_score": 78,
  "match_highlights": [
    "4年Java开发经验，符合岗位要求",
    "熟悉Spring Boot和微服务架构",
    "有电商系统开发经验，领域相关性强"
  ],
  "potential_gaps": [
    "简历中未明确展示MySQL优化的具体经验",
    "未提及\"高并发系统\"的实践经验",
    "简历过于简略，项目细节和个人贡献不够清晰"
  ],
  "resume_summary_for_jd": "王强拥有4年Java开发背景和电商系统经验，具备岗位要求的大部分技术栈，但在MySQL优化和高并发方面的经验不明确，整体匹配度良好。"
}

【候选人B简历】:
"刘芳，5年Java开发经验。精通Spring生态，设计并实现电商交易系统微服务改造，优化MySQL查询提升性能30%，使用Kafka处理订单峰值流量，Redis缓存减轻数据库压力，系统支持日均10万订单。"

【候选人B评估结果】:
{
  "match_score": 92,
  "match_highlights": [
    "5年Java开发经验，超出岗位要求",
    "精通Spring生态，有完整微服务架构实践",
    "有明确的MySQL优化经验，性能提升30%",
    "具备\"高并发系统\"经验，日均处理10万订单",
    "熟练运用Kafka和Redis解决实际业务问题"
  ],
  "potential_gaps": [
    "简历中未提及单元测试和CI/CD相关经验"
  ],
  "resume_summary_for_jd": "刘芳拥有5年Java开发经验，在电商微服务、数据库优化和高并发处理方面有丰富实践，技术栈完全匹配岗位需求，且有可量化的性能优化成果，是该职位的理想人选。"
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
	prompt := fmt.Sprintf(e.promptTemplate, jobDescriptionText, resumeText)

	// 添加Few-shot示例到prompt (如果存在且不为空)
	finalPrompt := prompt
	if e.fewShotExamples != "" {
		finalPrompt = fmt.Sprintf("%s\n\n%s", e.fewShotExamples, prompt)
	}

	// 2. 调用LLM
	systemMsg := einoschema.SystemMessage("你是一位资深的AI招聘助手，专注于分析岗位描述和候选人简历的匹配度。")
	userMsg := einoschema.UserMessage(finalPrompt)

	messages := []*einoschema.Message{systemMsg, userMsg}
	response, err := e.llmModel.Generate(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("LLMJobEvaluator: LLM call failed: %w", err)
	}

	if response == nil || response.Content == "" {
		return nil, fmt.Errorf("LLMJobEvaluator: LLM returned empty response")
	}

	// 3. 解析LLM响应
	jsonStr := extractJSONFromEvaluatorResponse(response.Content)
	if jsonStr == "" {
		return nil, fmt.Errorf("LLMJobEvaluator: failed to extract JSON from LLM response: %s", response.Content)
	}

	var evaluationResult LLMJobMatchEvaluation
	if err := json.Unmarshal([]byte(jsonStr), &evaluationResult); err != nil {
		return nil, fmt.Errorf("LLMJobEvaluator: failed to unmarshal LLM JSON response: %w. JSON string was: %s", err, jsonStr)
	}

	// 4. 验证结果
	if err := validateEvaluationResult(&evaluationResult); err != nil {
		return nil, fmt.Errorf("LLMJobEvaluator: invalid evaluation result: %w", err)
	}

	return &evaluationResult, nil
}

// EvaluateMatch 实现JobMatchEvaluator接口，评估简历与岗位的匹配度
// Note: This JobMatchEvaluation struct might need to be moved to a more generic place, e.g., a types package if used by other components.
type JobMatchEvaluation struct {
	MatchScore         int      `json:"match_score"`
	MatchHighlights    []string `json:"match_highlights"`
	PotentialGaps      []string `json:"potential_gaps"`
	ResumeSummaryForJD string   `json:"resume_summary_for_jd"`
	EvaluatedAt        int64    `json:"evaluated_at"` // Unix timestamp (seconds)
}

func (e *LLMJobEvaluator) EvaluateMatch(ctx context.Context, jobDescription string, resumeText string) (*JobMatchEvaluation, error) {
	// 调用内部评估方法
	result, err := e.Evaluate(ctx, jobDescription, resumeText)
	if err != nil {
		return nil, err
	}

	// 转换为通用评估结果
	return &JobMatchEvaluation{
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

	// 验证亮点数量 - 调整检查顺序和逻辑
	// 先检查特别高分但亮点太少的情况
	if result.MatchScore >= 85 && len(result.MatchHighlights) <= 2 {
		return fmt.Errorf("unusually few match_highlights (%d) for very high score (%d)",
			len(result.MatchHighlights), result.MatchScore)
	} else if result.MatchScore >= 70 && len(result.MatchHighlights) < 3 {
		// 再检查高分但亮点不足3个的情况
		return fmt.Errorf("match_highlights must have at least 3 items for high scores (score: %d, highlights: %d)",
			result.MatchScore, len(result.MatchHighlights))
	} else if result.MatchScore >= 50 && len(result.MatchHighlights) < 1 {
		// 再检查中分但亮点为空的情况
		return fmt.Errorf("match_highlights must have at least 1 item when score is %d or above", result.MatchScore)
	}
	// 对于50分以下的低分，允许亮点为空

	// 检查低分但没有不足的异常情况
	if result.MatchScore < 60 && len(result.PotentialGaps) == 0 {
		return fmt.Errorf("potential_gaps should not be empty for low scores (<60)")
	}

	// 验证亮点不超过5个
	if len(result.MatchHighlights) > 5 {
		return fmt.Errorf("match_highlights should not contain more than 5 items, got %d",
			len(result.MatchHighlights))
	}

	// 验证不足不超过3个
	if len(result.PotentialGaps) > 3 {
		return fmt.Errorf("potential_gaps should not contain more than 3 items, got %d",
			len(result.PotentialGaps))
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
				// 只提取出JSON字符串，不做处理
				return text[start : i+1]
			}
		}
	}
	return ""
}
