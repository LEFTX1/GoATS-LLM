package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// SectionType 表示简历章节类型
type SectionType string

const (
	// 基本信息章节
	SectionBasicInfo SectionType = "BASIC_INFO"
	// 教育经历章节
	SectionEducation SectionType = "EDUCATION"
	// 工作经历章节
	SectionWorkExperience SectionType = "WORK_EXPERIENCE"
	// 技能章节
	SectionSkills SectionType = "SKILLS"
	// 项目经历章节
	SectionProjects SectionType = "PROJECTS"
	// 实习经历章节
	SectionInternships SectionType = "INTERNSHIPS"
	// 获奖经历章节
	SectionAwards SectionType = "AWARDS"
	// 研究经历章节
	SectionResearch SectionType = "RESEARCH"
	// 作品集章节
	SectionPortfolio SectionType = "PORTFOLIO"
	// 其他经历章节
	SectionOtherExp SectionType = "OTHER_EXPERIENCE"
	// 个人介绍章节
	SectionPersonalIntro SectionType = "PERSONAL_INTRO"
	// 未分类内容章节
	SectionUnknown SectionType = "UNKNOWN"
	// 完整文本（未分块）
	SectionFullText SectionType = "FULL_TEXT"
)

// 简历章节结构
type ResumeSection struct {
	Type             SectionType // 章节类型
	Title            string      // 实际的章节标题
	Content          string      // 章节内容
	ChunkID          int         // 分块ID
	ResumeIdentifier string      // 简历标识符
}

// 简历分块器的配置
type ChunkerConfig struct {
	// 是否使用LLM辅助分块，当规则无法有效分块时使用
	UseLLMFallback bool

	// 自定义章节标记正则表达式映射
	// 例如 {"教育经历": "(?i)(教育|学历|教育背景|学历背景|学校|学院|大学|教育经历)"}
	CustomSectionRegexMap map[string]string

	// 最小块大小（字符数）
	MinChunkSize int

	// 是否保留空行和格式
	PreserveFormat bool
}

// 简历分块器
type ResumeChunker struct {
	config ChunkerConfig

	// 编译好的正则表达式
	sectionRegexMap map[SectionType]*regexp.Regexp

	// 基本信息识别模式
	basicInfoPatterns map[string]*regexp.Regexp
}

// 创建新的简历分块器
func NewResumeChunker(config ChunkerConfig) (*ResumeChunker, error) {
	chunker := &ResumeChunker{
		config:          config,
		sectionRegexMap: make(map[SectionType]*regexp.Regexp),
	}

	// 默认的章节标记正则表达式
	defaultSectionRegexMap := map[SectionType]string{
		SectionEducation:      `(?i)(教育|学历|教育背景|学历背景|学校|教育经历)[\s\:]`,
		SectionSkills:         `(?i)(专业技能|技能|技术栈|核心能力|技术技能)[\s\:]`,
		SectionProjects:       `(?i)(项目经[历验]|项目|项目描述)[\s\:]`,
		SectionWorkExperience: `(?i)(工作经[历验]|实习经[历验]|工作履历)[\s\:]`,
		SectionInternships:    `(?i)(实习经[历验]|实习)[\s\:]`,
		SectionAwards:         `(?i)(获奖情况|奖[项励]|荣誉)[\s\:]`,
		SectionResearch:       `(?i)(研究经[历验]|科研|学术)[\s\:]`,
		SectionPortfolio:      `(?i)(作品|作品集|个人作品)[\s\:]`,
		SectionOtherExp:       `(?i)(其他经[历验]|社团活动|课外活动)[\s\:]`,
		SectionPersonalIntro:  `(?i)(自我评价|个人简介|自我介绍|个人总结)[\s\:]`,
	}

	// 合并自定义正则
	if config.CustomSectionRegexMap != nil {
		for section, pattern := range config.CustomSectionRegexMap {
			sectionType := SectionType(section)
			defaultSectionRegexMap[sectionType] = pattern
		}
	}

	// 编译正则表达式
	for sectionType, pattern := range defaultSectionRegexMap {
		regex, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("编译章节正则表达式错误 %s: %w", sectionType, err)
		}
		chunker.sectionRegexMap[sectionType] = regex
	}

	// 编译基本信息识别模式
	chunker.basicInfoPatterns = map[string]*regexp.Regexp{
		"email":    regexp.MustCompile(`(?i)[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`),
		"phone":    regexp.MustCompile(`(?:\+?86)?1[3-9]\d{9}|(?:\d{3,4}-)?\d{7,8}`),
		"github":   regexp.MustCompile(`(?i)github\.com/[\w.-]+`),
		"linkedin": regexp.MustCompile(`(?i)linkedin\.com/in/[\w.-]+`),
		"position": regexp.MustCompile(`(?i)意向职位[：:]\s*([^\n]+)`),
		"location": regexp.MustCompile(`(?i)(?:地点|城市|地域)[：:]\s*([^\n]+)|[\-\|]\s*([北上广深杭成都重庆武汉西安]{2,})\s*[\-\|]`),
		"name":     regexp.MustCompile(`^[\p{Han}]{2,4}$|^[A-Za-z]+\s[A-Za-z]+$`), // 中文字符使用 \p{Han} 代替 \u4e00-\u9fa5
	}

	return chunker, nil
}

// 分块简历文本
func (c *ResumeChunker) ChunkResume(ctx context.Context, resumeText string) ([]*ResumeSection, error) {
	// 清理文本
	text := resumeText
	if !c.config.PreserveFormat {
		// 统一化换行符
		text = strings.ReplaceAll(text, "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")

		// 移除多余空行
		for strings.Contains(text, "\n\n\n") {
			text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
		}
	}

	// 按行分割
	lines := strings.Split(text, "\n")

	// 第一轮：识别每行可能属于的章节
	lineTypes := make([]SectionType, len(lines))
	for i, line := range lines {
		lineTypes[i] = c.classifyLine(line)
	}

	// 结果列表
	var sections []*ResumeSection

	// 当前章节
	currentSection := &ResumeSection{
		Type:  SectionBasicInfo, // 默认从基本信息开始
		Title: string(SectionBasicInfo),
	}
	currentContent := strings.Builder{}

	// 遍历每行，构建章节
	for i, line := range lines {
		lineType := lineTypes[i]

		// 如果这行是一个新章节标题
		if lineType != SectionUnknown && lineType != currentSection.Type {
			// 保存当前章节（如果不为空）
			if currentContent.Len() > 0 {
				currentSection.Content = currentContent.String()
				sections = append(sections, currentSection)

				// 创建新章节
				currentSection = &ResumeSection{
					Type:  lineType,
					Title: line, // 使用当前行作为标题
				}
				currentContent = strings.Builder{}
			} else {
				// 没有内容，直接更改当前章节类型
				currentSection.Type = lineType
				currentSection.Title = line
			}
		} else {
			// 继续添加到当前章节
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}

	// 添加最后一个章节
	if currentContent.Len() > 0 {
		currentSection.Content = currentContent.String()
		sections = append(sections, currentSection)
	}

	// 确保基本信息章节存在
	hasBasicInfo := false
	for _, section := range sections {
		if section.Type == SectionBasicInfo {
			hasBasicInfo = true
			break
		}
	}

	if !hasBasicInfo && len(sections) > 0 {
		// 从第一个章节中提取基本信息
		firstSection := sections[0]
		basicInfoSection := &ResumeSection{
			Type:    SectionBasicInfo,
			Title:   string(SectionBasicInfo),
			Content: c.extractBasicInfo(firstSection.Content),
		}
		// 将基本信息章节插入到最前面
		sections = append([]*ResumeSection{basicInfoSection}, sections...)
	}

	return sections, nil
}

// 检测行属于哪个章节类型
func (c *ResumeChunker) classifyLine(line string) SectionType {
	line = strings.TrimSpace(line)
	if line == "" {
		return SectionUnknown
	}

	// 检查每个章节类型
	for sectionType, regex := range c.sectionRegexMap {
		if regex.MatchString(line) {
			return sectionType
		}
	}

	return SectionUnknown
}

// 从文本中提取基本信息
func (c *ResumeChunker) extractBasicInfo(text string) string {
	var infoBuilder strings.Builder

	// 尝试提取每种基本信息
	for infoType, pattern := range c.basicInfoPatterns {
		matches := pattern.FindStringSubmatch(text)
		if len(matches) > 0 {
			match := matches[0]
			if len(matches) > 1 && matches[1] != "" {
				match = matches[1] // 使用捕获组，如果有的话
			}
			infoBuilder.WriteString(fmt.Sprintf("%s: %s\n", infoType, match))
		}
	}

	return infoBuilder.String()
}

// 将分块结果格式化为易读格式
func (c *ResumeChunker) FormatSections(sections []*ResumeSection) string {
	var result strings.Builder

	for i, section := range sections {
		result.WriteString(fmt.Sprintf("=== %s ===\n", section.Title))
		result.WriteString(section.Content)
		if i < len(sections)-1 {
			result.WriteString("\n\n")
		}
	}

	return result.String()
}
