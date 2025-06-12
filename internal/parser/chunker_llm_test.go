package parser

import (
	"ai-agent-go/internal/agent"
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/types"
	"context"
	"encoding/json"
	"fmt"
	"github.com/joho/godotenv"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 测试用LLM模型模拟器
type MockLLMModel struct {
	// 模拟响应
	mockResponse string
	// 记录绑定的工具 (可选，用于测试)
	boundTools []*schema.ToolInfo
	// 用于测试的错误
	Err error
	// 用于测试的调用次数
	CallCount int
	// 用于测试的成功调用次数
	SucceedAfterNCalls int
}

// Generate 实现model.ChatModel接口
func (m *MockLLMModel) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	// 返回预设的模拟响应
	return &schema.Message{
		Role:    "assistant",
		Content: m.mockResponse,
	}, nil
}

// Stream 实现model.ChatModel接口
func (m *MockLLMModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	// 测试中不需要流式响应
	return nil, nil
}

// BindTools 实现model.ChatModel接口
func (m *MockLLMModel) BindTools(tools []*schema.ToolInfo) error {
	// 空实现，测试中不需要工具绑定
	m.boundTools = tools // 可选：记录绑定的工具
	log.Printf("[MockLLMModel] BindTools called with %d tools.", len(tools))
	return nil
}

// WithTools 实现 model.ToolCallingChatModel 接口
// 这个模拟实现简单地调用 BindTools
func (m *MockLLMModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	log.Printf("[MockLLMModel] WithTools called with %d tools. Will call BindTools.", len(tools))
	err := m.BindTools(tools)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// TestLLMResumeChunker 测试LLM简历分块器的基本功能
func TestLLMResumeChunker(t *testing.T) {
	// 创建模拟响应
	mockResponse := `{
		"basic_info": {
			"name": "张三",
			"phone": "13800138000",
			"email": "zhangsan@example.com",
			"education_level": "本科",
			"position": "后端开发工程师"
		},
		"chunks": [
			{
				"chunk_id": 1,
				"resume_identifier": "张三_13800138000",
				"type": "BASIC_INFO",
				"title": "",
				"content": "张三\\n13800138000\\nzhangsan@example.com\\n后端开发工程师"
			},
			{
				"chunk_id": 2,
				"resume_identifier": "张三_13800138000",
				"type": "EDUCATION",
				"title": "教育经历",
				"content": "清华大学 计算机科学与技术 本科\\n2018-2022"
			},
			{
				"chunk_id": 3,
				"resume_identifier": "张三_13800138000",
				"type": "SKILLS",
				"title": "专业技能",
				"content": "精通Java、Spring Boot\\n熟悉MySQL、Redis"
			}
		],
		"metadata": {
			"is_211": true,
			"is_985": true,
			"is_double_top": true,
			"has_intern": false,
			"skills": ["Java", "Spring Boot", "MySQL", "Redis"],
			"highest_education": "本科",
			"years_of_experience": 1,
			"resume_score": 80,
			"tags": ["应届毕业生", "计算机专业", "后端开发"]
		}
	}`

	// 创建模拟LLM模型
	mockLLM := &MockLLMModel{
		mockResponse: mockResponse,
	}
	testLogger := log.New(io.Discard, "", 0)

	// 创建LLM简历分块器
	chunker := NewLLMResumeChunker(mockLLM, testLogger)

	// 测试分块功能
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 模拟简历文本
	resumeText := `张三
13800138000
zhangsan@example.com
后端开发工程师

教育经历
清华大学 计算机科学与技术 本科
2018-2022

专业技能
精通Java、Spring Boot
熟悉MySQL、Redis

项目经验
电商平台后端开发
负责用户模块和订单系统，优化数据库查询性能提升30%`

	// 调用分块函数
	sections, basicInfo, err := chunker.ChunkResume(ctx, resumeText)
	require.NoError(t, err, "简历分块不应返回错误")

	// 验证结果
	require.NotNil(t, sections, "分块结果不应为nil")
	require.NotNil(t, basicInfo, "基本信息不应为nil")

	// 验证基本信息
	assert.Equal(t, "张三", basicInfo["name"], "姓名应正确提取")
	assert.Equal(t, "13800138000", basicInfo["phone"], "电话号码应正确提取")
	assert.Equal(t, "本科", basicInfo["education_level"], "学历应正确提取")

	// 验证元数据
	assert.Equal(t, "true", basicInfo["is_211"], "学校是否211应正确标记")
	assert.Equal(t, "true", basicInfo["is_985"], "学校是否985应正确标记")
	assert.Equal(t, "true", basicInfo["is_double_top"], "学校是否双一流应正确标记")
	assert.Equal(t, "1.0", basicInfo["years_of_experience"], "经验年限应正确估算")
	assert.Equal(t, "80", basicInfo["resume_score"], "简历评分应正确计算")

	// 验证章节
	require.GreaterOrEqual(t, len(sections), 3, "应至少识别出3个章节")

	// 验证技能章节
	var skillsSection *types.ResumeSection
	for _, section := range sections {
		if section.Type == types.SectionSkills {
			skillsSection = section
			break
		}
	}
	require.NotNil(t, skillsSection, "应包含技能章节")
	assert.Contains(t, skillsSection.Content, "Java", "技能章节应包含Java")
}

// TestFewShotLearning 测试少样本学习效果
func TestFewShotLearning(t *testing.T) {
	// 创建更复杂的模拟响应，包含元数据
	mockResponse := `{
		"basic_info": {
			"name": "李四",
			"phone": "18600123456",
			"email": "lisi@gmail.com",
			"education_level": "硕士",
			"position": "机器学习工程师",
			"location": "上海"
		},
		"chunks": [
			{
				"chunk_id": 1,
				"resume_identifier": "李四_18600123456",
				"type": "BASIC_INFO",
				"title": "",
				"content": "李四 | 机器学习工程师 | 上海\\n电话：18600123456 | 邮箱：lisi@gmail.com"
			},
			{
				"chunk_id": 2,
				"resume_identifier": "李四_18600123456",
				"type": "EDUCATION",
				"title": "教育背景",
				"content": "上海交通大学 | 计算机科学 | 硕士 | 2020-2023\\n华东师范大学 | 数学 | 本科 | 2016-2020"
			},
			{
				"chunk_id": 3,
				"resume_identifier": "李四_18600123456",
				"type": "SKILLS",
				"title": "专业技能",
				"content": "精通Python、TensorFlow、PyTorch\\n熟悉大规模机器学习部署\\n了解前端React基础"
			},
			{
				"chunk_id": 4,
				"resume_identifier": "李四_18600123456",
				"type": "INTERNSHIPS",
				"title": "实习经历",
				"content": "阿里巴巴 | 算法实习生 | 2022.03-2022.09\\n负责搜索排序算法优化，提升点击率5%"
			}
		],
		"metadata": {
			"is_211": true,
			"is_985": true,
			"is_double_top": true,
			"has_intern": true,
			"skills": ["Python", "TensorFlow", "PyTorch", "机器学习", "深度学习"],
			"highest_education": "硕士",
			"years_of_experience": 0.5,
			"resume_score": 85,
			"tags": ["应届毕业生", "AI/ML", "有实习经验", "数学背景"]
		}
	}`

	// 创建模拟LLM模型
	mockLLM := &MockLLMModel{
		mockResponse: mockResponse,
	}
	testLoggerForFewShot := log.New(io.Discard, "", 0)

	// 创建带自定义少样本的LLM简历分块器
	customExample := `示例：
"""
王五 | 数据科学家
电话：13900001111 | 邮箱：wangwu@example.com

北京大学 | 统计学 | 博士 | 2018-2022
"""

输出：
{
  "basic_info": {
    "name": "王五",
    "phone": "13900001111",
    "email": "wangwu@example.com",
    "education_level": "博士"
  },
  "chunks": [
    {
      "chunk_id": 1,
      "resume_identifier": "王五_13900001111",
      "type": "BASIC_INFO",
      "title": "",
      "content": "王五 | 数据科学家\\n电话：13900001111 | 邮箱：wangwu@example.com"
    },
    {
      "chunk_id": 2,
      "resume_identifier": "王五_13900001111",
      "type": "EDUCATION",
      "title": "教育经历",
      "content": "北京大学 | 统计学 | 博士 | 2018-2022"
    }
  ],
  "metadata": {
    "is_211": true,
    "is_985": true,
    "is_double_top": true,
    "has_intern": false,
    "skills": [],
    "highest_education": "博士",
    "years_of_experience": 0,
    "resume_score": 90,
    "tags": ["博士学历", "统计学", "数据科学"]
  }
}`

	chunker := NewLLMResumeChunker(mockLLM, testLoggerForFewShot, WithCustomFewShotExamples(customExample))

	// 测试分块功能
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 模拟简历文本
	resumeText := `李四 | 机器学习工程师 | 上海
电话：18600123456 | 邮箱：lisi@gmail.com

教育背景：
上海交通大学 | 计算机科学 | 硕士 | 2020-2023
华东师范大学 | 数学 | 本科 | 2016-2020

专业技能：
- 精通Python、TensorFlow、PyTorch
- 熟悉大规模机器学习部署
- 了解前端React基础

实习经历：
阿里巴巴 | 算法实习生 | 2022.03-2022.09
负责搜索排序算法优化，提升点击率5%`

	// 调用分块函数
	sections, basicInfo, err := chunker.ChunkResume(ctx, resumeText)
	require.NoError(t, err, "简历分块不应返回错误")

	// 验证结果
	require.NotNil(t, sections, "分块结果不应为nil")
	require.NotNil(t, basicInfo, "基本信息不应为nil")

	// 验证元数据
	assert.Equal(t, "true", basicInfo["has_intern"], "实习经历应正确标记")
	assert.Equal(t, "硕士", basicInfo["highest_education"], "最高学历应正确提取")
	assert.Equal(t, "85", basicInfo["resume_score"], "简历评分应正确计算")

	// 验证技能章节
	var skillsSection *types.ResumeSection
	for _, section := range sections {
		if section.Type == types.SectionSkills {
			skillsSection = section
			break
		}
	}
	require.NotNil(t, skillsSection, "应包含技能章节")
	assert.Contains(t, skillsSection.Content, "Python", "技能章节应包含Python")
}

// TestJSONFormatHandling 测试JSON格式处理
func TestJSONFormatHandling(t *testing.T) {
	// 创建带有非标准格式的模拟响应
	mockResponse := `
我的分析结果如下：

{
  "basic_info": {
    "name": "赵六",
    "phone": "15911112222",
    "email": "zhaoliu@example.com"
  },
  "chunks": [
    {
      "chunk_id": 1,
      "resume_identifier": "赵六_15911112222",
      "type": "BASIC_INFO",
      "title": "",
      "content": "赵六\\n15911112222\\nzhaoliu@example.com\\n软件工程师"
    },
    {
      "chunk_id": 2,
      "resume_identifier": "赵六_15911112222",
      "type": "SKILLS",
      "title": "技能",
      "content": "JavaScript, Vue, Node.js"
    }
  ],
  "metadata": {
    "is_211": false,
    "is_985": false,
    "is_double_top": false,
    "has_intern": true,
    "highest_education": "本科",
    "years_of_experience": 2,
    "resume_score": 70,
    "tags": ["前端开发", "全栈开发"]
  }
}

希望这个分析对您有帮助！
`

	// 创建模拟LLM模型
	mockLLM := &MockLLMModel{
		mockResponse: mockResponse,
	}
	testLoggerForJSONHandling := log.New(io.Discard, "", 0)

	// 创建LLM简历分块器
	chunker := NewLLMResumeChunker(mockLLM, testLoggerForJSONHandling)

	// 测试分块功能
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 模拟简历文本
	resumeText := `赵六
15911112222
zhaoliu@example.com
软件工程师

技能：JavaScript, Vue, Node.js
工作经验：2年
最高学历：本科`

	// 调用分块函数
	sections, basicInfo, err := chunker.ChunkResume(ctx, resumeText)
	require.NoError(t, err, "简历分块不应返回错误")

	// 验证能否正确提取非标准响应中的JSON
	require.NotNil(t, sections, "分块结果不应为nil")
	require.NotNil(t, basicInfo, "基本信息不应为nil")

	// 验证基本信息
	assert.Equal(t, "赵六", basicInfo["name"], "姓名应正确提取")
	assert.Equal(t, "15911112222", basicInfo["phone"], "电话号码应正确提取")

	// 验证技能章节
	var skillsFound bool
	for _, section := range sections {
		if section.Type == types.SectionSkills {
			skillsFound = true
			assert.Contains(t, section.Content, "JavaScript", "技能章节应包含JavaScript")
			break
		}
	}
	assert.True(t, skillsFound, "应该识别出技能章节")

	// 验证经验年限
	assert.Equal(t, "2.0", basicInfo["years_of_experience"], "经验年限应正确提取")
}

// TestResumeStructureSerialization 测试简历结构的序列化和反序列化
func TestResumeStructureSerialization(t *testing.T) {
	// 创建示例数据
	resumeStructure := LLMResumeStructure{
		BasicInfo: map[string]interface{}{
			"name":  "测试用户",
			"phone": "13888888888",
			"email": "test@example.com",
		},
		Sections: []struct {
			ChunkID          int    `json:"chunk_id"`
			ResumeIdentifier string `json:"resume_identifier"`
			Type             string `json:"type"`
			Title            string `json:"title"`
			Content          string `json:"content"`
		}{
			{
				ChunkID:          1,
				ResumeIdentifier: "测试用户_13888888888",
				Type:             "BASIC_INFO",
				Title:            "",
				Content:          "测试用户\\n13888888888\\ntest@example.com",
			},
			{
				ChunkID:          2,
				ResumeIdentifier: "测试用户_13888888888",
				Type:             "EDUCATION",
				Title:            "教育经历",
				Content:          "测试大学，计算机科学，2018-2022",
			},
		},
		Metadata: struct {
			Is211                          bool     `json:"is_211"`
			Is985                          bool     `json:"is_985"`
			IsDoubleTop                    bool     `json:"is_double_top"`
			HasIntern                      bool     `json:"has_intern"`
			HasWorkExp                     bool     `json:"has_work_exp"`
			Education                      string   `json:"highest_education"`
			Experience                     float64  `json:"years_of_experience"`
			Score                          int      `json:"resume_score"`
			Tags                           []string `json:"tags"`
			HasAlgorithmAward              bool     `json:"has_algorithm_award"`
			AlgorithmAwardTitles           []string `json:"algorithm_award_titles"`
			HasProgrammingCompetitionAward bool     `json:"has_programming_competition_award"`
			ProgrammingCompetitionTitles   []string `json:"programming_competition_titles"`
		}{
			Is211:       true,
			Is985:       true,
			IsDoubleTop: true,
			HasIntern:   true,
			Education:   "本科",
			Experience:  3.5, // 使用浮点数表示经验年限
			Score:       85,
			Tags:        []string{"技术", "管理"},
		},
	}

	// 序列化
	jsonData, err := json.Marshal(resumeStructure)
	require.NoError(t, err, "序列化不应返回错误")

	// 反序列化
	var parsedStructure LLMResumeStructure
	err = json.Unmarshal(jsonData, &parsedStructure)
	require.NoError(t, err, "反序列化不应返回错误")

	// 验证字段
	assert.Equal(t, resumeStructure.BasicInfo["name"], parsedStructure.BasicInfo["name"], "姓名应正确序列化")
	assert.Equal(t, resumeStructure.BasicInfo["phone"], parsedStructure.BasicInfo["phone"], "电话应正确序列化")
	assert.Equal(t, resumeStructure.Metadata.Is211, parsedStructure.Metadata.Is211, "211标记应正确序列化")
	assert.Equal(t, resumeStructure.Metadata.Score, parsedStructure.Metadata.Score, "评分应正确序列化")
	assert.Equal(t, resumeStructure.Metadata.Experience, parsedStructure.Metadata.Experience, "经验年限应正确序列化")
	assert.Equal(t, resumeStructure.Sections[0].ChunkID, parsedStructure.Sections[0].ChunkID, "分块ID应正确序列化")
	assert.Equal(t, resumeStructure.Sections[0].ResumeIdentifier, parsedStructure.Sections[0].ResumeIdentifier, "简历标识应正确序列化")
}

// TestRealResumeParsing 使用真实PDF提取的文本测试
func TestRealResumeParsing(t *testing.T) {
	// 跳过CI环境测试
	if os.Getenv("CI") == "true" {
		t.Skip("在CI环境中跳过真实PDF测试")
	}

	// 创建PDF解析器
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second) // 增加超时时间至90秒
	defer cancel()

	// 加载配置
	configPath := "C:/Users/left/Desktop/agent-go/internal/config/config.yaml"
	cfg, err := config.LoadConfigFromFileAndEnv(configPath)
	if err != nil {
		t.Fatalf("加载配置文件失败: %v", err)
	}

	// 使用函数选项模式创建不带元数据的Tika解析器
	tikaExtractor := NewTikaPDFExtractor(
		cfg.Tika.ServerURL,
		WithMinimalMetadata(false),                 // 不提取精简元数据
		WithFullMetadata(false),                    // 不提取完整元数据
		WithTimeout(30*time.Second),                // 设置30秒超时
		WithAnnotations(false),                     // 禁用链接注释提取
		WithTikaLogger(log.New(io.Discard, "", 0)), // 为Tika添加丢弃型logger
	)
	t.Log("创建了不带元数据的Tika解析器，并禁用了链接注释提取")

	// 查找测试PDF文件
	testPDFs := []string{
		"testdata/黑白整齐简历模板 (6).pdf",
		"../testdata/黑白整齐简历模板 (6).pdf",
		"../../testdata/黑白整齐简历模板 (6).pdf",
	}

	var pdfText string
	var foundFile bool

	// 尝试使用Tika提取器提取PDF文本
	for _, path := range testPDFs {
		if _, err := os.Stat(path); err == nil {
			// 找到文件，使用Tika提取文本
			t.Logf("找到PDF文件: %s", path)
			text, metadata, err := tikaExtractor.ExtractFromFile(ctx, path)
			if err != nil {
				t.Logf("从文件提取失败: %v，尝试下一个文件", err)
				continue
			}

			pdfText = text
			foundFile = true

			t.Logf("成功从PDF提取文本，长度: %d 字符", len(pdfText))
			t.Logf("基础元数据项数: %d (应该只包含基本项，无Tika元数据)", len(metadata))
			for k, v := range metadata {
				t.Logf("元数据: %s = %v", k, v)
			}
			break
		}
	}

	// 如果没有找到PDF文件，尝试从文本文件加载
	if !foundFile || pdfText == "" {
		// 查找测试PDF的Tika输出的文本文件
		tikaOutputPaths := []string{
			"testdata/resume_tika_output.txt",
			"../testdata/resume_tika_output.txt",
			"../../testdata/resume_tika_output.txt",
		}

		for _, path := range tikaOutputPaths {
			if tikaData, err := os.ReadFile(path); err == nil {
				pdfText = string(tikaData)
				foundFile = true
				t.Logf("成功从Tika输出文件加载简历文本: %s", path)
				break
			}
		}
	}

	if !foundFile || pdfText == "" {
		t.Skip("找不到测试PDF文件或Tika输出文件，或提取失败，跳过测试")
		return
	}

	// 打印完整的简历文本
	t.Log("\n【输入的完整简历文本】:")
	t.Log("----------------------------------------------------------------------")
	t.Log(pdfText)
	t.Log("----------------------------------------------------------------------")

	// 使用真实LLM进行测试
	// 初始化真实的LLM模型
	llmModel, err := initRealLLM(ctx)
	if err != nil {
		// 如果环境变量 LLM_API_KEY 未设置，initRealLLM 会返回错误，此时应跳过测试
		if strings.Contains(err.Error(), "API Key") {
			t.Skipf("跳过真实LLM测试: %v。请在配置文件中设置有效的API Key。", err)
		}
		t.Fatalf("初始化真实LLM模型失败: %v", err)
	}
	testLoggerForRealLLM := log.New(io.Discard, "", 0)
	// 创建LLM简历分块器
	chunker := NewLLMResumeChunker(llmModel, testLoggerForRealLLM)

	// 使用从PDF提取的文本进行测试
	t.Log("开始使用LLM解析简历文本...")
	startTime := time.Now()
	sections, basicInfo, err := chunker.ChunkResume(ctx, pdfText)
	elapsedTime := time.Since(startTime)
	t.Logf("LLM解析耗时: %.3f 秒", elapsedTime.Seconds())

	require.NoError(t, err, "简历分块不应返回错误")

	// 验证结果
	require.NotNil(t, sections, "分块结果不应为nil")
	require.NotNil(t, basicInfo, "基本信息不应为nil")

	// 打印完整的LLM输出JSON
	t.Log("\n【LLM输出的完整JSON数据】:")
	t.Log("----------------------------------------------------------------------")

	// 重新构建JSON结构以便打印
	jsonOutput := struct {
		BasicInfo map[string]interface{} `json:"basic_info"`
		Chunks    []struct {
			ChunkID          int    `json:"chunk_id"`
			ResumeIdentifier string `json:"resume_identifier"`
			Type             string `json:"type"`
			Title            string `json:"title"`
			Content          string `json:"content"`
		} `json:"chunks"`
		Metadata map[string]interface{} `json:"metadata"`
	}{
		BasicInfo: make(map[string]interface{}),
		Metadata:  make(map[string]interface{}),
	}

	// 填充基本信息
	for k, v := range basicInfo {
		// 过滤出元数据字段
		if k == "is_211" || k == "is_985" || k == "is_double_top" || k == "has_intern" ||
			k == "highest_education" || k == "years_of_experience" || k == "resume_score" || k == "tags" {
			jsonOutput.Metadata[k] = v
		} else {
			jsonOutput.BasicInfo[k] = v
		}
	}

	// 填充章节
	for _, section := range sections {
		jsonOutput.Chunks = append(jsonOutput.Chunks, struct {
			ChunkID          int    `json:"chunk_id"`
			ResumeIdentifier string `json:"resume_identifier"`
			Type             string `json:"type"`
			Title            string `json:"title"`
			Content          string `json:"content"`
		}{
			ChunkID:          section.ChunkID,
			ResumeIdentifier: section.ResumeIdentifier,
			Type:             string(section.Type),
			Title:            section.Title,
			Content:          section.Content,
		})
	}

	// 转换为JSON并打印
	jsonData, err := json.MarshalIndent(jsonOutput, "", "  ")
	if err != nil {
		t.Logf("无法序列化JSON: %v", err)
	} else {
		t.Logf("%s", string(jsonData))
	}
	t.Log("----------------------------------------------------------------------")

	// 输出基本信息和元数据，用于调试
	t.Logf("真实PDF解析 - 基本信息：%+v", basicInfo)
	t.Logf("真实PDF解析 - 识别到 %d 个章节", len(sections))
	for i, section := range sections {
		t.Logf("真实PDF解析 - 章节 %d: 类型=%s, 标题=%s", i+1, section.Type, section.Title)
		// 打印章节内容预览
		if len(section.Content) > 100 {
			t.Logf("  内容预览: %s...", section.Content[:100])
		} else {
			t.Logf("  内容: %s", section.Content)
		}
	}

	// 对真实LLM的输出进行一些基本验证，由于结果不确定，断言会比较宽松
	assert.NotEmpty(t, basicInfo["name"], "姓名不应为空")
	assert.True(t, len(sections) > 0, "应至少解析出一个章节")

	// 例如，我们可以检查是否提取了"技能"或"教育"等常见元数据字段
	// 这里不作具体值的断言，因为不同简历和LLM表现可能不同
	_, phoneFound := basicInfo["phone"]
	_, emailFound := basicInfo["email"]
	assert.True(t, phoneFound || emailFound, "应至少提取电话或邮箱中的一个")
}

// TestRealLLMChunker 测试使用真实LLM模型的简历分块器
func TestRealLLMChunker(t *testing.T) {
	// 跳过测试，如果设置了环境变量 SKIP_LLM_TESTS
	if os.Getenv("SKIP_LLM_TESTS") == "true" {
		t.Skip("跳过LLM实际调用测试 (SKIP_LLM_TESTS=true)")
	}

	// 初始化真实的LLM模型
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // 增加超时时间
	defer cancel()

	t.Log("==================== 真实LLM简历解析评估测试 ====================")
	startTime := time.Now()

	llmModel, err := initRealLLM(ctx)
	if err != nil {
		// 如果环境变量 LLM_API_KEY 未设置，initRealLLM 会返回错误，此时应跳过测试
		if strings.Contains(err.Error(), "API 密钥不能为空") || strings.Contains(err.Error(), "LLM_API_KEY") {
			t.Skipf("跳过真实LLM测试: %v。请设置 LLM_API_KEY 环境变量。", err)
		}
		t.Fatalf("初始化真实LLM模型失败: %v", err)
	}

	// 创建LLM简历分块器
	chunker := NewLLMResumeChunker(llmModel, log.New(io.Discard, "", 0))

	// 测试简历文本 (使用周航航的简历作为例子，因为它在提示词中出现过)
	resumeText := `周航航
18822157735 2603167476@qq.com https://github.com/LEFTX1
在读 意向职位：服务端实习生 北京-天津-上海-深圳
教育经历
天津工业大学 (双一流) 计算机科学与技术 本科 2022-09 ~ 至今
专业技能
编程语言和基础知识:
| 熟悉 Golang，深入理解并发模型: Goroutine/Channel/Context, GMP调度, 内存管理/GC;
熟悉 Gin/Go-Zero/GORM 等框架 。了解 Java 基础与 Spring Boot 框架开发。
| 熟悉计算机网络 (HTTP/TCP), 常见数据结构 。
中间件:
| 熟悉 MySQL，理解 InnoDB: B+Tree, ACID, MVCC 相关实现原理; 具有分库分表经验。
| 熟悉 Redis ，理解其核心数据结构, 持久化 RDB/AOF, 高可用 哨兵/Cluster 实现原理; 熟悉缓
存设计模式:防穿透/雪崩/击穿, 分布式锁 RedLock 原理。
| 了解 Kafka，理解其核心架构 Broker/Topic/Partition/CG, 消息生产/消费模型, Offset管理, 副
本机制 Leader，Follower; 系统解耦, 异步通信 。
项目经历
Go-Link 2024-11 ~ 2025-03
go-zero+mysq+redis+etcd+Kafka https://www.laoyujianli.com/
项目介绍：为提升内部营销(短信/App Push)活动效果与追踪，主导/参与构建了短链接服务平
台，提供链接创建、管理、高性能跳转及点击统计分析。
核心优化与成果：
| 高性能ID生成：采用雪花算法 (优化时钟回拨问题)，对比随机串/MurmurHash方案，空间换
时间，API平均/P99延迟分别从11ms/16ms降至6ms/10ms (降低45%/37%)，显著提升了大规
模链接创建时的系统响应速度与可靠性。
| 异步解耦统计：通过Goroutine+Kafka将跳转统计异步化，消费者批量写库，确保跳转接口
性能仅依赖Redis(~1.5ms)。
| 读路径高可用：布隆过滤器拦截无效请求；空值缓存防穿透；Singleflight机制防热点击穿，
显著降低DB负载。
| 数据水平分片：核心业务表按CRC32哈希分16片，实现千万级数据高效存储与查询`

	t.Logf("【原始简历文本】长度: %d 字符", len(resumeText))
	t.Logf("【原始简历文本】前100字符预览: %s...", resumeText[:min(100, len(resumeText))])

	// 打印解析开始时间
	parseStartTime := time.Now()
	t.Logf("【解析开始】时间: %v", parseStartTime.Format("2006-01-02 15:04:05.000"))

	// 调用分块函数
	sections, basicInfo, err := chunker.ChunkResume(ctx, resumeText)

	// 解析结束时间
	parseEndTime := time.Now()
	parseElapsed := parseEndTime.Sub(parseStartTime)
	t.Logf("【解析结束】时间: %v", parseEndTime.Format("2006-01-02 15:04:05.000"))
	t.Logf("【解析耗时】: %.3f 秒", parseElapsed.Seconds())

	require.NoError(t, err, "简历分块应成功完成")

	// 验证结果
	require.NotNil(t, sections, "分块结果不应为nil")
	require.NotNil(t, basicInfo, "基本信息不应为nil")

	// 详细输出基本信息
	t.Logf("\n【基本信息解析】共 %d 项:", len(basicInfo))
	t.Logf("----------------------------------------------------------------------")

	// 按照重要性排序输出基本信息
	importantFields := []string{"name", "phone", "email", "github", "education_level", "job_intention", "location"}
	for _, field := range importantFields {
		if value, ok := basicInfo[field]; ok {
			t.Logf("%-20s: %s", field, value)
		}
	}

	// 输出其他基本信息
	for key, value := range basicInfo {
		if !contains(importantFields, key) {
			t.Logf("%-20s: %s", key, value)
		}
	}

	// 详细输出元数据分析结果
	t.Logf("\n【元数据分析】:")
	t.Logf("----------------------------------------------------------------------")
	schoolInfo := []string{"is_211", "is_985", "is_double_top", "highest_education"}
	for _, field := range schoolInfo {
		if value, ok := basicInfo[field]; ok {
			t.Logf("%-20s: %s", field, value)
		}
	}

	// 经验和评分
	expScoreInfo := []string{"years_of_experience", "resume_score"}
	for _, field := range expScoreInfo {
		if value, ok := basicInfo[field]; ok {
			t.Logf("%-20s: %s", field, value)
		}
	}

	// 技能和标签
	if skills, ok := basicInfo["skills"]; ok {
		t.Logf("【技能列表】: %s", skills)
	}
	if tags, ok := basicInfo["tags"]; ok {
		t.Logf("【简历标签】: %s", tags)
	}

	// 详细输出章节信息
	t.Logf("\n【章节解析】共 %d 个章节:", len(sections))
	t.Logf("----------------------------------------------------------------------")
	for i, section := range sections {
		t.Logf("\n章节 %d:", i+1)
		t.Logf("类型: %s", section.Type)
		t.Logf("标题: %s", section.Title)
		t.Logf("唯一ID: %d", section.ChunkID)
		t.Logf("简历标识: %s", section.ResumeIdentifier)
		t.Logf("内容长度: %d 字符", len(section.Content))

		// 显示内容预览
		previewLen := min(200, len(section.Content))
		t.Logf("内容预览: %s%s",
			section.Content[:previewLen],
			ternary(len(section.Content) > previewLen, "...", ""))
	}

	// 验证基本信息 (基于周航航简历的预期)
	t.Logf("\n【断言验证】:")
	t.Logf("----------------------------------------------------------------------")

	passed := 0
	total := 0

	// 验证基本字段
	basicAssertions := map[string]string{
		"name":            "周航航",
		"phone":           "18822157735",
		"email":           "2603167476@qq.com",
		"education_level": "本科",
		"job_intention":   "服务端实习生",
	}

	for field, expected := range basicAssertions {
		total++
		if actual, ok := basicInfo[field]; ok && actual == expected {
			t.Logf("✓ %s = %s", field, expected)
			passed++
		} else {
			t.Logf("✗ %s: 期望 %s, 实际 %s", field, expected, actual)
		}
	}

	// 验证元数据
	metaAssertions := map[string]string{
		"is_double_top":     "true",
		"highest_education": "本科",
	}

	for field, expected := range metaAssertions {
		total++
		if actual, ok := basicInfo[field]; ok && actual == expected {
			t.Logf("✓ %s = %s", field, expected)
			passed++
		} else {
			t.Logf("✗ %s: 期望 %s, 实际 %s", field, expected, actual)
		}
	}

	// 验证技能提取
	skillsList := []string{"Golang", "MySQL", "Redis"}

	// 查找技能章节
	var skillsSection *types.ResumeSection
	for _, section := range sections {
		if section.Type == types.SectionSkills {
			skillsSection = section
			break
		}
	}

	require.NotNil(t, skillsSection, "应该存在技能章节")

	// 检查技能章节中是否包含特定技能
	for _, skill := range skillsList {
		total++
		if strings.Contains(skillsSection.Content, skill) {
			t.Logf("✓ 技能章节包含 %s", skill)
			passed++
		} else {
			t.Logf("✗ 技能章节中未检测到 %s", skill)
		}
	}

	// 验证章节
	expectedSectionTypes := []types.SectionType{types.SectionEducation, types.SectionSkills, types.SectionProjects}
	foundSections := make(map[types.SectionType]bool)

	for _, section := range sections {
		foundSections[section.Type] = true
	}

	for _, expectedType := range expectedSectionTypes {
		total++
		if found := foundSections[expectedType]; found {
			t.Logf("✓ 识别出 %s 章节", expectedType)
			passed++
		} else {
			t.Logf("✗ 未识别出 %s 章节", expectedType)
		}
	}

	// 总结
	accuracy := float64(passed) / float64(total) * 100
	t.Logf("\n【测试总结】:")
	t.Logf("----------------------------------------------------------------------")
	t.Logf("总断言数: %d, 通过: %d, 准确率: %.2f%%", total, passed, accuracy)
	t.Logf("总耗时: %.3f 秒", time.Since(startTime).Seconds())
	t.Logf("==================== 测试结束 ====================")
}

// TestLLMWithDataAnalystResume 测试使用数据分析师简历
func TestLLMWithDataAnalystResume(t *testing.T) {
	// 跳过测试，如果设置了环境变量 SKIP_LLM_TESTS
	if os.Getenv("SKIP_LLM_TESTS") == "true" {
		t.Skip("跳过LLM实际调用测试 (SKIP_LLM_TESTS=true)")
	}

	// 初始化真实的LLM模型
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // 增加超时时间
	defer cancel()

	t.Log("==================== 数据分析师简历解析评估测试 ====================")
	startTime := time.Now()

	llmModel, err := initRealLLM(ctx)
	if err != nil {
		// 如果环境变量 LLM_API_KEY 未设置，initRealLLM 会返回错误，此时应跳过测试
		if strings.Contains(err.Error(), "API 密钥不能为空") || strings.Contains(err.Error(), "LLM_API_KEY") {
			t.Skipf("跳过真实LLM测试: %v。请设置 LLM_API_KEY 环境变量。", err)
		}
		t.Fatalf("初始化真实LLM模型失败: %v", err)
	}
	testLoggerForDataAnalyst := log.New(io.Discard, "", 0)
	// 创建LLM简历分块器
	chunker := NewLLMResumeChunker(llmModel, testLoggerForDataAnalyst)

	// 数据分析师简历文本
	resumeText := `王浩宇
15800009999 wang.haoyu.cv@163.com
个人主页: https://haoyu.wang.blog | GitHub: @hywang_dev
求职意向: 数据分析师 / 商业智能 BI 实习生 地点: 杭州优先, 上海可考虑

教育背景
浙江大学 (ZJU) - 985 & 211 工程院校
金融学 (辅修计算机科学) 本科学位
2021年9月 至 2025年6月 (预计)
主要课程: 统计学原理 (95/100), Python数据分析 (92/100), 机器学习导论 (88/100), 计量经济学, 金融市场学。
校级优秀学生干部, 获得二等学业奖学金。

实习经历
ABC科技有限公司 - 数据分析实习生 (远程)
2024年3月 – 2024年5月
- 参与公司电商平台用户行为数据分析项目，协助收集和清洗来自不同渠道的原始数据 (SQL, Excel)。
- 使用 Python (Pandas, NumPy, Matplotlib, Seaborn) 对用户购买路径、转化率等关键指标进行探索性数据分析 (EDA)，并撰写分析报告。
- 协助构建用户画像标签体系，通过 RFM 模型对用户进行分层，为精准营销活动提供数据支持。
- 每周向团队汇报分析进展，并根据反馈迭代分析方案。

XYZ咨询公司 - 商业分析助理 (暑期实习)
2023年7月 – 2023年8月
- 协助咨询顾问进行市场调研，收集特定行业的竞争格局、市场规模等信息。
- 参与撰写行业分析报告的部分章节，主要负责数据图表的制作 (Excel, PowerPoint)。
- 学习并应用SWOT分析、波特五力模型等商业分析工具。

专业技能
数据分析 & 可视化:
熟练掌握 Python (Pandas, NumPy, Scikit-learn) 进行数据处理、建模与分析。
精通 SQL 进行复杂数据查询与提取 (MySQL, PostgreSQL)。
能够使用 Tableau 或 Power BI 进行交互式数据可视化和仪表盘制作。
熟悉常用的统计学方法和机器学习算法 (如回归、分类、聚类)。

编程与其他:
了解 Java 语言基础。
英语 CET-6 (580分)，具备良好的英文文献阅读和沟通能力。
熟练使用 Microsoft Office 套件 (Excel, Word, PowerPoint)。

荣誉奖项
- 2023年 全国大学生数学建模竞赛 省级二等奖
- 2022年 浙江大学"挑战杯"创业计划大赛 校级铜奖
- 2021-2023连续两年 浙江大学优秀学生二等奖学金

自我评价
对数据敏感，逻辑思维清晰，具备较强的学习能力和解决问题的能力。
注重细节，有良好的沟通协调能力和团队合作精神。
渴望在数据驱动决策的领域贡献自己的力量。`

	t.Logf("【原始简历文本】长度: %d 字符", len(resumeText))
	t.Logf("【原始简历文本】前100字符预览: %s...", resumeText[:min(100, len(resumeText))])

	// 打印解析开始时间
	parseStartTime := time.Now()
	t.Logf("【解析开始】时间: %v", parseStartTime.Format("2006-01-02 15:04:05.000"))

	// 调用分块函数
	sections, basicInfo, err := chunker.ChunkResume(ctx, resumeText)

	// 解析结束时间
	parseEndTime := time.Now()
	parseElapsed := parseEndTime.Sub(parseStartTime)
	t.Logf("【解析结束】时间: %v", parseEndTime.Format("2006-01-02 15:04:05.000"))
	t.Logf("【解析耗时】: %.3f 秒", parseElapsed.Seconds())

	require.NoError(t, err, "简历分块应成功完成")

	// 验证结果
	require.NotNil(t, sections, "分块结果不应为nil")
	require.NotNil(t, basicInfo, "基本信息不应为nil")

	// 详细输出基本信息
	t.Logf("\n【基本信息解析】共 %d 项:", len(basicInfo))
	t.Logf("----------------------------------------------------------------------")

	// 按照重要性排序输出基本信息
	importantFields := []string{"name", "phone", "email", "github", "education_level", "job_intention", "location"}
	for _, field := range importantFields {
		if value, ok := basicInfo[field]; ok {
			t.Logf("%-20s: %s", field, value)
		}
	}

	// 输出其他基本信息
	for key, value := range basicInfo {
		if !contains(importantFields, key) {
			t.Logf("%-20s: %s", key, value)
		}
	}

	// 详细输出元数据分析结果
	t.Logf("\n【元数据分析】:")
	t.Logf("----------------------------------------------------------------------")
	schoolInfo := []string{"is_211", "is_985", "is_double_top", "highest_education"}
	for _, field := range schoolInfo {
		if value, ok := basicInfo[field]; ok {
			t.Logf("%-20s: %s", field, value)
		}
	}

	// 经验和评分
	expScoreInfo := []string{"years_of_experience", "resume_score"}
	for _, field := range expScoreInfo {
		if value, ok := basicInfo[field]; ok {
			t.Logf("%-20s: %s", field, value)
		}
	}

	// 技能和标签
	if skills, ok := basicInfo["skills"]; ok {
		t.Logf("【技能列表】: %s", skills)
	}
	if tags, ok := basicInfo["tags"]; ok {
		t.Logf("【简历标签】: %s", tags)
	}

	// 详细输出章节信息
	t.Logf("\n【章节解析】共 %d 个章节:", len(sections))
	t.Logf("----------------------------------------------------------------------")
	for i, section := range sections {
		t.Logf("\n章节 %d:", i+1)
		t.Logf("类型: %s", section.Type)
		t.Logf("标题: %s", section.Title)
		t.Logf("唯一ID: %d", section.ChunkID)
		t.Logf("简历标识: %s", section.ResumeIdentifier)
		t.Logf("内容长度: %d 字符", len(section.Content))

		// 显示内容预览
		previewLen := min(200, len(section.Content))
		t.Logf("内容预览: %s%s",
			section.Content[:previewLen],
			ternary(len(section.Content) > previewLen, "...", ""))
	}

	// 验证基本信息 (基于王浩宇简历的预期)
	t.Logf("\n【断言验证】:")
	t.Logf("----------------------------------------------------------------------")

	passed := 0
	total := 0

	// 验证基本字段
	basicAssertions := map[string]string{
		"name":            "王浩宇",
		"phone":           "15800009999",
		"email":           "wang.haoyu.cv@163.com",
		"education_level": "本科",
		"job_intention":   "数据分析师 / 商业智能 BI 实习生",
	}

	for field, expected := range basicAssertions {
		total++
		if actual, ok := basicInfo[field]; ok && (actual == expected || strings.Contains(actual, expected)) {
			t.Logf("✓ %s = %s", field, actual)
			passed++
		} else {
			t.Logf("✗ %s: 期望包含 %s, 实际 %s", field, expected, actual)
		}
	}

	// 验证元数据
	metaAssertions := map[string]string{
		"is_211":            "true",
		"is_985":            "true",
		"highest_education": "本科",
		"has_intern":        "true",
	}

	for field, expected := range metaAssertions {
		total++
		if actual, ok := basicInfo[field]; ok && actual == expected {
			t.Logf("✓ %s = %s", field, expected)
			passed++
		} else {
			t.Logf("✗ %s: 期望 %s, 实际 %s", field, expected, actual)
		}
	}

	// 验证技能提取
	skillsList := []string{"Python", "SQL", "数据分析", "Pandas"}

	// 查找技能章节
	var skillsSection *types.ResumeSection
	for _, section := range sections {
		if section.Type == types.SectionSkills {
			skillsSection = section
			break
		}
	}

	require.NotNil(t, skillsSection, "应该存在技能章节")

	// 检查技能章节中是否包含特定技能
	for _, skill := range skillsList {
		total++
		if strings.Contains(skillsSection.Content, skill) {
			t.Logf("✓ 技能章节包含 %s", skill)
			passed++
		} else {
			t.Logf("✗ 技能章节中未检测到 %s", skill)
		}
	}

	// 验证章节
	expectedSectionTypes := []types.SectionType{types.SectionEducation, types.SectionSkills, types.SectionInternships, types.SectionAwards}
	foundSections := make(map[types.SectionType]bool)

	for _, section := range sections {
		foundSections[section.Type] = true
		// 检查实习和工作经验是否有内容
		if section.Type == types.SectionInternships {
			assert.NotEmpty(t, section.Content, "实习经历(INTERNSHIPS)章节不应为空")
		}
		if section.Type == types.SectionWorkExperience {
			assert.NotEmpty(t, section.Content, "工作经验(WORK_EXPERIENCE)章节不应为空")
		}
	}

	for _, expectedType := range expectedSectionTypes {
		total++
		if found := foundSections[expectedType]; found {
			t.Logf("✓ 识别出 %s 章节", expectedType)
			passed++
		} else {
			t.Logf("✗ 未识别出 %s 章节", expectedType)
		}
	}

	// 总结
	accuracy := float64(passed) / float64(total) * 100
	t.Logf("\n【测试总结】:")
	t.Logf("----------------------------------------------------------------------")
	t.Logf("总断言数: %d, 通过: %d, 准确率: %.2f%%", total, passed, accuracy)
	t.Logf("总耗时: %.3f 秒", time.Since(startTime).Seconds())
	t.Logf("==================== 测试结束 ====================")
}

// TestLLMWithMixedExperienceResume 测试使用同时包含实习和工作经验的简历
func TestLLMWithMixedExperienceResume(t *testing.T) {
	// 跳过测试，如果设置了环境变量 SKIP_LLM_TESTS
	if os.Getenv("SKIP_LLM_TESTS") == "true" {
		t.Skip("跳过LLM实际调用测试 (SKIP_LLM_TESTS=true)")
	}

	// 初始化真实的LLM模型
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // 增加超时时间
	defer cancel()

	t.Log("==================== 混合经验简历解析评估测试 ====================")
	startTime := time.Now()

	llmModel, err := initRealLLM(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "API 密钥不能为空") || strings.Contains(err.Error(), "LLM_API_KEY") {
			t.Skipf("跳过真实LLM测试: %v。请设置 LLM_API_KEY 环境变量。", err)
		}
		t.Fatalf("初始化真实LLM模型失败: %v", err)
	}
	testLoggerForMixedExp := log.New(io.Discard, "", 0)
	// 创建LLM简历分块器
	chunker := NewLLMResumeChunker(llmModel, testLoggerForMixedExp)

	// 张伟的简历文本 (同时包含实习和工作经验) - 可以稍微调整为揉成一团的格式以增加挑战
	resumeText := `张伟
13587654321 zhang.wei.pro@GMAIL.COM
上海 | 3年工作经验 | 高级软件工程师

教育背景
复旦大学 | 计算机软件与理论 | 硕士 | 2017-09 -- 2020-06

工作经验
BBD 技术有限公司 | 高级后端工程师 | 2021-07 -- 至今
- 负责电商交易核心系统的设计与开发，采用微服务架构 (Spring Cloud, Dubbo)。
- 主导订单履约模块重构，引入领域驱动设计(DDD)，系统QPS提升50%，故障率降低30%。
- 指导初级工程师，进行代码评审和技术分享。

实习经历
CCA 科技有限公司 | 后端开发实习生 | 2019-07 -- 2019-09
- 参与内容管理系统后端API开发，使用 Spring Boot 和 Mybatis。
- 协助完成单元测试和接口文档编写。

专业技能
- 深入理解Java，熟悉JVM原理、并发编程、网络编程。
- 精通Spring Boot, Spring Cloud, Dubbo等微服务框架。
- 熟练使用MySQL, Oracle数据库，有SQL优化经验。
- 熟悉Redis, Kafka, Elasticsearch等常用中间件。
- 了解Docker, Kubernetes等容器化技术。`

	t.Logf("【原始简历文本】长度: %d 字符", len(resumeText))
	t.Logf("【原始简历文本】前100字符预览: %s...", resumeText[:min(100, len(resumeText))])

	// 打印解析开始时间
	parseStartTime := time.Now()
	t.Logf("【解析开始】时间: %v", parseStartTime.Format("2006-01-02 15:04:05.000"))

	// 调用分块函数
	sections, basicInfo, err := chunker.ChunkResume(ctx, resumeText)

	// 解析结束时间
	parseEndTime := time.Now()
	parseElapsed := parseEndTime.Sub(parseStartTime)
	t.Logf("【解析结束】时间: %v", parseEndTime.Format("2006-01-02 15:04:05.000"))
	t.Logf("【解析耗时】: %.3f 秒", parseElapsed.Seconds())

	require.NoError(t, err, "简历分块应成功完成")

	// 验证结果
	require.NotNil(t, sections, "分块结果不应为nil")
	require.NotNil(t, basicInfo, "基本信息不应为nil")

	// 详细输出基本信息
	t.Logf("\n【基本信息解析】共 %d 项:", len(basicInfo))
	t.Logf("----------------------------------------------------------------------")
	importantFields := []string{"name", "phone", "email", "position", "location", "education_level", "years_of_experience"}
	for _, field := range importantFields {
		if value, ok := basicInfo[field]; ok {
			t.Logf("%-20s: %s", field, value)
		}
	}
	for key, value := range basicInfo {
		if !contains(importantFields, key) {
			t.Logf("%-20s: %s", key, value)
		}
	}

	// 详细输出元数据分析结果
	t.Logf("\n【元数据分析】:")
	t.Logf("----------------------------------------------------------------------")
	metaFields := []string{"is_211", "is_985", "is_double_top", "has_intern", "highest_education", "years_of_experience", "resume_score"}
	for _, field := range metaFields {
		if value, ok := basicInfo[field]; ok {
			t.Logf("%-20s: %s", field, value)
		}
	}

	// 详细输出章节信息
	t.Logf("\n【章节解析】共 %d 个章节:", len(sections))
	t.Logf("----------------------------------------------------------------------")
	for i, section := range sections {
		t.Logf("\n章节 %d:", i+1)
		t.Logf("类型: %s", section.Type)
		t.Logf("标题: %s", section.Title)
		t.Logf("唯一ID: %d", section.ChunkID)
		t.Logf("简历标识: %s", section.ResumeIdentifier)
		t.Logf("内容长度: %d 字符", len(section.Content))
		previewLen := min(200, len(section.Content))
		t.Logf("内容预览: %s%s",
			section.Content[:previewLen],
			ternary(len(section.Content) > previewLen, "...", ""))
	}

	// 验证基本信息 (基于张伟简历的预期)
	t.Logf("\n【断言验证】:")
	t.Logf("----------------------------------------------------------------------")

	passed := 0
	total := 0

	// 验证基本字段
	basicAssertions := map[string]string{
		"name":            "张伟",
		"phone":           "13587654321",
		"email":           "zhang.wei.pro@gmail.com", // LLM可能会转小写
		"position":        "高级软件工程师",
		"education_level": "硕士",
	}
	for field, expected := range basicAssertions {
		total++
		actual, ok := basicInfo[field]
		// 对于 email，允许LLM输出小写版本
		if field == "email" && ok && strings.ToLower(actual) == strings.ToLower(expected) {
			t.Logf("✓ %s = %s (忽略大小写匹配)", field, actual)
			passed++
		} else if ok && actual == expected {
			t.Logf("✓ %s = %s", field, actual)
			passed++
		} else {
			t.Logf("✗ %s: 期望 %s, 实际 %s", field, expected, actual)
		}
	}

	// 验证元数据
	metaAssertions := map[string]string{
		"is_211":            "true",
		"is_985":            "true",
		"is_double_top":     "true", // 假设复旦也是双一流
		"has_intern":        "true",
		"highest_education": "硕士",
		// "years_of_experience": "3.0", // LLM对年限的估算可能有浮动，或者从文本直接提取 "3年"
	}
	for field, expected := range metaAssertions {
		total++
		if actual, ok := basicInfo[field]; ok && actual == expected {
			t.Logf("✓ %s = %s", field, expected)
			passed++
		} else {
			t.Logf("✗ %s: 期望 %s, 实际 %s", field, expected, actual)
		}
	}
	// 特殊处理 years_of_experience 因为LLM可能输出 "3" 或 "3.0"
	total++
	if yoe, ok := basicInfo["years_of_experience"]; ok && (yoe == "3" || yoe == "3.0" || strings.Contains(yoe, "3")) {
		t.Logf("✓ years_of_experience 约等于 3年 (实际: %s)", yoe)
		passed++
	} else {
		t.Logf("✗ years_of_experience: 期望约等于 3年, 实际 %s", yoe)
	}

	// 验证技能提取 (检查SKILLS chunk内容)
	skillsList := []string{"Java", "Spring Boot", "Dubbo", "MySQL", "Redis"}
	var skillsSection *types.ResumeSection
	for _, section := range sections {
		if section.Type == types.SectionSkills {
			skillsSection = section
			break
		}
	}
	require.NotNil(t, skillsSection, "应该存在技能章节 (SKILLS)")
	for _, skill := range skillsList {
		total++
		if strings.Contains(skillsSection.Content, skill) {
			t.Logf("✓ 技能章节包含 %s", skill)
			passed++
		} else {
			t.Logf("✗ 技能章节中未检测到 %s (在 '%s' 中)", skill, skillsSection.Title)
		}
	}

	// 验证章节类型
	// 对于张伟的简历，我们期望同时有 EDUCATION, WORK_EXPERIENCE, INTERNSHIPS, SKILLS
	expectedSectionTypes := []types.SectionType{types.SectionEducation, types.SectionWorkExperience, types.SectionInternships, types.SectionSkills}
	foundSections := make(map[types.SectionType]bool)
	for _, section := range sections {
		foundSections[section.Type] = true
		// 检查实习和工作经验是否有内容
		if section.Type == types.SectionInternships {
			assert.NotEmpty(t, section.Content, "实习经历(INTERNSHIPS)章节不应为空")
		}
		if section.Type == types.SectionWorkExperience {
			assert.NotEmpty(t, section.Content, "工作经验(WORK_EXPERIENCE)章节不应为空")
		}
	}

	for _, expectedType := range expectedSectionTypes {
		total++
		if found := foundSections[expectedType]; found {
			t.Logf("✓ 识别出 %s 章节", expectedType)
			passed++
		} else {
			t.Logf("✗ 未识别出 %s 章节", expectedType)
		}
	}

	// 总结
	accuracy := float64(passed) / float64(total) * 100
	t.Logf("\n【测试总结】:")
	t.Logf("----------------------------------------------------------------------")
	t.Logf("总断言数: %d, 通过: %d, 准确率: %.2f%%", total, passed, accuracy)
	t.Logf("总耗时: %.3f 秒", time.Since(startTime).Seconds())
	t.Logf("==================== 测试结束 ====================")
}

// TestLLMChunkerWithRealResume 测试使用真实LLM解析实际简历，并使用不带元数据的Tika解析器
func TestLLMChunkerWithRealResume(t *testing.T) {
	// 跳过测试，如果设置了环境变量 SKIP_LLM_TESTS
	if os.Getenv("SKIP_LLM_TESTS") == "true" {
		t.Skip("跳过LLM实际调用测试 (SKIP_LLM_TESTS=true)")
	}

	// 初始化上下文
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second) // 增加超时时间至90秒
	defer cancel()

	t.Log("==================== 使用真实简历和无元数据Tika解析器的LLM测试 ====================")
	startTime := time.Now()

	// 加载配置
	configPath := "C:/Users/left/Desktop/agent-go/internal/config/config.yaml"
	cfg, err := config.LoadConfigFromFileAndEnv(configPath)
	if err != nil {
		t.Fatalf("加载配置文件失败: %v", err)
	}

	// 1. 创建不带元数据的Tika PDF解析器 (使用函数选项模式)
	t.Log("【步骤1】创建不带元数据的Tika PDF解析器...")
	tikaExtractor := NewTikaPDFExtractor(
		cfg.Tika.ServerURL,
		WithMinimalMetadata(false),                 // 不提取精简元数据
		WithFullMetadata(false),                    // 不提取完整元数据
		WithTimeout(30*time.Second),                // 设置30秒超时
		WithAnnotations(false),                     // 禁用链接注释提取，避免URL重复
		WithTikaLogger(log.New(io.Discard, "", 0)), // 为Tika添加丢弃型logger
	)
	t.Log("Tika解析器创建成功，配置为不提取任何元数据且禁用链接注释提取")

	// 2. 查找测试PDF文件
	testPDFs := []string{
		"testdata/黑白整齐简历模板 (6).pdf",
		"../testdata/黑白整齐简历模板 (6).pdf",
		"../../testdata/黑白整齐简历模板 (6).pdf",
	}

	var pdfPath string
	for _, path := range testPDFs {
		if _, err := os.Stat(path); err == nil {
			pdfPath = path
			break
		}
	}

	if pdfPath == "" {
		t.Fatal("找不到测试PDF文件")
	}
	t.Logf("【步骤2】找到测试PDF文件: %s", pdfPath)

	// 3. 使用无元数据Tika提取器提取PDF文本
	t.Log("【步骤3】使用无元数据Tika提取器提取PDF文本...")
	text, metadata, err := tikaExtractor.ExtractFromFile(ctx, pdfPath)
	if err != nil {
		t.Fatalf("提取PDF文本失败: %v", err)
	}
	t.Logf("PDF文本提取成功，长度: %d 字符", len(text))
	t.Logf("提取的元数据项数: %d (应该只有基本项，无Tika元数据)", len(metadata))

	// 打印元数据
	t.Log("【提取的基本元数据】:")
	for k, v := range metadata {
		t.Logf("  %s: %v", k, v)
	}

	// 打印完整的简历文本
	t.Log("\n【输入的完整简历文本】:")
	t.Log("----------------------------------------------------------------------")
	t.Log(text)
	t.Log("----------------------------------------------------------------------")

	// 4. 初始化真实的LLM模型
	t.Log("【步骤4】初始化真实LLM模型...")
	llmModel, err := initRealLLM(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "API Key") {
			t.Skipf("跳过真实LLM测试: %v。请在配置文件中设置有效的API Key。", err)
		}
		t.Fatalf("初始化LLM模型失败: %v", err)
	}
	t.Log("LLM模型初始化成功")
	testLoggerForRealResumeWithTika := log.New(io.Discard, "", 0)
	// 5. 创建LLM简历分块器
	t.Log("【步骤5】创建LLM简历分块器...")
	chunker := NewLLMResumeChunker(llmModel, testLoggerForRealResumeWithTika)
	t.Log("LLM简历分块器创建成功")

	// 6. 使用LLM解析PDF文本
	t.Log("【步骤6】使用LLM解析简历文本...")
	parseStartTime := time.Now()
	t.Logf("解析开始时间: %v", parseStartTime.Format("2006-01-02 15:04:05.000"))

	sections, basicInfo, err := chunker.ChunkResume(ctx, text)

	parseEndTime := time.Now()
	parseElapsed := parseEndTime.Sub(parseStartTime)
	t.Logf("解析结束时间: %v", parseEndTime.Format("2006-01-02 15:04:05.000"))
	t.Logf("解析耗时: %.3f 秒", parseElapsed.Seconds())

	if err != nil {
		t.Fatalf("LLM解析简历失败: %v", err)
	}
	t.Logf("LLM解析成功，识别出 %d 个章节和 %d 项基本信息", len(sections), len(basicInfo))

	// 打印完整的LLM输出JSON
	t.Log("\n【LLM输出的完整JSON数据】:")
	t.Log("----------------------------------------------------------------------")

	// 重新构建JSON结构以便打印
	jsonOutput := struct {
		BasicInfo map[string]interface{} `json:"basic_info"`
		Chunks    []struct {
			ChunkID          int    `json:"chunk_id"`
			ResumeIdentifier string `json:"resume_identifier"`
			Type             string `json:"type"`
			Title            string `json:"title"`
			Content          string `json:"content"`
		} `json:"chunks"`
		Metadata map[string]interface{} `json:"metadata"`
	}{
		BasicInfo: make(map[string]interface{}),
		Metadata:  make(map[string]interface{}),
	}

	// 填充基本信息
	for k, v := range basicInfo {
		// 过滤出元数据字段
		if k == "is_211" || k == "is_985" || k == "is_double_top" || k == "has_intern" ||
			k == "highest_education" || k == "years_of_experience" || k == "resume_score" || k == "tags" {
			jsonOutput.Metadata[k] = v
		} else {
			jsonOutput.BasicInfo[k] = v
		}
	}

	// 填充章节
	for _, section := range sections {
		jsonOutput.Chunks = append(jsonOutput.Chunks, struct {
			ChunkID          int    `json:"chunk_id"`
			ResumeIdentifier string `json:"resume_identifier"`
			Type             string `json:"type"`
			Title            string `json:"title"`
			Content          string `json:"content"`
		}{
			ChunkID:          section.ChunkID,
			ResumeIdentifier: section.ResumeIdentifier,
			Type:             string(section.Type),
			Title:            section.Title,
			Content:          section.Content,
		})
	}

	// 转换为JSON并打印
	jsonData, err := json.MarshalIndent(jsonOutput, "", "  ")
	if err != nil {
		t.Logf("无法序列化JSON: %v", err)
	} else {
		t.Logf("%s", string(jsonData))
	}
	t.Log("----------------------------------------------------------------------")

	// 7. 打印详细解析结果
	t.Log("\n【基本信息解析结果】:")
	t.Log("----------------------------------------------------------------------")
	importantFields := []string{"name", "phone", "email", "github", "education_level", "job_intention", "location", "position"}
	for _, field := range importantFields {
		if value, ok := basicInfo[field]; ok {
			t.Logf("%-20s: %s", field, value)
		}
	}

	// 打印其他基本信息
	t.Log("\n【其他基本信息】:")
	for key, value := range basicInfo {
		if !contains(importantFields, key) {
			t.Logf("%-20s: %s", key, value)
		}
	}

	// 打印元数据分析
	t.Log("\n【元数据分析】:")
	t.Log("----------------------------------------------------------------------")
	metadataFields := []string{
		"is_211", "is_985", "is_double_top", "has_intern", "has_work_exp",
		"highest_education", "years_of_experience", "resume_score",
		"has_algorithm_award", "has_programming_competition_award",
	}
	for _, field := range metadataFields {
		if value, ok := basicInfo[field]; ok {
			t.Logf("%-30s: %s", field, value)
		}
	}

	// 打印奖项详情（如果存在）
	if algorithmAwards, ok := basicInfo["algorithm_award_titles"]; ok && algorithmAwards != "" {
		t.Log("\n【算法奖项详情】:")
		t.Log(algorithmAwards)
	}

	if programmingAwards, ok := basicInfo["programming_competition_titles"]; ok && programmingAwards != "" {
		t.Log("\n【编程竞赛奖项详情】:")
		t.Log(programmingAwards)
	}

	// 打印标签
	if tags, ok := basicInfo["tags"]; ok && tags != "" {
		t.Log("\n【简历标签】:")
		t.Log(tags)
	}

	// 打印章节信息
	t.Log("\n【章节解析结果】:")
	t.Log("----------------------------------------------------------------------")
	for i, section := range sections {
		t.Logf("\n章节 %d:", i+1)
		t.Logf("类型: %s", section.Type)
		t.Logf("标题: %s", section.Title)
		t.Logf("唯一ID: %d", section.ChunkID)
		t.Logf("简历标识: %s", section.ResumeIdentifier)
		t.Logf("内容长度: %d 字符", len(section.Content))

		// 显示内容预览
		previewLen := min(200, len(section.Content))
		preview := section.Content
		if len(preview) > previewLen {
			preview = preview[:previewLen] + "..."
		}
		t.Logf("内容预览: %s", preview)
	}

	// 8. 总结
	t.Log("\n【测试总结】:")
	t.Log("----------------------------------------------------------------------")
	t.Logf("总测试耗时: %.3f 秒", time.Since(startTime).Seconds())
	t.Logf("PDF文本提取耗时: %.3f 秒", parseStartTime.Sub(startTime).Seconds())
	t.Logf("LLM解析耗时: %.3f 秒", parseElapsed.Seconds())
	t.Log("==================== 测试结束 ====================")
}

// initRealLLM 初始化真实的大语言模型
// 该函数将使用 pkg/agent/model_aliyun_qwen.go 中的 AliyunQwenChatModel
func initRealLLM(ctx context.Context) (model.ToolCallingChatModel, error) {
	// 指定绝对配置文件路径
	configPath := "C:/Users/left/Desktop/agent-go/internal/config/config.yaml"
	log.Printf("加载配置文件: %s", configPath)

	// 尝试加载.env文件（如果存在）
	// 更精确地定位.env文件路径
	currentDir, _ := os.Getwd()
	log.Printf("测试运行时的工作目录: %s", currentDir)

	// 构建多个可能的路径
	envPaths := []string{
		filepath.Join(currentDir, ".env"),               // 当前目录
		".env",                                          // 相对路径
		filepath.Join(currentDir, "../../.env"),         // 假设从internal/api/handler运行
		filepath.Join(filepath.Dir(configPath), ".env"), // 配置文件同级目录
	}

	envLoaded := false
	for _, envPath := range envPaths {
		log.Printf("尝试加载环境变量文件: %s", envPath)
		if _, err := os.Stat(envPath); err == nil {
			if err := godotenv.Load(envPath); err == nil {
				log.Printf("成功加载环境变量文件: %s", envPath)
				envLoaded = true
				break
			} else {
				log.Printf("发现.env文件但加载失败: %v", err)
			}
		}
	}

	if !envLoaded {
		log.Printf("警告: 未能加载任何.env文件")
	}
	// 优先从环境变量获取API密钥
	apiKey := os.Getenv("DASHSCOPE_API_KEY")

	// 读取配置文件
	cfg, err := config.LoadConfigFromFileAndEnv(configPath)
	if err != nil {
		log.Printf("加载配置文件失败: %v", err)
		return nil, fmt.Errorf("加载配置文件失败: %w", err)
	}

	cfg.Aliyun.APIKey = apiKey
	apiURL := cfg.Aliyun.APIURL
	modelName := cfg.Aliyun.Model

	// 安全地打印部分API Key
	maskedKey := "未设置"
	if apiKey != "" {
		if len(apiKey) > 8 {
			maskedKey = apiKey[:4] + "..." + apiKey[len(apiKey)-4:]
		} else {
			maskedKey = "****" // 太短的key只显示星号
		}
	}

	log.Printf("从配置文件加载LLM配置: API Key=%s, URL=%s, Model=%s",
		maskedKey, apiURL, modelName)

	// 检查API Key是否配置
	if apiKey == "" || apiKey == "your_api_key_here" {
		return nil, fmt.Errorf("API Key未在配置文件中配置或配置无效")
	}

	// 如果模型名称为空，设置一个默认值
	if modelName == "" {
		modelName = "qwen-plus" // 或其他合适的默认模型
		log.Printf("LLM模型名称未在配置文件中配置，使用默认值: %s", modelName)
	}

	log.Printf("初始化AliyunQwenChatModel: Model=%s, URL=%s", modelName, apiURL)
	llmModel, err := agent.NewAliyunQwenChatModel(apiKey, modelName, apiURL)
	if err != nil {
		log.Printf("初始化模型失败: %v", err)
		return nil, fmt.Errorf("初始化真实LLM模型失败 (%s): %w", modelName, err)
	}

	log.Printf("真实LLM模型 (%s) 初始化成功", modelName)
	return llmModel, nil
}

// contains 检查字符串切片是否包含指定字符串
func contains(slice []string, str string) bool {
	for _, v := range slice {
		if v == str {
			return true
		}
	}
	return false
}

// ternary 三元运算符替代
func ternary(condition bool, trueVal, falseVal string) string {
	if condition {
		return trueVal
	}
	return falseVal
}

// TestExtractJSON tests the extractJSON utility function
func TestExtractJSON(t *testing.T) {
	testCases := []struct {
		name         string
		inputText    string
		expectedJSON string
	}{
		{
			name:         "Simple JSON object",
			inputText:    `{"key": "value"}`,
			expectedJSON: `{"key": "value"}`,
		},
		{
			name:         "JSON object with leading/trailing whitespace",
			inputText:    `  {"key": "value", "number": 123}  `,
			expectedJSON: `{"key": "value", "number": 123}`,
		},
		{
			name: "JSON wrapped in markdown code block",
			inputText: "Some text before ```json\n" +
				"{\"key\": \"value\", \"nested\": {\"num\": 42}}\n" +
				"``` and after.",
			expectedJSON: `{"key": "value", "nested": {"num": 42}}`,
		},

		{
			name:         "JSON wrapped in markdown code block with extra spaces",
			inputText:    "```json   \n  {\"key\": \"value\"} \n  ```",
			expectedJSON: `{"key": "value"}`,
		},
		{
			name:         "JSON with internal newlines and spaces",
			inputText:    `{"message": "Hello\nWorld", "data": [1, 2, {"info": "  spaced  "}]}`,
			expectedJSON: `{"message": "Hello\nWorld", "data": [1, 2, {"info": "  spaced  "}]}`,
		},
		{
			name:         "No JSON content",
			inputText:    "This is just plain text.",
			expectedJSON: "",
		},
		{
			name:         "Incomplete JSON (missing closing brace)",
			inputText:    `{"key": "value"`,
			expectedJSON: "",
		},
		{
			name:         "Multiple JSON objects (should extract first valid one)",
			inputText:    `{"first": true}{\"second\": false}`,
			expectedJSON: `{"first": true}`,
		},
		{
			name:         "JSON within other text, no markdown",
			inputText:    `Preamble text. {"data": "important"}. Postamble text.`,
			expectedJSON: `{"data": "important"}`,
		},
		{
			name:         "Malformed JSON in markdown (test regex robustness)",
			inputText:    "```json\n{\"key\": value_not_string}\n```",
			expectedJSON: `{"key": value_not_string}`,
		},
		{
			name: "JSON with escaped quotes and newlines in markdown",
			inputText: "```json\n" +
				`{"summary": "This is a summary with a \"quote\" and a newline\nhere."}` +
				"\n```",
			expectedJSON: `{"summary": "This is a summary with a \"quote\" and a newline\nhere."}`,
		},
		{
			name:         "JSON with mixed valid and invalid parts in markdown (extracts first valid block)",
			inputText:    "```json\n{\"valid\": true}\n```\nSome other text and an invalid block: ```json\n{\"invalid\": not_a_string oops\n```",
			expectedJSON: `{"valid": true}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			extracted := extractJSON(tc.inputText)
			require.Equal(t, tc.expectedJSON, extracted)

			// Optionally, try to parse the extracted JSON if we expect it to be valid
			if tc.expectedJSON != "" {
				var jsonData interface{}
				err := json.Unmarshal([]byte(extracted), &jsonData)
				// For "Malformed JSON in markdown", we expect regex to extract, but unmarshal to fail
				if tc.name == "Malformed JSON in markdown (test regex robustness)" {
					require.Error(t, err, "Extracted content for malformed case should not unmarshal cleanly")
				} else {
					require.NoError(t, err, "Extracted JSON string should be valid parsable JSON, but got error: %v. Extracted: '%s'", err, extracted)
				}
			}
		})
	}
}

// TestLLMChunkerWithWorkAndInternship 测试LLM对实习和工作经历的识别能力
func TestLLMChunkerWithWorkAndInternship(t *testing.T) {
	// 跳过测试，如果设置了环境变量 SKIP_LLM_TESTS
	if os.Getenv("SKIP_LLM_TESTS") == "true" {
		t.Skip("跳过LLM实际调用测试 (SKIP_LLM_TESTS=true)")
	}

	// 初始化上下文
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	t.Log("==================== 实习和工作经历识别测试 ====================")

	// 加载配置
	configPath := "C:/Users/left/Desktop/agent-go/internal/config/config.yaml"
	_, err := config.LoadConfigFromFileAndEnv(configPath)
	if err != nil {
		t.Fatalf("加载配置文件失败: %v", err)
	}

	// 1. 初始化真实的LLM模型
	t.Log("【步骤1】初始化LLM模型...")
	llmModel, err := initRealLLM(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "API Key") {
			t.Skipf("跳过真实LLM测试: %v。请在配置文件中设置有效的API Key。", err)
		}
		t.Fatalf("初始化LLM模型失败: %v", err)
	}
	t.Log("LLM模型初始化成功")

	// 2. 创建LLM简历分块器
	t.Log("【步骤2】创建LLM简历分块器...")
	testLogger := log.New(os.Stdout, "[TestLLMChunker] ", log.LstdFlags)
	chunker := NewLLMResumeChunker(llmModel, testLogger)
	t.Log("LLM简历分块器创建成功")

	// 3. 准备测试用例
	t.Log("【步骤3】准备测试用例...")
	testCases := []struct {
		name string
		text string
	}{
		{
			name: "只有实习经历的简历",
			text: `
张实习
13800001234 zhang.shixi@example.com
上海市 应届毕业生 计算机科学

教育经历
上海某大学 计算机科学与技术 本科 2019-2023
GPA: 3.8/4.0

技能专长
编程语言: Python, Java, C++
框架: Spring Boot, Django, React
数据库: MySQL, MongoDB
工具: Git, Docker

实习经历
阿里巴巴 后端开发实习生 2022.06-2022.09
- 参与电商平台后端API开发和优化
- 实现了订单处理模块的重构，提升了30%的处理速度

项目经历
校园二手交易平台 2022.01-2022.05
- 使用Spring Boot和React开发全栈应用
- 设计并实现了商品推荐算法

获奖情况
校级程序设计竞赛二等奖 2021
`,
		},
		{
			name: "同时有实习和工作经历的简历",
			text: `
李工作
13900009876 li.gongzuo@example.com
北京市 软件工程师 5年经验

教育经历
清华大学 软件工程 硕士 2015-2018
北京大学 计算机科学 本科 2011-2015

专业技能
语言: Java, Go, Python, JavaScript
框架: Spring Cloud, Docker, Kubernetes, React
数据库: MySQL, Redis, Elasticsearch
算法: 机器学习, 分布式系统

工作经历
腾讯 高级软件工程师 2020.07-至今
- 负责广告系统核心引擎开发
- 带领5人团队完成平台重构，性能提升200%

百度 软件工程师 2018.06-2020.06
- 参与搜索引擎后端开发
- 优化了索引构建流程，减少50%的构建时间

实习经历
微软亚洲研究院 研究实习生 2017.07-2017.12
- 参与自然语言处理项目研究
- 发表了一篇ACL会议论文

获奖经历
ACM-ICPC 亚洲区域赛铜奖 2014
华为软件精英挑战赛全国50强 2016
`,
		},
	}

	// 4. 对每个测试用例执行测试
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("测试用例: %s", tc.name)
			t.Logf("输入文本长度: %d 字符", len(tc.text))

			// 调用LLM解析
			startTime := time.Now()
			sections, basicInfo, err := chunker.ChunkResume(ctx, tc.text)
			elapsed := time.Since(startTime)

			if err != nil {
				t.Fatalf("LLM解析失败: %v", err)
			}

			t.Logf("解析完成，耗时: %.2f 秒", elapsed.Seconds())
			t.Logf("解析出 %d 个章节和 %d 项基本信息", len(sections), len(basicInfo))

			// 检查并打印关键元数据
			t.Log("\n【关键元数据】:")
			metadataFields := []string{
				"has_intern", "has_work_exp", "is_211", "is_985", "is_double_top",
				"highest_education", "years_of_experience",
			}
			for _, field := range metadataFields {
				value := "未设置"
				if v, ok := basicInfo[field]; ok {
					value = v
				}
				t.Logf("%-20s: %s", field, value)
			}

			// 检查奖项识别
			if v, ok := basicInfo["has_algorithm_award"]; ok && v == "true" {
				t.Log("\n【算法奖项】: 是")
				if titles, ok := basicInfo["algorithm_award_titles"]; ok {
					t.Logf("奖项列表: %s", titles)
				}
			} else {
				t.Log("\n【算法奖项】: 否")
			}

			if v, ok := basicInfo["has_programming_competition_award"]; ok && v == "true" {
				t.Log("\n【编程竞赛奖项】: 是")
				if titles, ok := basicInfo["programming_competition_titles"]; ok {
					t.Logf("奖项列表: %s", titles)
				}
			} else {
				t.Log("\n【编程竞赛奖项】: 否")
			}

			// 检查是否正确识别了chunk类型
			t.Log("\n【识别的chunk类型】:")
			chunkTypes := make(map[types.SectionType]int)
			for _, section := range sections {
				chunkTypes[section.Type]++
			}

			for sectionType, count := range chunkTypes {
				t.Logf("%-20s: %d 个", sectionType, count)
			}

			// 验证实习和工作经历的判断逻辑
			hasInternChunk := false
			hasWorkChunk := false
			for _, section := range sections {
				if section.Type == types.SectionInternships {
					hasInternChunk = true
				}
				if section.Type == types.SectionWorkExperience {
					hasWorkChunk = true
				}
			}

			hasInternMetadata := basicInfo["has_intern"] == "true"
			hasWorkMetadata := basicInfo["has_work_exp"] == "true"

			t.Log("\n【实习和工作经历判断结果验证】:")
			internJudgmentResult := "正确"
			if hasInternChunk != hasInternMetadata {
				internJudgmentResult = "错误❌"
			}
			t.Logf("存在实习chunk: %t, has_intern元数据: %t, 判断%s",
				hasInternChunk, hasInternMetadata, internJudgmentResult)

			workJudgmentResult := "正确"
			if hasWorkChunk != hasWorkMetadata {
				workJudgmentResult = "错误❌"
			}
			t.Logf("存在工作chunk: %t, has_work_exp元数据: %t, 判断%s",
				hasWorkChunk, hasWorkMetadata, workJudgmentResult)

			// 详细打印每个chunk
			t.Log("\n【所有识别的chunks】:")
			for i, section := range sections {
				t.Logf("\nChunk #%d:", i+1)
				t.Logf("Type: %s", section.Type)
				t.Logf("Title: %s", section.Title)
				t.Logf("Content预览: %s", shortenString(section.Content, 100))
			}
		})
	}
}

// shortenString 截断字符串，用于显示预览
func shortenString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
