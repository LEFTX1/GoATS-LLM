package parser

import (
	"ai-agent-go/internal/types"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	einoschema "github.com/cloudwego/eino/schema"
)

// LLMResumeChunker 使用LLM进行更智能的简历分块和元信息提取
type LLMResumeChunker struct {
	// LLM模型接口
	llmModel model.ToolCallingChatModel

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

	logger *log.Logger
}

// LLMResumeStructure LLM提取的简历结构
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
		HasWorkExp  bool     `json:"has_work_exp"` // 新增：正式工作经验
		Education   string   `json:"highest_education"`
		Experience  float64  `json:"years_of_experience"`
		Score       int      `json:"resume_score"`
		Tags        []string `json:"tags"`

		// 新增算法奖项相关字段
		HasAlgorithmAward    bool     `json:"has_algorithm_award"`
		AlgorithmAwardTitles []string `json:"algorithm_award_titles"`

		// 新增编程比赛奖项相关字段
		HasProgrammingCompetitionAward bool     `json:"has_programming_competition_award"`
		ProgrammingCompetitionTitles   []string `json:"programming_competition_titles"`
	} `json:"metadata"`

	// 评估信息
	Evaluation map[string]interface{} `json:"evaluation,omitempty"`
}

// NewLLMResumeChunker 创建新的LLM简历分块器
func NewLLMResumeChunker(llmModel model.ToolCallingChatModel, logger *log.Logger, options ...LLMChunkerOption) *LLMResumeChunker {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}

	chunker := &LLMResumeChunker{
		llmModel: llmModel,
		logger:   logger,
		// 默认提取的基本信息字段
		extractFields: []string{
			"name", "email", "phone", "github", "location", "education_level", "position", "years_of_experience", "job_intention",
		},
		// 默认章节类型
		sectionTypes: []string{
			string(types.SectionBasicInfo), string(types.SectionEducation), string(types.SectionSkills),
			string(types.SectionProjects), string(types.SectionWorkExperience), string(types.SectionInternships),
			string(types.SectionAwards), string(types.SectionResearch), string(types.SectionPortfolio),
			string(types.SectionOtherExp), string(types.SectionPersonalIntro),
		},
		detailedBasicInfo: true,
	}

	// 应用选项
	for _, opt := range options {
		opt(chunker)
	}

	// 首先生成或确认 fewShotExamples，因为 promptTemplate 可能会用到它
	if chunker.fewShotExamples == "" {
		chunker.generateFewShotExamples()
	}

	// 然后生成或确认 promptTemplate
	if chunker.promptTemplate == "" {
		chunker.generatePromptTemplate()
	}

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

// WithCustomFewShotExamples 设置自定义少样本示例
func WithCustomFewShotExamples(examples string) LLMChunkerOption {
	return func(c *LLMResumeChunker) {
		c.fewShotExamples = examples
	}
}

// 生成提示词模板
func (c *LLMResumeChunker) generatePromptTemplate() {
	// 创建提示词模板
	// 注意：确保模板中有两个 %s 占位符，分别对应 fieldsList, sectionsList
	// 简历文本将通过 user message 传递
	baseTemplate := `你是一个专业的简历解析专家，专注于从【可能已包含基本换行和分段结构】的简历文本中提取结构化信息，并按要求分割内容。

核心任务：
1. 提取基本信息：准确识别并提取候选人的核心个人信息，通常位于简历开头，将其作为一个独立的 "BASIC_INFO" 类型的chunk。
2. 内容分块 (Chunking)：在已有的基本分段基础上，进一步将简历按语义分割成不同的内容块(chunks)，如教育背景、专业技能、项目经验、实习经历、工作经验等。每个明确的章节（如"教育经历"、"专业技能"等）应成为一个独立的chunk。
3. 识别块类型与标题：为每个chunk确定类型 (type) 和原始标题 (title，如果存在)。
4. 分配ID与标识符：为每个chunk分配从1开始的唯一chunk_id，并生成resume_identifier (格式："姓名_电话号码")。
5. 分析元数据：根据提取的信息综合分析简历的元数据，包括是否有算法奖项、编程比赛奖项、正式工作经验等。
6. 输出标准JSON：严格按照指定的JSON格式输出结果。

基本信息字段提取指示：提取以下字段 -> %s

内容分块类型识别指示：识别并将内容归类到以下类型 -> %s (除了BASIC_INFO，它在任务1中处理)

重要指令：
- 格式理解：简历文本通常已包含换行和空行作为段落分隔，请充分利用这些已有结构进行准确的语义边界识别。如果一段连续文本没有明显章节标题，但内容上属于某一类型（如一段个人简介），也应将其识别为一个chunk。
- 信息缺失处理：若某信息项缺失，在basic_info或metadata中对应字段设为空字符串或null/0/false。resume_identifier中缺失部分留空 (如 "姓名_" 或 "_")。请勿编造信息。
- 学校背景判断：请注意，双一流、211、985是独立的概念。双一流不一定是211，211不一定是985。请依据中国高校正确名单判断学校的属性。
- 经验年限估算：根据实习、工作和项目经历，综合估算总经验年限（数字，如0.5, 1, 2.0）。
- 经验类型区分：明确区分项目经历(PROJECTS)、实习经历(INTERNSHIPS)和正式工作经验(WORK_EXPERIENCE)。

【极其重要】实习和工作判断标准：
1. has_intern标志：必须同时满足以下两个条件才能设为true：
   a. 简历中明确出现"实习"相关内容 
   b. 该内容被解析为INTERNSHIPS类型的chunk
   如果没有INTERNSHIPS类型的chunk，has_intern必须设为false。

2. has_work_exp标志：必须同时满足以下两个条件才能设为true：
   a. 简历中明确出现"工作经验"相关内容
   b. 该内容被解析为WORK_EXPERIENCE类型的chunk
   如果没有WORK_EXPERIENCE类型的chunk，has_work_exp必须设为false。

3. 严禁推断：不得基于项目经历(PROJECTS)推断工作经验。无论项目描述如何专业或持续时间多长，如果没有明确标记为工作经验并创建WORK_EXPERIENCE类型的chunk，都必须将has_work_exp设为false。

- 奖项识别：仔细区分算法奖项（如ACM-ICPC、NOIP、NOI等）和编程竞赛奖项（如Kaggle、编程马拉松等）。
- 技能信息处理：专业技能相关内容应完整保留在其独立的 "SKILLS" 类型chunk的content字段中，metadata中不包含技能列表。

JSON输出格式规范：
{
  "basic_info": { 
    "name": "string",
    "phone": "string",
    "email": "string",
    "position": "string",
    "education_level": "string" 
  },
  "chunks": [
    {
      "chunk_id": 1,
      "resume_identifier": "string",
      "type": "BASIC_INFO",
      "title": "string",
      "content": "string"
    }
  ],
  "metadata": {
    "is_211": "boolean",
    "is_985": "boolean",
    "is_double_top": "boolean",
    "has_intern": "boolean",
    "has_work_exp": "boolean",
    "highest_education": "string",
    "years_of_experience": "number",
    "resume_score": "integer",
    "tags": ["string"],
    "has_algorithm_award": "boolean",
    "algorithm_award_titles": ["string"],
    "has_programming_competition_award": "boolean",
    "programming_competition_titles": ["string"]
  }
}

请严格按照上述JSON格式规范输出，不要包含任何解释性文字或Markdown标记。确保JSON的完整性和可解析性。
接下来，你将收到一份简历文本，请对其进行分析。`

	// 将字段列表和章节类型列表转换为逗号分隔的字符串
	fieldsStr := strings.Join(c.extractFields, ", ")
	sectionsStr := strings.Join(c.sectionTypes, ", ")

	// 使用这两个字符串作为格式化参数
	formattedTemplate := fmt.Sprintf(baseTemplate, fieldsStr, sectionsStr)

	if c.fewShotExamples != "" {
		c.promptTemplate = fmt.Sprintf("%s\n\n%s", c.fewShotExamples, formattedTemplate)
	} else {
		c.promptTemplate = formattedTemplate
	}
}

func (c *LLMResumeChunker) generateFewShotExamples() {
	c.fewShotExamples = `以下是一些示例分析，请参考这些模式进行学习：

示例1 (演示：BASIC_INFO提取，EDUCATION, SKILLS, PROJECTS基础分块，模拟Tika结构)
输入简历文本：
"""
王小明
13800001111 wang.xiaoming@example.com
上海市 软件工程师

教育背景
某大学 计算机科学 本科 2018-2022

专业技能
Java, Spring Boot, MySQL.
Python, Django.

项目经历
个人博客系统
 - 使用Java和Spring Boot开发。
"""
输出：
{
  "basic_info": {
    "name": "王小明", "phone": "13800001111", "email": "wang.xiaoming@example.com", 
    "location": "上海市", "position": "软件工程师", "github": "",
    "education_level": "本科", "job_intention": "软件工程师", "years_of_experience": 0 
  },
  "chunks": [
    { "chunk_id": 1, "resume_identifier": "王小明_13800001111", "type": "BASIC_INFO", "title": "基本信息", 
      "content": "王小明\n13800001111 wang.xiaoming@example.com\n上海市 软件工程师" },
    { "chunk_id": 2, "resume_identifier": "王小明_13800001111", "type": "EDUCATION", "title": "教育背景", 
      "content": "某大学 计算机科学 本科 2018-2022" },
    { "chunk_id": 3, "resume_identifier": "王小明_13800001111", "type": "SKILLS", "title": "专业技能",
      "content": "Java, Spring Boot, MySQL.\nPython, Django." },
    { "chunk_id": 4, "resume_identifier": "王小明_13800001111", "type": "PROJECTS", "title": "项目经历",
      "content": "个人博客系统\n - 使用Java和Spring Boot开发。" }
  ],
  "metadata": {
    "is_211": false, "is_985": false, "is_double_top": false, "has_intern": false,
    "has_work_exp": false,
    "highest_education": "本科", "years_of_experience": 0, "resume_score": 70,
    "tags": ["应届毕业生", "软件开发"],
    "has_algorithm_award": false, "algorithm_award_titles": [],
    "has_programming_competition_award": false, "programming_competition_titles": []
  }
}

示例2 (演示：INTERNSHIPS识别，PERSONAL_INTRO分块，学校元数据判断，模拟Tika结构)
输入简历文本：
"""
李小莉
13900002222 li.xiaoli@example.com
北京 应届硕士 意向：算法工程师

个人简介
对机器学习充满热情，乐于钻研算法原理。

教育背景
顶尖大学 (985, 211, 双一流) 人工智能 硕士 2023-2026 (预计)

实习经历
AI创新公司 算法实习生 2025.01 - 2025.03
 - 参与图像识别模型调优。
"""
输出：
{
  "basic_info": { 
    "name": "李小莉", "phone": "13900002222", "email": "li.xiaoli@example.com",
    "education_level": "硕士", "job_intention": "算法工程师", "location": "北京", "github": "", "position": "", "years_of_experience": 0.25 
  },
  "chunks": [
    { "chunk_id": 1, "resume_identifier": "李小莉_13900002222", "type": "BASIC_INFO", "title": "基本信息", 
      "content": "李小莉\n13900002222 li.xiaoli@example.com\n北京 应届硕士 意向：算法工程师" },
    { "chunk_id": 2, "resume_identifier": "李小莉_13900002222", "type": "PERSONAL_INTRO", "title": "个人简介",
      "content": "对机器学习充满热情，乐于钻研算法原理。" },
    { "chunk_id": 3, "resume_identifier": "李小莉_13900002222", "type": "EDUCATION", "title": "教育背景", 
      "content": "顶尖大学 (985, 211, 双一流) 人工智能 硕士 2023-2026 (预计)" },
    { "chunk_id": 4, "resume_identifier": "李小莉_13900002222", "type": "INTERNSHIPS", "title": "实习经历", 
      "content": "AI创新公司 算法实习生 2025.01 - 2025.03\n - 参与图像识别模型调优。" }
  ],
  "metadata": { 
    "is_211": true, "is_985": true, "is_double_top": true, "has_intern": true,
    "has_work_exp": false,
    "highest_education": "硕士", "years_of_experience": 0.25, "resume_score": 80,
    "tags": ["应届硕士", "算法", "有实习", "名校背景"],
    "has_algorithm_award": false, "algorithm_award_titles": [],
    "has_programming_competition_award": false, "programming_competition_titles": []
  }
}

示例3 (演示：WORK_EXPERIENCE与INTERNSHIPS并存和区分，AWARDS分块，模拟Tika结构)
输入简历文本：
"""
张大强
13700003333 zhang.daqiang@example.com
深圳 高级软件经理 8年经验

工作经验
牛X科技 高级经理 2019-至今
 - 带领团队负责核心产品线。
老牌公司 软件工程师 2016-2019
 - 开发与维护XX系统。

实习经历
初创企业 暑期实习 2015.07-2015.08
 - 参与产品原型设计。

获奖情况
2017年 公司年度优秀员工
"""
输出：
{
  "basic_info": { 
    "name": "张大强", "phone": "13700003333", "email": "zhang.daqiang@example.com", "position": "高级软件经理",
    "education_level": "", "job_intention": "高级软件经理", "location": "深圳", "github": "", "years_of_experience": 8 
  },
  "chunks": [
    { "chunk_id": 1, "resume_identifier": "张大强_13700003333", "type": "BASIC_INFO", "title": "基本信息", 
      "content": "张大强\n13700003333 zhang.daqiang@example.com\n深圳 高级软件经理 8年经验" },
    { "chunk_id": 2, "resume_identifier": "张大强_13700003333", "type": "WORK_EXPERIENCE", "title": "工作经验", 
      "content": "牛X科技 高级经理 2019-至今\n - 带领团队负责核心产品线。\n老牌公司 软件工程师 2016-2019\n - 开发与维护XX系统。" },
    { "chunk_id": 3, "resume_identifier": "张大强_13700003333", "type": "INTERNSHIPS", "title": "实习经历", 
      "content": "初创企业 暑期实习 2015.07-2015.08\n - 参与产品原型设计。" },
    { "chunk_id": 4, "resume_identifier": "张大强_13700003333", "type": "AWARDS", "title": "获奖情况",
      "content": "2017年 公司年度优秀员工" }
  ],
  "metadata": { 
    "is_211": false, "is_985": false, "is_double_top": false, 
    "has_intern": true, "has_work_exp": true,
    "highest_education": "", 
    "years_of_experience": 8, 
    "resume_score": 88,
    "tags": ["经验丰富", "管理经验", "技术管理"],
    "has_algorithm_award": false, "algorithm_award_titles": [],
    "has_programming_competition_award": false, "programming_competition_titles": []
  }
}

示例4 (演示：项目经历不应被误认为工作经验)
输入简历文本：
"""
周明慧
13888887777 zhou.minghui@example.com
杭州 算法工程师

教育经历
浙江大学 计算机科学 在读硕士 2023-至今

项目经历
大型分布式系统 2024.01-至今
 - 负责核心算法模块开发
 - 优化系统性能提升50%

图像识别API开发 2023.06-2023.12
 - 从零构建REST API服务
 - 部署和维护云服务架构
"""
输出：
{
  "basic_info": {
    "name": "周明慧", "phone": "13888887777", "email": "zhou.minghui@example.com",
    "location": "杭州", "position": "算法工程师", "github": "",
    "education_level": "硕士", "job_intention": "算法工程师", "years_of_experience": 1
  },
  "chunks": [
    { "chunk_id": 1, "resume_identifier": "周明慧_13888887777", "type": "BASIC_INFO", "title": "基本信息",
      "content": "周明慧\n13888887777 zhou.minghui@example.com\n杭州 算法工程师" },
    { "chunk_id": 2, "resume_identifier": "周明慧_13888887777", "type": "EDUCATION", "title": "教育经历",
      "content": "浙江大学 计算机科学 在读硕士 2023-至今" },
    { "chunk_id": 3, "resume_identifier": "周明慧_13888887777", "type": "PROJECTS", "title": "项目经历",
      "content": "大型分布式系统 2024.01-至今\n - 负责核心算法模块开发\n - 优化系统性能提升50%\n\n图像识别API开发 2023.06-2023.12\n - 从零构建REST API服务\n - 部署和维护云服务架构" }
  ],
  "metadata": {
    "is_211": true, "is_985": true, "is_double_top": true,
    "has_intern": false, "has_work_exp": false,
    "highest_education": "硕士", "years_of_experience": 1, "resume_score": 85,
    "tags": ["在读硕士", "算法", "分布式系统", "API开发"],
    "has_algorithm_award": false, "algorithm_award_titles": [],
    "has_programming_competition_award": false, "programming_competition_titles": []
  }
}

示例5 (演示：算法奖项和编程比赛奖项的识别与区分)
输入简历文本：
"""
张算法
13700004444 zhang.suanfa@example.com
北京 算法工程师

教育背景
清华大学 计算机科学 博士 2017-2022

获奖情况
2016年 ACM-ICPC 亚洲区域赛 银奖
2019年 Kaggle 图像识别竞赛 金牌
2020年 NOI 全国竞赛 一等奖

工作经验
人工智能公司 算法研究员 2022-至今
 - 负责深度学习模型优化
"""
输出：
{
  "basic_info": {
    "name": "张算法", "phone": "13700004444", "email": "zhang.suanfa@example.com",
    "location": "北京", "position": "算法工程师", "github": "",
    "education_level": "博士", "job_intention": "算法工程师", "years_of_experience": 1
  },
  "chunks": [
    { "chunk_id": 1, "resume_identifier": "张算法_13700004444", "type": "BASIC_INFO", "title": "基本信息",
      "content": "张算法\n13700004444 zhang.suanfa@example.com\n北京 算法工程师" },
    { "chunk_id": 2, "resume_identifier": "张算法_13700004444", "type": "EDUCATION", "title": "教育背景",
      "content": "清华大学 计算机科学 博士 2017-2022" },
    { "chunk_id": 3, "resume_identifier": "张算法_13700004444", "type": "AWARDS", "title": "获奖情况",
      "content": "2016年 ACM-ICPC 亚洲区域赛 银奖\n2019年 Kaggle 图像识别竞赛 金牌\n2020年 NOI 全国竞赛 一等奖" },
    { "chunk_id": 4, "resume_identifier": "张算法_13700004444", "type": "WORK_EXPERIENCE", "title": "工作经验",
      "content": "人工智能公司 算法研究员 2022-至今\n - 负责深度学习模型优化" }
  ],
  "metadata": {
    "is_211": true, "is_985": true, "is_double_top": true,
    "has_intern": false, "has_work_exp": true,
    "highest_education": "博士", "years_of_experience": 1, "resume_score": 95,
    "tags": ["算法", "深度学习", "比赛获奖", "名校博士"],
    "has_algorithm_award": true, "algorithm_award_titles": ["ACM-ICPC 亚洲区域赛 银奖", "NOI 全国竞赛 一等奖"],
    "has_programming_competition_award": true, "programming_competition_titles": ["Kaggle 图像识别竞赛 金牌"]
  }
}`
}

// ChunkResume 使用LLM解析简历文本，提取章节和基本信息
// 返回: 解析出的章节列表, 基本信息map, 错误
func (c *LLMResumeChunker) ChunkResume(ctx context.Context, text string) ([]*types.ResumeSection, map[string]string, error) {
	// 构建提取字段列表
	fieldsList := strings.Join(c.extractFields, "、")
	// 构建章节类型列表
	sectionsList := strings.Join(c.sectionTypes, "、")

	// System prompt 现在是 c.promptTemplate 格式化后的结果，它已经包含了 few-shot 示例和任务描述
	systemPromptContent := fmt.Sprintf(c.promptTemplate, fieldsList, sectionsList)

	// User prompt 就是实际的简历文本
	userPromptContent := text

	// 调用LLM
	response, err := c.callLLM(ctx, systemPromptContent, userPromptContent)
	if err != nil {
		return nil, nil, fmt.Errorf("LLM调用失败: %w", err)
	}

	// 解析LLM响应
	resumeStructure, err := c.parseResponse(response)
	if err != nil {
		return nil, nil, fmt.Errorf("解析LLM响应失败: %w", err)
	}

	// 转换为ResumeSection格式
	var sections []*types.ResumeSection
	for _, section := range resumeStructure.Sections {
		sections = append(sections, &types.ResumeSection{
			Type:    types.SectionType(section.Type),
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
	meta := resumeStructure.Metadata

	// 原有字段
	if meta.Is211 {
		basicInfo["is_211"] = "true"
	}
	if meta.Is985 {
		basicInfo["is_985"] = "true"
	}
	if meta.IsDoubleTop {
		basicInfo["is_double_top"] = "true"
	}
	if meta.HasIntern {
		basicInfo["has_intern"] = "true"
	}

	// 新增字段
	if meta.HasWorkExp {
		basicInfo["has_work_exp"] = "true"
	}

	if meta.HasAlgorithmAward {
		basicInfo["has_algorithm_award"] = "true"
		if len(meta.AlgorithmAwardTitles) > 0 {
			awardsJSON, _ := json.Marshal(meta.AlgorithmAwardTitles)
			basicInfo["algorithm_award_titles"] = string(awardsJSON)
		}
	}

	if meta.HasProgrammingCompetitionAward {
		basicInfo["has_programming_competition_award"] = "true"
		if len(meta.ProgrammingCompetitionTitles) > 0 {
			titlesJSON, _ := json.Marshal(meta.ProgrammingCompetitionTitles)
			basicInfo["programming_competition_titles"] = string(titlesJSON)
		}
	}

	// 添加学历信息
	if meta.Education != "" {
		basicInfo["highest_education"] = meta.Education
	}

	// 添加经验年限
	if meta.Experience > 0 {
		basicInfo["years_of_experience"] = fmt.Sprintf("%.1f", meta.Experience)
	}

	// 添加标签
	if len(meta.Tags) > 0 {
		basicInfo["tags"] = strings.Join(meta.Tags, ",")
	}

	// 添加评分
	if meta.Score > 0 {
		basicInfo["resume_score"] = fmt.Sprintf("%d", meta.Score)
	}

	return sections, basicInfo, nil
}

// callLLM 调用LLM处理提示词
func (c *LLMResumeChunker) callLLM(ctx context.Context, systemContent string, userContent string) (string, error) {
	// 创建消息列表，包含系统提示和用户提示
	messages := []*einoschema.Message{
		{Role: "system", Content: systemContent}, // 合并后的系统提示
		{Role: "user", Content: userContent},     // 实际的简历文本
	}

	// 设置最大重试次数
	maxRetries := 2
	retryDelay := 2 * time.Second

	var response *einoschema.Message
	var err error

	c.logger.Printf("[LLMResumeChunker] System Prompt: %.50s...", systemContent)
	c.logger.Printf("[LLMResumeChunker] User Prompt: %.50s...", userContent)

	// 重试逻辑
	for retry := 0; retry <= maxRetries; retry++ {
		// 如果是重试，则先检查上下文是否已取消
		if retry > 0 {
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("上下文已取消: %w", ctx.Err())
			case <-time.After(retryDelay):
				// 增加退避时间
				retryDelay *= 2
				c.logger.Printf("重试LLM调用 (第%d次)", retry)
			}
		}

		// 创建带超时的上下文，继承上游的取消信号
		callCtx, cancel := context.WithTimeout(ctx, 60*time.Second) // 继承上游的ctx

		// 调用LLM
		response, err = c.llmModel.Generate(callCtx, messages)
		cancel() // 释放子上下文

		if err == nil {
			break // 调用成功，退出重试循环
		}

		// 判断是否应该重试
		if !isRetryableError(err) || retry >= maxRetries {
			c.logger.Printf("[LLMResumeChunker] LLM call final error after retries: %v", err)
			return "", fmt.Errorf("LLM Generate failed: %w", err)
		}
	}

	c.logger.Printf("[LLMResumeChunker] LLM Response: %.50s", response.Content)
	// 返回响应内容
	return response.Content, nil
}

// isRetryableError 判断错误是否应该重试
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// 检查常见的可重试错误
	errStr := err.Error()
	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "no such host")
}

// 解析LLM响应
func (c *LLMResumeChunker) parseResponse(response string) (*LLMResumeStructure, error) {
	// 提取JSON部分（防止LLM返回的不是纯JSON）
	jsonStr := extractJSON(response)
	if jsonStr == "" {
		// 记录原始响应以供调试
		c.logger.Printf("无法从LLM响应中提取有效的JSON。原始响应: %s", response)
		return nil, fmt.Errorf("无法从LLM响应中提取有效的JSON")
	}

	// 解析JSON
	var result LLMResumeStructure
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("解析JSON失败: %w", err)
	}

	// 验证has_work_exp和has_intern标志的准确性
	// 1. 验证has_work_exp，只有在存在WORK_EXPERIENCE类型的chunk时才为true
	hasWorkExpChunk := false
	hasInternshipChunk := false

	for _, section := range result.Sections {
		if strings.ToUpper(section.Type) == "WORK_EXPERIENCE" {
			hasWorkExpChunk = true
		}
		if strings.ToUpper(section.Type) == "INTERNSHIPS" {
			hasInternshipChunk = true
		}
	}

	// 重写元数据中的has_work_exp和has_intern，确保与实际chunk类型一致
	result.Metadata.HasWorkExp = hasWorkExpChunk
	result.Metadata.HasIntern = hasInternshipChunk

	c.logger.Printf("[LLMResumeChunker] 验证结果: hasWorkExpChunk=%v, hasInternshipChunk=%v",
		hasWorkExpChunk, hasInternshipChunk)

	return &result, nil
}

// 从文本中提取JSON
func extractJSON(text string) string {
	// 尝试使用正则表达式提取 ```json ... ``` 代码块中的内容
	re := regexp.MustCompile("(?s)```json\\s*(\\{.*?\\})\\s*```")
	matches := re.FindStringSubmatch(text)
	if len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}

	// 如果正则没有匹配到，尝试寻找 JSON 的开始和结束位置作为回退
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
				return strings.TrimSpace(text[start : i+1])
			}
		}
	}
	return ""
}
