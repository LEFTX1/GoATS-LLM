package parser

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-agent-go/pkg/agent"
	"ai-agent-go/pkg/config"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// 测试用LLM模型模拟器
type MockJobEvalLLM struct {
	mockResponse string
}

// Generate 实现model.ChatModel接口
func (m *MockJobEvalLLM) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	return &schema.Message{
		Role:    schema.RoleType("assistant"),
		Content: m.mockResponse,
	}, nil
}

// Stream 实现model.ChatModel接口
func (m *MockJobEvalLLM) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, nil
}

// BindTools 实现model.ChatModel接口
func (m *MockJobEvalLLM) BindTools(tools []*schema.ToolInfo) error {
	return nil
}

// TestLLMJobEvaluatorWithRealModel 使用真实LLM模型测试评估功能
func TestLLMJobEvaluatorWithRealModel(t *testing.T) {
	// 跳过测试，如果设置了环境变量 SKIP_LLM_TESTS
	if os.Getenv("SKIP_LLM_TESTS") == "true" {
		t.Skip("跳过LLM实际调用测试 (SKIP_LLM_TESTS=true)")
	}

	// 初始化真实的LLM模型
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	llmModel, err := initRealLLMForJobEvaluatorTest(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "API 密钥不能为空") || strings.Contains(err.Error(), "LLM_API_KEY") {
			t.Skipf("跳过真实LLM测试: %v。请设置 LLM_API_KEY 环境变量。", err)
		}
		t.Fatalf("初始化真实LLM模型失败: %v", err)
	}

	// 创建评估器
	evaluator := NewLLMJobEvaluator(llmModel)

	// 测试评估功能
	jdText := `高级后端开发工程师
要求：
- 5年以上Go语言开发经验
- 精通微服务架构和分布式系统设计
- 深入了解MySQL、Redis、Kafka等中间件
- 有高并发、高可用系统经验
- 熟悉Docker、Kubernetes等容器化技术
- 有大规模系统监控和告警经验`

	resumeText := `李小明
资深后端工程师 | 5年工作经验
技能：
- Go语言开发，精通gin、gorm框架
- 微服务架构设计与实现
- 分布式系统设计，包括分布式锁、分布式事务等
- MySQL数据库优化，索引设计，慢查询分析
- Redis高可用集群搭建与运维
- Kafka消息队列应用与性能调优

工作经历：
某科技公司 | 高级后端工程师 | 2019-至今
- 负责订单系统重构，将单体应用拆分为微服务架构
- 设计实现高并发支付系统，支持每秒千级交易
- 优化数据库性能，提高查询速度30%`

	// 调用评估函数
	result, err := evaluator.Evaluate(ctx, jdText, resumeText)
	require.NoError(t, err, "评估不应返回错误")
	require.NotNil(t, result, "评估结果不应为nil")

	// 验证结果
	assert.GreaterOrEqual(t, result.MatchScore, 0, "匹配分数应为0-100")
	assert.LessOrEqual(t, result.MatchScore, 100, "匹配分数应为0-100")

	// 根据分数范围验证亮点和不足
	if result.MatchScore >= 70 {
		assert.GreaterOrEqual(t, len(result.MatchHighlights), 3, "高分应有至少3个匹配亮点")
	} else if result.MatchScore >= 50 {
		assert.GreaterOrEqual(t, len(result.MatchHighlights), 1, "中分应有至少1个匹配亮点")
	}

	if result.MatchScore < 60 {
		assert.NotEmpty(t, result.PotentialGaps, "低分应有潜在不足")
	}

	assert.NotEmpty(t, result.ResumeSummaryForJD, "简历摘要不应为空")
}

// TestInvalidResponseWithRealModel 测试真实LLM可能出现的错误情况
func TestInvalidResponseWithRealModel(t *testing.T) {
	// 跳过测试，如果设置了环境变量 SKIP_LLM_TESTS
	if os.Getenv("SKIP_LLM_TESTS") == "true" {
		t.Skip("跳过LLM实际调用测试 (SKIP_LLM_TESTS=true)")
	}

	// 初始化真实的LLM模型
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	llmModel, err := initRealLLMForJobEvaluatorTest(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "API 密钥不能为空") || strings.Contains(err.Error(), "LLM_API_KEY") {
			t.Skipf("跳过真实LLM测试: %v。请设置 LLM_API_KEY 环境变量。", err)
		}
		t.Fatalf("初始化真实LLM模型失败: %v", err)
	}

	evaluator := NewLLMJobEvaluator(llmModel)

	// 测试极端情况：提供不合理/不充分的输入，可能导致LLM无法正确生成JSON格式
	// 注：这个测试可能不稳定，因为真实LLM可能仍然尝试生成有效的JSON
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 提供极简/不合理的输入
	result, err := evaluator.Evaluate(ctx, "招聘", "应聘")

	// 这里我们不能严格断言会出错，因为真实LLM可能仍然能生成有效JSON
	// 但我们可以检查结果的合理性
	if err == nil && result != nil {
		// 如果没有出错，检查结果是否合理
		assert.GreaterOrEqual(t, result.MatchScore, 0, "匹配分数应在0-100之间")
		assert.LessOrEqual(t, result.MatchScore, 100, "匹配分数应在0-100之间")
		// 极简简历匹配度应该不高
		assert.LessOrEqual(t, result.MatchScore, 70, "对于极简输入，匹配度不应过高")
	}
}

// TestKeywordMatchingWithRealLLM 使用真实LLM测试关键词匹配和决定性因素处理
func TestKeywordMatchingWithRealLLM(t *testing.T) {
	// 跳过测试，如果设置了环境变量 SKIP_LLM_TESTS
	if os.Getenv("SKIP_LLM_TESTS") == "true" {
		t.Skip("跳过LLM实际调用测试 (SKIP_LLM_TESTS=true)")
	}

	// 初始化真实的LLM模型
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	llmModel, err := initRealLLMForJobEvaluatorTest(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "API 密钥不能为空") || strings.Contains(err.Error(), "LLM_API_KEY") {
			t.Skipf("跳过真实LLM测试: %v。请设置 LLM_API_KEY 环境变量。", err)
		}
		t.Fatalf("初始化真实LLM模型失败: %v", err)
	}
	evaluator := NewLLMJobEvaluator(llmModel)

	// 测试用例：应届生限制
	t.Run("应届生限制", func(t *testing.T) {
		ctx := context.Background()
		jd := "前端开发工程师（2026应届生）：本科及以上学历，计算机相关专业，熟悉React/Vue。"
		resume := "张三，2021年毕业，3年工作经验，熟悉JavaScript，参与过若干Web项目开发。"

		result, err := evaluator.Evaluate(ctx, jd, resume)
		require.NoError(t, err)

		// 预期分数应该较低，因为不符合应届生要求
		assert.LessOrEqual(t, result.MatchScore, 50, "不符合应届生要求应得低分")

		// 检查不足点中是否提到应届生要求
		foundGraduationIssue := false
		for _, gap := range result.PotentialGaps {
			if strings.Contains(strings.ToLower(gap), "届") ||
				strings.Contains(strings.ToLower(gap), "毕业") ||
				strings.Contains(strings.ToLower(gap), "应届") {
				foundGraduationIssue = true
				break
			}
		}
		assert.True(t, foundGraduationIssue, "应当指出不符合应届生要求")
	})

	// 测试用例：必备技能不符
	t.Run("必备技能不符", func(t *testing.T) {
		ctx := context.Background()
		jd := "后端开发工程师：5年以上开发经验，必须精通Go语言，熟悉微服务架构，有高并发系统经验。"
		resume := "李四，5年Java后端开发经验，熟悉Spring全家桶，了解微服务架构，使用过MySQL和Redis。"

		result, err := evaluator.Evaluate(ctx, jd, resume)
		require.NoError(t, err)

		// 预期分数应该一般，因为没有Go语言经验
		assert.LessOrEqual(t, result.MatchScore, 70, "缺少核心技能应得中低分")

		// 检查不足点中是否提到Go语言
		foundSkillIssue := false
		for _, gap := range result.PotentialGaps {
			if strings.Contains(strings.ToLower(gap), "go") {
				foundSkillIssue = true
				break
			}
		}
		assert.True(t, foundSkillIssue, "应当指出缺少Go语言经验")
	})
}

// TestRealLLMJobEvaluator 使用真实LLM模型测试岗位-简历匹配评估
func TestRealLLMJobEvaluator(t *testing.T) {
	// 跳过测试，如果设置了环境变量 SKIP_LLM_TESTS
	if os.Getenv("SKIP_LLM_TESTS") == "true" {
		t.Skip("跳过LLM实际调用测试 (SKIP_LLM_TESTS=true)")
	}

	// 初始化真实的LLM模型
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second) // 给LLM调用足够的时间，增加超时时间
	defer cancel()

	t.Log("==================== 真实LLM岗位-简历匹配评估测试 ====================")
	startTime := time.Now()

	llmModel, err := initRealLLMForJobEvaluatorTest(ctx)
	if err != nil {
		// 如果环境变量 LLM_API_KEY 未设置，initRealLLMForJobEvaluatorTest 会返回错误，此时应跳过测试
		if strings.Contains(err.Error(), "API 密钥不能为空") || strings.Contains(err.Error(), "LLM_API_KEY") {
			t.Skipf("跳过真实LLM测试: %v。请设置 LLM_API_KEY 环境变量。", err)
		}
		t.Fatalf("初始化真实LLM模型失败: %v", err)
	}

	// 创建评估器
	evaluator := NewLLMJobEvaluator(llmModel)

	// 测试数据集
	testCases := []struct {
		name          string
		jdTitle       string
		jd            string
		resumeTitle   string
		resume        string
		expectedScore string // 预期分数范围，如"85-95"，用于检查而不是严格断言
	}{
		{
			name:    "前端与前端匹配",
			jdTitle: "前端开发岗位",
			jd: `高级前端开发工程师

职位描述：
- 负责公司核心业务前端开发，包括Web端和移动端H5页面
- 参与需求分析和技术方案设计，提升用户体验和页面性能
- 与设计团队、后端团队协作，推动产品快速迭代

职位要求：
1. 本科及以上学历，计算机相关专业，3年以上前端开发经验
2. 精通HTML5/CSS3/JavaScript，熟悉ES6+语法和特性
3. 精通React或Vue框架，熟悉组件化开发和状态管理
4. 熟悉前端构建工具如Webpack、Vite等，了解前端工程化实践
5. 有移动端H5、小程序、ReactNative等开发经验优先
6. 良好的团队协作能力和沟通能力，有较强的学习能力和解决问题的能力`,
			resumeTitle: "前端工程师简历",
			resume: `王浩宇
前端开发工程师 | 4年工作经验 | 15800009999 | wang.haoyu.cv@163.com

教育背景
浙江大学 | 计算机科学与技术 | 本科学位 | 2016-2020

专业技能
- 精通HTML5/CSS3/JavaScript，熟练掌握ES6+新特性
- 精通React框架及其生态(Redux、React Router等)，熟悉Vue.js
- 熟悉前端工程化，包括Webpack、Babel等构建工具
- 熟练使用TypeScript进行大型项目开发
- 了解Node.js和Express框架，能够开发简单的后端API
- 熟悉微信小程序开发，有实际项目经验

工作经验
ABC科技 | 高级前端开发工程师 | 2022.03-至今
- 负责公司电商平台核心模块的开发与重构，使用React + TypeScript开发
- 主导前端性能优化，通过代码分割、懒加载等策略，页面加载速度提升50%
- 设计并实现组件库，提升团队开发效率，组件复用率提高30%
- 参与跨团队技术评审，制定前端开发规范

XYZ互联网 | 前端开发工程师 | 2020.07-2022.02
- 负责公司社交产品Web端和H5端的开发，使用Vue.js框架
- 开发了图片处理、视频播放等核心功能模块
- 参与小程序开发，实现了用户注册、登录、内容发布等功能
- 协助进行前端自动化测试，提高产品质量`,
			expectedScore: "85-95", // 预期高分
		},
		{
			name:    "后端与后端匹配",
			jdTitle: "后端开发岗位",
			jd: `资深后端开发工程师

职位描述：
我们正在寻找一位资深的后端开发工程师，加入我们的技术团队，负责设计、开发和维护我们的核心服务端系统。

岗位职责：
- 负责公司核心业务系统的设计和开发
- 设计高可用、高性能的分布式系统架构
- 优化现有系统，提升系统稳定性和性能
- 与前端团队配合，设计和实现RESTful API
- 编写技术文档，参与技术评审

任职要求：
1. 计算机相关专业本科及以上学历，4年以上后端开发经验
2. 精通Java或Go语言，熟悉常用的设计模式和数据结构
3. 熟悉分布式系统设计，了解微服务架构
4. 精通MySQL等关系型数据库，熟悉SQL优化和索引设计
5. 熟练使用Redis, Kafka等中间件，了解其原理和最佳实践
6. 熟悉Linux操作系统和Docker容器技术
7. 有高并发、高可用系统设计和实现经验者优先`,
			resumeTitle: "后端工程师简历",
			resume: `张大明
资深后端工程师 | 5年工作经验 | 13711223344 | zhang.daming@corp.com

教育背景:
清华大学 | 计算机科学与技术 | 硕士 | 2015-2018

专业技能:
- 精通Java语言，熟悉Spring Boot/Cloud和微服务架构
- 熟悉Go语言，使用过Gin框架开发高性能服务
- 深入了解MySQL, PostgreSQL数据库，熟悉数据库设计和优化
- 熟练使用Redis, Kafka, ElasticSearch等中间件
- 熟悉Docker, Kubernetes等容器化技术
- 了解分布式系统设计原则，有高并发系统实战经验

工作经历:
互联网科技有限公司 | 高级后端工程师 | 2020.06-至今
- 负责核心交易系统的设计和开发，支持日均千万级交易量
- 主导订单系统重构，将单体应用拆分为微服务架构，系统可用性从99%提升到99.99%
- 设计并实现分布式限流和降级方案，确保系统在峰值流量下稳定运行
- 优化数据库查询性能，大型报表查询耗时减少80%

移动科技有限公司 | 后端开发工程师 | 2018.07-2020.05
- 负责用户系统和权限模块的开发和维护
- 实现基于Redis的分布式缓存方案，显著降低数据库压力
- 参与消息推送系统设计，支持千万级用户的实时消息推送
- 编写单元测试和集成测试，提升代码质量和稳定性`,
			expectedScore: "85-95", // 预期高分
		},
		{
			name:    "前端与后端不匹配",
			jdTitle: "前端开发岗位",
			jd: `高级前端开发工程师

职位描述：
- 负责公司核心业务前端开发，包括Web端和移动端H5页面
- 参与需求分析和技术方案设计，提升用户体验和页面性能
- 与设计团队、后端团队协作，推动产品快速迭代

职位要求：
1. 本科及以上学历，计算机相关专业，3年以上前端开发经验
2. 精通HTML5/CSS3/JavaScript，熟悉ES6+语法和特性
3. 精通React或Vue框架，熟悉组件化开发和状态管理
4. 熟悉前端构建工具如Webpack、Vite等，了解前端工程化实践
5. 有移动端H5、小程序、ReactNative等开发经验优先
6. 良好的团队协作能力和沟通能力，有较强的学习能力和解决问题的能力`,
			resumeTitle: "后端工程师简历",
			resume: `张大明
资深后端工程师 | 5年工作经验 | 13711223344 | zhang.daming@corp.com

教育背景:
清华大学 | 计算机科学与技术 | 硕士 | 2015-2018

专业技能:
- 精通Java语言，熟悉Spring Boot/Cloud和微服务架构
- 熟悉Go语言，使用过Gin框架开发高性能服务
- 深入了解MySQL, PostgreSQL数据库，熟悉数据库设计和优化
- 熟练使用Redis, Kafka, ElasticSearch等中间件
- 熟悉Docker, Kubernetes等容器化技术
- 了解分布式系统设计原则，有高并发系统实战经验

工作经历:
互联网科技有限公司 | 高级后端工程师 | 2020.06-至今
- 负责核心交易系统的设计和开发，支持日均千万级交易量
- 主导订单系统重构，将单体应用拆分为微服务架构，系统可用性从99%提升到99.99%
- 设计并实现分布式限流和降级方案，确保系统在峰值流量下稳定运行
- 优化数据库查询性能，大型报表查询耗时减少80%

移动科技有限公司 | 后端开发工程师 | 2018.07-2020.05
- 负责用户系统和权限模块的开发和维护
- 实现基于Redis的分布式缓存方案，显著降低数据库压力
- 参与消息推送系统设计，支持千万级用户的实时消息推送
- 编写单元测试和集成测试，提升代码质量和稳定性`,
			expectedScore: "30-50", // 预期低分
		},
		{
			name:    "应届生限制测试",
			jdTitle: "前端开发(应届生)岗位",
			jd: `前端开发工程师（2026应届生）

职位描述：
- 参与公司产品的前端开发工作
- 与设计团队协作，实现高质量的用户界面
- 参与前端技术选型和架构设计

职位要求：
1. 计算机相关专业，本科及以上学历，2026届应届毕业生
2. 熟悉HTML5/CSS3/JavaScript基础知识
3. 了解至少一种前端框架（React/Vue/Angular等）
4. 有前端项目实习经验者优先
5. 有较强的学习能力和团队合作精神`,
			resumeTitle: "有经验前端工程师简历",
			resume: `李明
前端工程师 | 3年工作经验 | 13988889999 | li.ming@example.com

教育背景
北京大学 | 计算机科学 | 本科 | 2018-2022

专业技能
- 精通HTML5/CSS3/JavaScript，熟悉ES6+语法
- 熟练使用React框架和相关生态
- 了解Node.js后端开发
- 熟悉Git版本控制

工作经验
科技有限公司 | 前端开发工程师 | 2022.07-至今
- 参与电商平台前端开发
- 负责多个核心页面的重构和性能优化
- 开发可复用的UI组件库`,
			expectedScore: "30-45", // 预期低分，因为不符合应届生要求
		},
		{
			name:    "核心技能缺失测试",
			jdTitle: "必须精通React的前端岗位",
			jd: `高级前端开发工程师

职位描述：
负责公司核心产品的前端开发工作

职位要求：
1. 本科及以上学历，3年以上前端开发经验
2. 必须精通React框架及其生态系统，有丰富的React实践经验
3. 熟悉现代前端工程化工具和实践
4. 良好的团队协作能力`,
			resumeTitle: "Vue开发者简历",
			resume: `赵强
前端开发工程师 | 4年工作经验

教育背景
武汉大学 | 软件工程 | 本科 | 2016-2020

专业技能
- 精通Vue.js全家桶，熟练使用Vue CLI、Vuex、Vue Router
- 熟悉HTML5/CSS3/JavaScript/ES6+
- 熟悉Webpack、Babel等构建工具
- 了解Node.js和Express框架

工作经验
科技公司 | 前端开发 | 2020-至今
- 负责公司产品的Vue.js前端开发
- 开发和维护核心页面组件
- 参与前端架构优化`,
			expectedScore: "50-65", // 中等分数，技能不完全匹配
		},
		{
			name:    "学历要求不符测试",
			jdTitle: "硕士及以上学历要求岗位",
			jd: `算法工程师

职位描述：
负责公司AI算法的研发和优化

职位要求：
1. 硕士及以上学历，计算机、人工智能相关专业
2. 熟悉机器学习算法和深度学习框架
3. 有相关项目经验
4. 良好的英文读写能力`,
			resumeTitle: "本科学历算法工程师简历",
			resume: `王小明
算法工程师 | 1年工作经验

教育背景
某大学 | 计算机科学 | 本科 | 2018-2022

专业技能
- 熟悉Python编程，了解TensorFlow、PyTorch等框架
- 有机器学习项目经验
- 了解常用数据结构和算法
- 英语CET-6

工作经验
某AI公司 | 初级算法工程师 | 2022-至今
- 参与图像识别算法开发
- 协助进行模型训练和调优`,
			expectedScore: "40-55", // 中低分，学历不符合要求
		},
	}

	t.Log("【测试数据准备】测试多种匹配场景:")
	for i, tc := range testCases {
		t.Logf("%d. %s", i+1, tc.name)
	}

	// 运行所有测试场景
	for _, tc := range testCases {
		testJobResumeMatch(t, ctx, evaluator, tc.jdTitle, tc.jd, tc.resumeTitle, tc.resume)

		// 通过CheckScore函数检查分数是否在预期范围内
		// (这个函数需要单独定义，参见下面的实现)
	}

	// 总结
	t.Logf("\n【测试总结】:")
	t.Logf("----------------------------------------------------------------------")
	t.Logf("总耗时: %.3f 秒", time.Since(startTime).Seconds())
	t.Logf("==================== 测试结束 ====================")
}

// 检查分数是否在预期范围内的辅助函数
func checkScoreInRange(t *testing.T, score int, expectedRange string) bool {
	if expectedRange == "" {
		return true // 没有预期范围，不做检查
	}

	parts := strings.Split(expectedRange, "-")
	if len(parts) != 2 {
		t.Logf("警告: 无效的分数范围格式: %s", expectedRange)
		return false
	}

	min, err1 := strconv.Atoi(parts[0])
	max, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		t.Logf("警告: 无效的分数范围值: %s", expectedRange)
		return false
	}

	inRange := score >= min && score <= max
	if !inRange {
		t.Logf("警告: 分数 %d 不在预期范围 %s 内", score, expectedRange)
	}
	return inRange
}

// testJobResumeMatch 测试特定的岗位与简历匹配
func testJobResumeMatch(t *testing.T, ctx context.Context, evaluator *LLMJobEvaluator, jobTitle string, jobDesc string, resumeTitle string, resumeText string) {
	t.Logf("\n【%s - %s】:", jobTitle, resumeTitle)
	t.Logf("----------------------------------------------------------------------")

	// 记录调用开始时间
	startTime := time.Now()
	t.Logf("调用开始时间: %s", startTime.Format("15:04:05.000"))

	// 调用评估函数
	result, err := evaluator.Evaluate(ctx, jobDesc, resumeText)

	// 记录调用结束时间
	endTime := time.Now()
	t.Logf("调用结束时间: %s", endTime.Format("15:04:05.000"))
	t.Logf("调用耗时: %.3f 秒", endTime.Sub(startTime).Seconds())

	require.NoError(t, err, "评估不应返回错误")
	require.NotNil(t, result, "评估结果不应为nil")

	// 输出详细的评估结果
	t.Logf("\n匹配分数: %d/100", result.MatchScore)

	t.Logf("\n匹配亮点 (%d条):", len(result.MatchHighlights))
	for i, highlight := range result.MatchHighlights {
		t.Logf("  %d. %s", i+1, highlight)
	}

	t.Logf("\n潜在不足 (%d条):", len(result.PotentialGaps))
	if len(result.PotentialGaps) > 0 {
		for i, gap := range result.PotentialGaps {
			t.Logf("  %d. %s", i+1, gap)
		}
	} else {
		t.Logf("  (无)")
	}

	t.Logf("\n简历摘要:")
	t.Logf("  %s", result.ResumeSummaryForJD)

	// 基本断言
	assert.GreaterOrEqual(t, result.MatchScore, 0, "匹配分数应该大于等于0")
	assert.LessOrEqual(t, result.MatchScore, 100, "匹配分数应该小于等于100")
	// 只有当匹配分数≥50时才检查匹配亮点是否为空
	if result.MatchScore >= 50 {
		assert.NotEmpty(t, result.MatchHighlights, "匹配亮点不应为空")
	}
	assert.NotEmpty(t, result.ResumeSummaryForJD, "简历摘要不应为空")

	// 交叉匹配的情况，分数应该较低
	if (jobTitle == "前端开发岗位" && resumeTitle == "后端工程师简历") ||
		(jobTitle == "后端开发岗位" && resumeTitle == "前端工程师简历") {
		t.Logf("\n【交叉匹配验证】")
		if result.MatchScore < 60 {
			t.Logf("✓ 匹配分数较低 (%d)，符合预期", result.MatchScore)
		} else {
			t.Logf("! 匹配分数较高 (%d)，可能不符合预期", result.MatchScore)
		}
	}
}

// initRealLLMForJobEvaluatorTest 初始化真实的大语言模型
func initRealLLMForJobEvaluatorTest(ctx context.Context) (model.ChatModel, error) {
	// 打印当前工作目录，帮助定位配置文件路径问题
	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("警告: 无法获取当前工作目录: %v", err)
	} else {
		log.Printf("当前工作目录: %s", cwd)
	}

	// 尝试加载配置
	var apiKey, apiURL, modelName string
	var cfg *config.Config

	// 1. 从环境变量获取
	apiKey = os.Getenv("LLM_API_KEY")
	apiURL = os.Getenv("LLM_API_URL")
	modelName = os.Getenv("LLM_MODEL_NAME")

	// 2. 如果环境变量中没有，尝试从配置文件获取
	if apiKey == "" {
		// 尝试直接加载配置文件
		configPath := findConfigFile()
		if configPath != "" {
			log.Printf("找到配置文件: %s", configPath)
			// 读取配置文件
			content, err := os.ReadFile(configPath)
			if err == nil {
				cfg = &config.Config{}
				if err := yaml.Unmarshal(content, cfg); err == nil {
					if cfg.Aliyun.APIKey != "" {
						apiKey = cfg.Aliyun.APIKey
						log.Printf("从配置文件获取API Key")
					}
					if cfg.Aliyun.APIURL != "" && apiURL == "" {
						apiURL = cfg.Aliyun.APIURL
					}

					// 获取任务特定模型：为评估任务使用专用模型
					taskModelName := cfg.GetModelForTask("job_evaluate")
					if taskModelName != "" {
						modelName = taskModelName
						log.Printf("使用任务专用模型: %s", modelName)
					} else if cfg.Aliyun.Model != "" && modelName == "" {
						modelName = cfg.Aliyun.Model
					}
				}
			}
		}
	}

	// 3. 检查是否有有效的API Key
	if apiKey == "" || apiKey == "your_api_key_here" || apiKey == "test_api_key" {
		return nil, fmt.Errorf("未找到有效的API Key。请设置LLM_API_KEY环境变量或在config.yaml中配置")
	}

	// 4. 设置默认值
	if apiURL == "" {
		apiURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
		log.Printf("使用默认API URL: %s", apiURL)
	}

	if modelName == "" {
		modelName = "qwen-turbo"
		log.Printf("使用默认模型: %s", modelName)
	}

	// 5. 创建LLM模型
	log.Printf("初始化LLM模型: %s", modelName)
	llmModel, err := agent.NewAliyunQwenChatModel(apiKey, modelName, apiURL)
	if err != nil {
		return nil, fmt.Errorf("创建LLM模型失败: %w", err)
	}

	return llmModel, nil
}

// findConfigFile 查找配置文件
func findConfigFile() string {
	// 在常见位置查找配置文件
	searchPaths := []string{
		"config.yaml",
		"./config.yaml",
		"../config.yaml",
		"../../config.yaml",
	}

	// 获取当前可执行文件路径
	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)
		// 添加可执行文件所在目录
		searchPaths = append(searchPaths, filepath.Join(execDir, "config.yaml"))
	}

	// 获取工作目录
	workDir, err := os.Getwd()
	if err == nil {
		// 添加可能的项目根目录
		projectRoots := []string{
			workDir,
			filepath.Join(workDir, ".."),
			filepath.Join(workDir, "..", ".."),
		}
		for _, root := range projectRoots {
			searchPaths = append(searchPaths, filepath.Join(root, "config.yaml"))
		}
	}

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			absPath, _ := filepath.Abs(path)
			return absPath
		}
	}

	return ""
}

// 添加新的测试用例，检测不同匹配度下的评估质量
func TestMatchScoreRangeValidation(t *testing.T) {
	testCases := []struct {
		name           string
		score          int
		highlights     []string
		gaps           []string
		summary        string
		expectError    bool
		errorSubstring string
	}{
		{
			name:        "完美匹配，95分",
			score:       95,
			highlights:  []string{"亮点1", "亮点2", "亮点3", "亮点4"},
			gaps:        []string{},
			summary:     "非常匹配的候选人",
			expectError: false,
		},
		{
			name:        "优秀匹配，85分",
			score:       85,
			highlights:  []string{"亮点1", "亮点2", "亮点3"},
			gaps:        []string{"小缺点"},
			summary:     "优秀的候选人",
			expectError: false,
		},
		{
			name:           "高分但亮点太少",
			score:          90,
			highlights:     []string{"亮点1", "亮点2"},
			gaps:           []string{},
			summary:        "亮点不足的高分候选人",
			expectError:    true,
			errorSubstring: "unusually few match_highlights",
		},
		{
			name:           "低分但无不足点",
			score:          55,
			highlights:     []string{"亮点1"},
			gaps:           []string{},
			summary:        "无不足点的低分候选人",
			expectError:    true,
			errorSubstring: "potential_gaps should not be empty",
		},
		{
			name:           "亮点超过限制",
			score:          80,
			highlights:     []string{"亮点1", "亮点2", "亮点3", "亮点4", "亮点5", "亮点6"},
			gaps:           []string{"缺点"},
			summary:        "亮点过多的候选人",
			expectError:    true,
			errorSubstring: "match_highlights should not contain more than 5",
		},
		{
			name:           "不足点超过限制",
			score:          60,
			highlights:     []string{"亮点1", "亮点2"},
			gaps:           []string{"缺点1", "缺点2", "缺点3", "缺点4"},
			summary:        "缺点过多的候选人",
			expectError:    true,
			errorSubstring: "potential_gaps should not contain more than 3",
		},
		{
			name:           "摘要过长",
			score:          75,
			highlights:     []string{"亮点1", "亮点2", "亮点3"},
			gaps:           []string{"缺点1"},
			summary:        strings.Repeat("很长的摘要内容测试ABCDEFG", 15), // 超过200个rune
			expectError:    true,
			errorSubstring: "resume_summary_for_jd is too long",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			evaluation := &LLMJobMatchEvaluation{
				MatchScore:         tc.score,
				MatchHighlights:    tc.highlights,
				PotentialGaps:      tc.gaps,
				ResumeSummaryForJD: tc.summary,
			}

			err := validateEvaluationResult(evaluation)

			if tc.expectError {
				require.Error(t, err, "应该返回错误")
				if tc.errorSubstring != "" && err != nil {
					assert.Contains(t, err.Error(), tc.errorSubstring, "错误消息应包含预期子字符串")
				}
			} else {
				assert.NoError(t, err, "不应返回错误")
			}
		})
	}
}
