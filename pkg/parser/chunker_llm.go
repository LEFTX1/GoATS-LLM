package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	einoschema "github.com/cloudwego/eino/schema"
)

// LLMResumeChunker 使用LLM进行更智能的简历分块和元信息提取
type LLMResumeChunker struct {
	// LLM模型接口
	llmModel model.ChatModel

	// 提取的字段配置
	extractFields []string

	// 章节配置
	sectionTypes []string

	// 提示词模板
	promptTemplate string

	// 少样本示例
	fewShotExamples string

	// 是否详细解析基本信息
	detailedBasicInfo bool
}

// LLM提取的简历结构
type LLMResumeStructure struct {
	// 基本信息
	BasicInfo map[string]interface{} `json:"basic_info"`

	// 章节列表
	Sections []struct {
		ChunkID          int    `json:"chunk_id"`
		ResumeIdentifier string `json:"resume_identifier"`
		Type             string `json:"type"`
		Title            string `json:"title"`
		Content          string `json:"content"`
	} `json:"chunks"`

	// 元数据分析结果
	Metadata struct {
		Is211       bool     `json:"is_211"`
		Is985       bool     `json:"is_985"`
		IsDoubleTop bool     `json:"is_double_top"`
		HasIntern   bool     `json:"has_intern"`
		Education   string   `json:"highest_education"`
		Experience  float64  `json:"years_of_experience"`
		Score       int      `json:"resume_score"`
		Tags        []string `json:"tags"`
	} `json:"metadata"`

	// 评估信息
	Evaluation map[string]interface{} `json:"evaluation,omitempty"`
}

// 创建新的LLM简历分块器
func NewLLMResumeChunker(llmModel model.ChatModel, options ...LLMChunkerOption) *LLMResumeChunker {
	chunker := &LLMResumeChunker{
		llmModel: llmModel,
		// 默认提取的基本信息字段
		extractFields: []string{
			"name", "email", "phone", "github", "location", "education_level", "position", "years_of_experience", "job_intention",
		},
		// 默认章节类型
		sectionTypes: []string{
			string(SectionBasicInfo), string(SectionEducation), string(SectionSkills),
			string(SectionProjects), string(SectionWorkExperience), string(SectionInternships),
			string(SectionAwards), string(SectionResearch), string(SectionPortfolio),
			string(SectionOtherExp), string(SectionPersonalIntro),
		},
		detailedBasicInfo: true,
	}

	// 应用选项
	for _, opt := range options {
		opt(chunker)
	}

	// 生成提示词模板和少样本示例
	chunker.generatePromptTemplate()
	chunker.generateFewShotExamples()

	return chunker
}

// LLMChunkerOption 是LLM分块器的配置选项
type LLMChunkerOption func(*LLMResumeChunker)

// WithExtractFields 设置要提取的字段
func WithExtractFields(fields []string) LLMChunkerOption {
	return func(c *LLMResumeChunker) {
		c.extractFields = fields
	}
}

// WithSectionTypes 设置要识别的章节类型
func WithSectionTypes(types []string) LLMChunkerOption {
	return func(c *LLMResumeChunker) {
		c.sectionTypes = types
	}
}

// WithDetailedBasicInfo 设置是否详细解析基本信息
func WithDetailedBasicInfo(detailed bool) LLMChunkerOption {
	return func(c *LLMResumeChunker) {
		c.detailedBasicInfo = detailed
	}
}

// WithCustomPromptTemplate 设置自定义提示词模板
func WithCustomPromptTemplate(template string) LLMChunkerOption {
	return func(c *LLMResumeChunker) {
		c.promptTemplate = template
	}
}

// WithCustomFewShotExamples 设置自定义少样本示例
func WithCustomFewShotExamples(examples string) LLMChunkerOption {
	return func(c *LLMResumeChunker) {
		c.fewShotExamples = examples
	}
}

// 生成提示词模板
func (c *LLMResumeChunker) generatePromptTemplate() {
	// 构建提取字段列表
	fieldsList := strings.Join(c.extractFields, "、")
	// 构建章节类型列表
	sectionsList := strings.Join(c.sectionTypes, "、")

	// 创建提示词模板
	// 注意：确保模板中有三个 %s 占位符，分别对应 fieldsList, sectionsList, 和 resumeText
	template := `你是一个专业的简历解析专家，专注于从非结构化简历文本中提取结构化信息，并按要求分割内容。

核心任务：
1. 提取基本信息：准确识别并提取候选人的核心个人信息。
2. 内容分块 (Chunking)：将简历按语义分割成不同的内容块(chunks)，如教育背景、专业技能、项目经验、实习经历、工作经验等。
3. 识别块类型与标题：为每个chunk确定类型 (type) 和原始标题 (title，如果存在)。
4. 分配ID与标识符：为每个chunk分配从1开始的唯一chunk_id，并生成resume_identifier (格式："姓名_电话号码")。
5. 分析元数据：根据提取的信息综合分析简历的元数据。
6. 输出标准JSON：严格按照指定的JSON格式输出结果。

基本信息字段提取指示：提取以下字段 -> %s

内容分块类型识别指示：识别并将内容归类到以下类型 -> %s

重要指令：
- 格式鲁棒性：简历文本可能无明显格式，请智能识别段落和信息边界。
- 信息缺失处理：若某信息项缺失，在basic_info或metadata中对应字段设为空字符串或null/0/false。resume_identifier中缺失部分留空 (如 "姓名_" 或 "_")。请勿编造信息。
- 学校背景判断：依据中国高校名单判断学校的211、985、双一流属性。
- 经验年限估算：根据实习、工作和项目经历，综合估算总经验年限（数字，如0.5, 1, 2.0）。
- 经验类型区分：明确区分项目经历(PROJECTS)、实习经历(INTERNSHIPS)和正式工作经验(WORK_EXPERIENCE)。将在校期间的课程项目、个人项目或竞赛项目归类为PROJECTS；将在校或毕业初期的短期、以学习为目的的工作实践归类为INTERNSHIPS；毕业后的全职、长期工作经历归类为WORK_EXPERIENCE。
- 技能信息处理：专业技能相关内容应完整保留在其独立的 "SKILLS" 类型chunk的content字段中，metadata中不包含技能列表。

JSON输出格式规范：
{
  "basic_info": {
    "name": "string",
    "phone": "string",
    "email": "string"
    // ... (LLM应根据"基本信息字段提取指示"动态填充此处未列出的其他字段)
  },
  "chunks": [
    {
      "chunk_id": 1,
      "resume_identifier": "string",
      "type": "string", // (LLM应从"内容分块类型识别指示"中选择)
      "title": "string",
      "content": "string"
    }
    // ... 更多 chunks
  ],
  "metadata": {
    "is_211": "boolean",
    "is_985": "boolean",
    "is_double_top": "boolean",
    "has_intern": "boolean",
    "highest_education": "string",
    "years_of_experience": "number",
    "resume_score": "integer",
    "tags": ["string"]
  }
}

待分析的简历文本如下：
"""
%s 
"""

请严格按照上述JSON格式规范输出，不要包含任何解释性文字或Markdown标记。确保JSON的完整性和可解析性。`

	// 填充模板
	c.promptTemplate = fmt.Sprintf(template, fieldsList, sectionsList) // 只填充前两个%s，第三个%s是为ChunkResume中的resumeText预留的

}

// 生成少样本示例
func (c *LLMResumeChunker) generateFewShotExamples() {
	// 目标：精简示例以减少token消耗，同时保留核心教学点。
	// 每个示例主要演示1-2个关键特性或LLM易错点。
	c.fewShotExamples = `以下是一些示例分析，请参考这些模式进行学习：

示例1 (演示：基本信息提取、单教育背景、简单技能和项目分块)
输入简历文本 (部分内容)：
"""
周小二18812345678zhou.xiaoer@example.com在读 意向：后端实习生教育经历XX工业大学 软件工程 本科 2022-至今专业技能熟悉Golang, Gin框架。了解MySQL。项目经历个人博客系统 后端开发 使用Go和Gin完成。
"""
输出：
{
  "basic_info": {
    "name": "周小二", "phone": "18812345678", "email": "zhou.xiaoer@example.com",
    "education_level": "本科", "job_intention": "后端/服务端实习生", "location": "", "github": "", "years_of_experience": 0
  },
  "chunks": [
    { "chunk_id": 1, "resume_identifier": "周小二_18812345678", "type": "EDUCATION", "title": "教育经历", "content": "XX工业大学 软件工程 本科 2022-至今" },
    { "chunk_id": 2, "resume_identifier": "周小二_18812345678", "type": "SKILLS", "title": "专业技能", "content": "熟悉Golang, Gin框架。了解MySQL。" },
    { "chunk_id": 3, "resume_identifier": "周小二_18812345678", "type": "PROJECTS", "title": "项目经历", "content": "个人博客系统 后端开发 使用Go和Gin完成。" }
  ],
  "metadata": {
    "is_211": false, "is_985": false, "is_double_top": false, "has_intern": false,
    "highest_education": "本科", "years_of_experience": 0, "resume_score": 70,
    "tags": ["在读学生", "后端"]
  }
}

示例2 (演示：实习经历(INTERNSHIPS)识别、内容揉合文本处理、学校背景判断)
输入简历文本 (部分内容，揉合)：
"""
李小花13987654321li.xiaohua@email.com应届 前端开发岗教育背景YY大学(985院校) 计算机科学 学士 2021-2025专业技能精通JS, React, Vue。熟悉CSS, HTML。实习经历ZZ科技 - 前端实习生 2024.03-2024.06 参与电商首页改版，用React。
"""
输出：
{
  "basic_info": {
    "name": "李小花", "phone": "13987654321", "email": "li.xiaohua@email.com",
    "education_level": "本科", "job_intention": "前端开发岗", "location": "", "github": "", "years_of_experience": 0.25
  },
  "chunks": [
    { "chunk_id": 1, "resume_identifier": "李小花_13987654321", "type": "EDUCATION", "title": "教育背景", "content": "YY大学(985院校) 计算机科学 学士 2021-2025" },
    { "chunk_id": 2, "resume_identifier": "李小花_13987654321", "type": "SKILLS", "title": "专业技能", "content": "精通JS, React, Vue。熟悉CSS, HTML。" },
    { "chunk_id": 3, "resume_identifier": "李小花_13987654321", "type": "INTERNSHIPS", "title": "实习经历", "content": "ZZ科技 - 前端实习生 2024.03-2024.06 参与电商首页改版，用React。" }
  ],
  "metadata": {
    "is_211": true, "is_985": true, "is_double_top": true, "has_intern": true,
    "highest_education": "本科", "years_of_experience": 0.25, "resume_score": 75,
    "tags": ["应届毕业生", "前端", "有实习", "985院校"]
  }
}

示例3 (演示：工作经验(WORK_EXPERIENCE)与实习经历(INTERNSHIPS)并存和区分、多年经验估算)
输入简历文本 (部分内容)：
"""
张大明13711223344zhang.daming@corp.com高级工程师 上海教育经验某大学 硕士 2015-2018工作经验AA公司 高级工程师 2020-至今 负责核心模块...成果显著...BB公司 软件工程师 2018-2020 参与项目X...实习经历CC公司 实习生 2017.07-2017.09 辅助开发...专业技能精通Java, Spring生态...
"""
输出：
{
  "basic_info": {
    "name": "张大明", "phone": "13711223344", "position": "高级工程师","email": "zhang.daming@corp.com",
    "education_level": "硕士", "job_intention": "高级工程师", "location": "上海", "github": "", "years_of_experience": 4.5
  },
  "chunks": [
    { "chunk_id": 1, "resume_identifier": "张大明_13711223344", "type": "EDUCATION", "title": "教育经验", "content": "某大学 硕士 2015-2018" },
    { "chunk_id": 2, "resume_identifier": "张大明_13711223344", "type": "WORK_EXPERIENCE", "title": "工作经验", "content": "AA公司 高级工程师 2020-至今 负责核心模块...成果显著...\nBB公司 软件工程师 2018-2020 参与项目X..." },
    { "chunk_id": 3, "resume_identifier": "张大明_13711223344", "type": "INTERNSHIPS", "title": "实习经历", "content": "CC公司 实习生 2017.07-2017.09 辅助开发..." },
    { "chunk_id": 4, "resume_identifier": "张大明_13711223344", "type": "SKILLS", "title": "专业技能", "content": "精通Java, Spring生态..." }
  ],
  "metadata": {
    "is_211": false, "is_985": false, "is_double_top": false, // 假设"某大学"不是这类学校
    "has_intern": true,
    "highest_education": "硕士", "years_of_experience": 4.5, // 根据工作经验估算
    "resume_score": 85,
    "tags": ["经验丰富", "高级工程师", "Java"]
  }
}`
}

// ChunkResume 使用LLM分块简历文本
func (c *LLMResumeChunker) ChunkResume(ctx context.Context, resumeText string) ([]*ResumeSection, map[string]string, error) {
	// 创建提示词
	prompt := fmt.Sprintf(c.promptTemplate, resumeText)

	// 添加少样本学习示例（如果有）
	if c.fewShotExamples != "" {
		prompt = fmt.Sprintf("%s\n\n%s", c.fewShotExamples, prompt)
	}

	// 调用LLM
	response, err := c.callLLM(ctx, prompt)
	if err != nil {
		return nil, nil, fmt.Errorf("LLM调用失败: %w", err)
	}

	// 解析LLM响应
	resumeStructure, err := c.parseResponse(response)
	if err != nil {
		return nil, nil, fmt.Errorf("解析LLM响应失败: %w", err)
	}

	// 转换为ResumeSection格式
	var sections []*ResumeSection
	for _, section := range resumeStructure.Sections {
		sections = append(sections, &ResumeSection{
			Type:    SectionType(section.Type),
			Title:   section.Title,
			Content: section.Content,
			// 增加分块ID和简历标识信息
			ChunkID:          section.ChunkID,
			ResumeIdentifier: section.ResumeIdentifier,
		})
	}

	// 将 map[string]interface{} 转换为 map[string]string
	basicInfo := make(map[string]string)
	for k, v := range resumeStructure.BasicInfo {
		// 根据值的类型进行转换
		switch val := v.(type) {
		case string:
			basicInfo[k] = val
		case float64:
			basicInfo[k] = fmt.Sprintf("%.1f", val) // 对于数字，格式化为带一位小数的字符串
		case int:
			basicInfo[k] = fmt.Sprintf("%d", val)
		case bool:
			if val {
				basicInfo[k] = "true"
			} else {
				basicInfo[k] = "false"
			}
		case nil:
			basicInfo[k] = ""
		default:
			// 对于其他类型，尝试使用JSON编码
			jsonVal, _ := json.Marshal(val)
			basicInfo[k] = string(jsonVal)
		}
	}

	// 添加元数据
	if resumeStructure.Metadata.Is211 {
		basicInfo["is_211"] = "true"
	}
	if resumeStructure.Metadata.Is985 {
		basicInfo["is_985"] = "true"
	}
	if resumeStructure.Metadata.IsDoubleTop {
		basicInfo["is_double_top"] = "true"
	}
	if resumeStructure.Metadata.HasIntern {
		basicInfo["has_intern"] = "true"
	}

	// 添加学历信息
	if resumeStructure.Metadata.Education != "" {
		basicInfo["highest_education"] = resumeStructure.Metadata.Education
	}

	// 添加经验年限
	if resumeStructure.Metadata.Experience > 0 {
		basicInfo["years_of_experience"] = fmt.Sprintf("%.1f", resumeStructure.Metadata.Experience)
	}

	// 添加标签
	if len(resumeStructure.Metadata.Tags) > 0 {
		basicInfo["tags"] = strings.Join(resumeStructure.Metadata.Tags, ",")
	}

	// 添加评分
	if resumeStructure.Metadata.Score > 0 {
		basicInfo["resume_score"] = fmt.Sprintf("%d", resumeStructure.Metadata.Score)
	}

	return sections, basicInfo, nil
}

// 调用LLM
func (c *LLMResumeChunker) callLLM(ctx context.Context, prompt string) (string, error) {
	// 使用Eino提供的辅助函数创建消息
	systemMsg := einoschema.SystemMessage("你是一个专业的简历解析专家，擅长从简历文本中提取结构化信息并输出标准JSON格式。")
	userMsg := einoschema.UserMessage(prompt)

	messages := []*einoschema.Message{systemMsg, userMsg}

	// 调用LLM
	response, err := c.llmModel.Generate(ctx, messages)
	if err != nil {
		return "", err
	}

	// 返回响应内容
	return response.Content, nil
}

// 解析LLM响应
func (c *LLMResumeChunker) parseResponse(response string) (*LLMResumeStructure, error) {
	// 提取JSON部分（防止LLM返回的不是纯JSON）
	jsonStr := extractJSON(response)
	if jsonStr == "" {
		return nil, fmt.Errorf("无法从LLM响应中提取有效的JSON")
	}

	// 解析JSON
	var result LLMResumeStructure
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("解析JSON失败: %w", err)
	}

	return &result, nil
}

// 从文本中提取JSON
func extractJSON(text string) string {
	// 尝试寻找 JSON 的开始和结束位置
	start := strings.Index(text, "{")
	if start == -1 {
		return ""
	}

	// 查找匹配的}
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

	return ""
}
