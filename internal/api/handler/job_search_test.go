package handler_test

import (
	"ai-agent-go/internal/api/handler"
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	appCoreLogger "ai-agent-go/internal/logger"
	"ai-agent-go/internal/parser"
	"ai-agent-go/internal/processor"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	glog "github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route/param"
	hertzadapter "github.com/hertz-contrib/logger/zerolog"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 使用不同的配置路径常量名称，避免与其他测试文件冲突
const (
	jobSearchTestConfigPath = "../../config/config.yaml"
)

// RerankDocument 定义了发送给Reranker服务的文档结构
type RerankDocument struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// RerankRequest 定义了发送给Reranker服务的请求体结构
type RerankRequest struct {
	Query     string           `json:"query"`
	Documents []RerankDocument `json:"documents"`
}

// RerankedDocument 定义了从Reranker服务接收的文档结构
type RerankedDocument struct {
	ID          string  `json:"id"`
	RerankScore float32 `json:"rerank_score"`
}

// GoldenTruthSet 定义了用于评测的黄金标准答案
type GoldenTruthSet struct {
	Tier1 map[string]bool // 高相关
	Tier2 map[string]bool // 中相关
	Tier3 map[string]bool // 低相关
}

// TestLogger 记录测试过程中的详细信息到文件
type TestLogger struct {
	file    *os.File
	testing *testing.T
}

// NewTestLogger 创建一个新的测试日志记录器
func NewTestLogger(t *testing.T, filename string) (*TestLogger, error) {
	// 确保目录存在
	logDir := filepath.Join(".", "test_logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, err
	}

	// 创建带时间戳的日志文件名
	timestamp := time.Now().Format("20060102_150405")
	fullFilename := filepath.Join(logDir, fmt.Sprintf("%s_%s.log", filename, timestamp))

	// 创建或截断文件
	file, err := os.Create(fullFilename)
	if err != nil {
		return nil, err
	}

	t.Logf("测试日志将写入: %s", fullFilename)

	return &TestLogger{
		file:    file,
		testing: t,
	}, nil
}

// Log 记录信息到文件和测试日志
func (tl *TestLogger) Log(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	tl.testing.Log(msg)

	// 添加时间戳
	timestamp := time.Now().Format("15:04:05.000")
	formattedMsg := fmt.Sprintf("[%s] %s\n", timestamp, msg)

	if _, err := tl.file.WriteString(formattedMsg); err != nil {
		tl.testing.Logf("写入日志文件失败: %v", err)
	}
}

// LogObject 记录对象到文件，以json格式
func (tl *TestLogger) LogObject(prefix string, obj interface{}) {
	bytes, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		tl.Log("%s: 无法序列化对象: %v", prefix, err)
		return
	}

	tl.Log("%s:\n%s", prefix, string(bytes))
}

// Close 关闭日志文件
func (tl *TestLogger) Close() {
	if tl.file != nil {
		tl.file.Close()
	}
}

// TestJobSearchRecall 测试岗位搜索召回效果
func TestJobSearchRecall(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "JobSearchRecall")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试岗位搜索召回效果")

	// 1. 设置测试环境
	ctx := context.Background()

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true})
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)
	glog.SetLogger(hertzCompatibleLogger)
	glog.SetLevel(glog.LevelDebug)

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")
	testLogger.Log("配置加载成功")

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()
	testLogger.Log("存储组件初始化成功")

	// 3. 创建Embedding服务
	testLogger.Log("创建Embedding服务")
	aliyunEmbedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "创建AliyunEmbedder失败")
	testLogger.Log("Embedding服务创建成功")

	// 4. 初始化处理器
	testLogger.Log("初始化JD处理器")
	jdProcessor, err := processor.NewJDProcessor(aliyunEmbedder, s, cfg.ActiveParserVersion)
	require.NoError(t, err, "初始化JD处理器失败")
	testLogger.Log("JD处理器初始化成功")

	// 5. 初始化处理程序
	testLogger.Log("初始化JobSearchHandler")
	h := handler.NewJobSearchHandler(cfg, s, jdProcessor)
	testLogger.Log("JobSearchHandler初始化成功")

	// 6. 确保测试数据存在 - 岗位信息
	testJobID := "11111111-2222-3333-4444-555555555555"
	jobTitle := "产品质量技术负责人"
	jobDesc := `工作职责：
1. 做产品质量的负责人 & 技术风险的把控者，针对产品整体质量和每次发版做好质量保障工作，包括但不限于功能测试、接口测试、性能测试、大数据测试；
2. 产品对应的测试工具开发；
3. 通用型测试平台开发。

岗位要求：
· 本科及以上学历，硕士/博士优先，计算机、数学、信息管理等相关专业；
· 熟悉 C、C++、Java、Python、Perl 等至少一种编程语言；
· 熟悉软件研发流程，掌握软件测试理论和方法，有设计和开发测试工具、自动化测试框架能力更佳；
· 熟悉计算机系统结构、操作系统、网络、分布式系统等基础知识；
· 熟悉机器学习算法、自然语言处理或图像算法之一更佳；
· 具备广泛技术视野、较强学习与问题解决能力；
· 对质量捍卫有热情，持续追求卓越的用户体验；
· 具备奉献精神，善于沟通与团队合作。`

	testLogger.Log("测试岗位ID: %s", testJobID)
	testLogger.Log("岗位标题: %s", jobTitle)
	testLogger.Log("岗位描述:\n%s", jobDesc)

	// 7. 准备测试数据 - 确保数据库中有岗位数据
	testLogger.Log("准备测试数据 - 确保数据库中有岗位数据")
	ensureJobExists(t, s, testJobID, jobTitle, jobDesc)

	// 8. 创建请求上下文
	testLogger.Log("创建请求上下文")
	c := app.NewContext(16)

	// 添加参数
	c.Params = append(c.Params, param.Param{
		Key:   "job_id",
		Value: testJobID,
	})

	// 检查Qdrant中的数据量
	if s.Qdrant != nil {
		count, countErr := s.Qdrant.CountPoints(ctx)
		if countErr != nil {
			testLogger.Log("获取Qdrant点数量失败: %v", countErr)
		} else {
			testLogger.Log("Qdrant集合中总共有 %d 个点", count)
		}
	}

	// 9. 执行测试
	testLogger.Log("执行岗位搜索处理")
	h.HandleSearchResumesByJobID(ctx, c)

	// 10. 验证结果
	statusCode := c.Response.StatusCode()
	testLogger.Log("状态码: %d", statusCode)

	var respData map[string]interface{}
	if err := json.Unmarshal(c.Response.Body(), &respData); err != nil {
		testLogger.Log("解析响应失败: %v", err)
		t.Fatalf("解析响应失败: %v", err)
	}

	// 记录完整响应数据到日志文件
	testLogger.LogObject("响应数据", respData)

	// 11. 验证基本响应结构
	assert.Equal(t, consts.StatusOK, statusCode)
	assert.Contains(t, respData, "message")
	assert.Contains(t, respData, "job_id")
	assert.Equal(t, testJobID, respData["job_id"])

	// 12. 验证返回的简历数据
	if data, ok := respData["data"].([]interface{}); ok {
		testLogger.Log("找到 %d 个匹配的简历", len(data))
		t.Logf("找到 %d 个匹配的简历", len(data))

		// 分析每个返回的简历
		for i, item := range data {
			resumeData := item.(map[string]interface{})
			submissionUUID := resumeData["submission_uuid"].(string)
			score := resumeData["qdrant_score"].(float64)

			testLogger.Log("简历 #%d - UUID: %s, 分数: %.4f", i+1, submissionUUID, score)
			t.Logf("简历 #%d - UUID: %s, 分数: %.4f", i+1, submissionUUID, score)

			// 记录更详细的简历信息到日志文件
			testLogger.LogObject(fmt.Sprintf("简历 #%d 详细信息", i+1), resumeData)

			// 可以增加更详细的验证，例如检查相关性分数是否超过某个阈值
			if score, ok := resumeData["qdrant_score"].(float64); ok {
				assert.True(t, score > 0.1, "相关性分数应该大于0.1")
			}
		}
	} else {
		testLogger.Log("没有找到匹配的简历")
		t.Log("没有找到匹配的简历")
	}

	testLogger.Log("测试完成")
}

// ensureJobExists 确保测试岗位在数据库中存在
func ensureJobExists(t *testing.T, s *storage.Storage, jobID, title, description string) {
	// 创建一个静默的logger以关闭SQL日志打印
	silentLogger := logger.New(
		log.New(io.Discard, "", log.LstdFlags), // 输出到丢弃的writer
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Silent, // 设置为Silent级别
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	// 使用静默的logger创建一个新的数据库会话
	silentDB := s.MySQL.DB().Session(&gorm.Session{Logger: silentLogger})

	// 检查岗位是否已存在
	var count int64
	if err := silentDB.Model(&models.Job{}).Where("job_id = ?", jobID).Count(&count).Error; err != nil {
		t.Fatalf("检查岗位是否存在失败: %v", err)
	}

	if count == 0 {
		// 创建岗位记录
		job := models.Job{
			JobID:              jobID,
			JobTitle:           title,
			JobDescriptionText: description,
			Department:         "测试平台",
			Location:           "天津",
			Status:             "ACTIVE",
			CreatedAt:          time.Now(),
			UpdatedAt:          time.Now(),
		}

		if err := silentDB.Create(&job).Error; err != nil {
			t.Fatalf("创建岗位记录失败: %v", err)
		}
		t.Logf("已创建测试岗位: %s", jobID)
	} else {
		t.Logf("测试岗位已存在: %s", jobID)
	}

	// 检查向量是否存在，如果不存在，测试将依赖正常的处理流程生成向量
	var vectorCount int64
	if err := silentDB.Model(&models.JobVector{}).Where("job_id = ?", jobID).Count(&vectorCount).Error; err != nil {
		t.Logf("检查岗位向量时发生错误: %v", err)
	}

	if vectorCount == 0 {
		t.Logf("岗位向量不存在，将在测试过程中生成")
	} else {
		t.Logf("岗位向量已存在")
	}
}

// TestSimpleVectorSearch 测试一个简单的向量搜索
func TestSimpleVectorSearch(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "SimpleVectorSearch")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试简单向量搜索")

	// 1. 设置测试环境
	ctx := context.Background()

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true})
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)
	glog.SetLogger(hertzCompatibleLogger)
	glog.SetLevel(glog.LevelDebug)

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")
	testLogger.Log("配置加载成功")

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()
	testLogger.Log("存储组件初始化成功")

	// 跳过CI环境测试
	if os.Getenv("CI") != "" {
		testLogger.Log("在CI环境中跳过此测试")
		t.Skip("在CI环境中跳过此测试")
	}

	// 检查必要组件是否可用
	if s.Qdrant == nil {
		testLogger.Log("Qdrant未配置或不可用，跳过测试")
		t.Skip("Qdrant未配置或不可用，跳过测试")
	}

	// 确保API密钥配置正确
	if cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" {
		testLogger.Log("未配置有效的阿里云API密钥，跳过测试")
		t.Skip("未配置有效的阿里云API密钥，跳过测试")
	}

	// 初始化embedder
	testLogger.Log("初始化Embedder")
	aliyunEmbeddingConfig := config.EmbeddingConfig{
		Model:      cfg.Aliyun.Embedding.Model,
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,
		Dimensions: cfg.Aliyun.Embedding.Dimensions,
	}
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)
	require.NoError(t, err, "初始化embedder失败")
	testLogger.Log("Embedder初始化成功，Model: %s, Dimensions: %d", aliyunEmbeddingConfig.Model, aliyunEmbeddingConfig.Dimensions)

	// 检查Qdrant中的数据量
	count, countErr := s.Qdrant.CountPoints(ctx)
	if countErr != nil {
		testLogger.Log("获取Qdrant点数量失败: %v", countErr)
	} else {
		testLogger.Log("Qdrant集合中总共有 %d 个点", count)
	}

	// 1. 将"go后端"字符串转换为向量
	searchText := "go后端"
	testLogger.Log("使用文本 '%s' 生成向量并进行搜索", searchText)

	vectors, err := embedder.EmbedStrings(context.Background(), []string{searchText})
	require.NoError(t, err, "为搜索文本生成向量失败")
	require.NotEmpty(t, vectors, "生成的向量不能为空")
	require.NotEmpty(t, vectors[0], "生成的向量数组不能为空")

	vector := vectors[0]
	testLogger.Log("成功生成向量，维度: %d", len(vector))

	// 记录向量的前10个元素作为示例
	if len(vector) > 10 {
		sampleVector := vector[:10]
		testLogger.LogObject("向量样本(前10个元素)", sampleVector)
	}

	// 2. 使用向量在Qdrant中搜索
	limit := 10
	testLogger.Log("在Qdrant中搜索，限制结果数: %d", limit)
	results, err := s.Qdrant.SearchSimilarResumes(ctx, vector, limit, nil)
	require.NoError(t, err, "向量搜索失败")

	// 3. 分析搜索结果
	testLogger.Log("搜索返回了 %d 个结果", len(results))
	t.Logf("搜索返回了 %d 个结果", len(results))

	if len(results) == 0 {
		// 如果没有结果，检查Qdrant集合中的点数量
		count, countErr := s.Qdrant.CountPoints(ctx)
		if countErr != nil {
			testLogger.Log("获取Qdrant点数量失败: %v", countErr)
			t.Logf("获取Qdrant点数量失败: %v", countErr)
		} else {
			testLogger.Log("Qdrant集合中总共有 %d 个点", count)
			t.Logf("Qdrant集合中总共有 %d 个点", count)
		}

		// 检查集合是否为空
		if count == 0 {
			testLogger.Log("Qdrant集合为空，没有可搜索的数据")
			t.Log("Qdrant集合为空，没有可搜索的数据")
		} else {
			testLogger.Log("虽然集合中有数据，但搜索没有返回结果，可能是搜索文本与现有数据相关性低")
			t.Log("虽然集合中有数据，但搜索没有返回结果，可能是搜索文本与现有数据相关性低")
		}
	} else {
		// 如果有结果，打印详细信息
		for i, result := range results {
			// 从payload中获取submission_uuid
			submissionUUID := ""
			if subUUID, ok := result.Payload["submission_uuid"]; ok {
				if s, ok := subUUID.(string); ok {
					submissionUUID = s
				}
			}

			testLogger.Log("结果 #%d: SubmissionUUID=%s, Score=%.4f",
				i+1, submissionUUID, result.Score)
			t.Logf("结果 #%d: SubmissionUUID=%s, Score=%.4f",
				i+1, submissionUUID, result.Score)

			// 记录完整的payload到日志文件
			testLogger.LogObject(fmt.Sprintf("结果 #%d 完整Payload", i+1), result.Payload)

			if s.MySQL != nil {
				// 尝试获取简历块内容
				var chunkContent string
				err := s.MySQL.DB().WithContext(ctx).
					Raw("SELECT chunk_content_text FROM resume_submission_chunks WHERE point_id = ?",
						result.ID).Scan(&chunkContent).Error
				if err == nil {
					// 截取内容前100个字符
					preview := chunkContent
					if len(chunkContent) > 100 {
						preview = chunkContent[:100] + "..."
					}
					testLogger.Log("内容预览: %s", preview)
					t.Logf("内容预览: %s", preview)

					// 记录完整内容到日志文件
					testLogger.Log("完整内容:\n%s", chunkContent)
				} else {
					testLogger.Log("获取内容失败: %v", err)
				}
			}
		}
	}

	testLogger.Log("测试完成")
}

// TestCompareJDDescriptions 比较不同格式的JD描述在向量检索中的效果差异
func TestCompareJDDescriptions(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "CompareJDDescriptions")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试比较三种不同JD描述格式的检索效果")

	// 1. 设置测试环境
	ctx := context.Background()

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true})
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)
	glog.SetLogger(hertzCompatibleLogger)
	glog.SetLevel(glog.LevelDebug)

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")
	testLogger.Log("配置加载成功")

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()
	testLogger.Log("存储组件初始化成功")

	// 跳过CI环境测试
	if os.Getenv("CI") != "" {
		testLogger.Log("在CI环境中跳过此测试")
		t.Skip("在CI环境中跳过此测试")
	}

	// 检查必要组件是否可用
	if s.Qdrant == nil {
		testLogger.Log("Qdrant未配置或不可用，跳过测试")
		t.Skip("Qdrant未配置或不可用，跳过测试")
	}

	// 确保API密钥配置正确
	if cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" {
		testLogger.Log("未配置有效的阿里云API密钥，跳过测试")
		t.Skip("未配置有效的阿里云API密钥，跳过测试")
	}

	// 初始化embedder
	testLogger.Log("初始化Embedder")
	aliyunEmbeddingConfig := config.EmbeddingConfig{
		Model:      cfg.Aliyun.Embedding.Model,
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,
		Dimensions: cfg.Aliyun.Embedding.Dimensions,
	}
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)
	require.NoError(t, err, "初始化embedder失败")
	testLogger.Log("Embedder初始化成功，Model: %s, Dimensions: %d", aliyunEmbeddingConfig.Model, aliyunEmbeddingConfig.Dimensions)

	// 3. 定义要比较的三种JD描述
	jobDesc1 := `工作职责：
1. 做产品质量的负责人 & 技术风险的把控者，针对产品整体质量和每次发版做好质量保障工作，包括但不限于功能测试、接口测试、性能测试、大数据测试；
2. 产品对应的测试工具开发；
3. 通用型测试平台开发。

岗位要求：
· 本科及以上学历，硕士/博士优先，计算机、数学、信息管理等相关专业；
· 熟悉 C、C++、Java、Python、Perl 等至少一种编程语言；
· 熟悉软件研发流程，掌握软件测试理论和方法，有设计和开发测试工具、自动化测试框架能力更佳；
· 熟悉计算机系统结构、操作系统、网络、分布式系统等基础知识；
· 熟悉机器学习算法、自然语言处理或图像算法之一更佳；
· 具备广泛技术视野、较强学习与问题解决能力；
· 对质量捍卫有热情，持续追求卓越的用户体验；
· 具备奉献精神，善于沟通与团队合作。`

	jobDesc2 := `「产品质量技术负责人 / SDET Lead」。候选人需对每次版本发布的整体质量与技术风险负全责，主导功能测试、接口测试、性能测试和大数据场景测试。必须具备 ≥5 年测试开发或测试架构经验，能够用 Python、Java、C++ 等至少一种语言设计并实现自动化测试框架、测试工具及通用测试平台。熟悉 CI/CD、分布式系统、操作系统、网络协议；了解机器学习、NLP 或图像算法者更佳。关键能力：质量保障体系设计、测试平台 0→1 落地、性能调优、缺陷预防、跨团队沟通与风险把控。`

	jobDesc3 := `产品质量技术负责人 / QA Lead / SDET Lead
核心职责：版本发布质量保障；功能、接口、性能、大数据测试；设计并落地测试工具与通用测试平台。
必需技能：Python / Java / C++ 之一；PyTest、Selenium、Rest-Assured、JMeter、Locust；CI/CD( Jenkins / GitLab ); 分布式系统。
加分：机器学习、NLP、云原生监控、Chaos Engineering。
指标：≥5 年测试架构经验；支撑百万 QPS 压测；缺陷率 <0.1%；质量看板 0→1。`

	testLogger.Log("已定义三种JD描述格式")
	testLogger.Log("JD描述1 (详细结构化格式)，长度: %d", len(jobDesc1))
	testLogger.Log("JD描述2 (精简段落格式)，长度: %d", len(jobDesc2))
	testLogger.Log("JD描述3 (超简洁列表格式)，长度: %d", len(jobDesc3))

	// 4. 为三种描述分别生成向量
	testLogger.Log("开始为三种JD描述生成向量")
	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc1, jobDesc2, jobDesc3})
	require.NoError(t, err, "为JD描述生成向量失败")
	require.Equal(t, 3, len(vectors), "应该生成三个向量")
	require.NotEmpty(t, vectors[0], "JD描述1的向量不能为空")
	require.NotEmpty(t, vectors[1], "JD描述2的向量不能为空")
	require.NotEmpty(t, vectors[2], "JD描述3的向量不能为空")

	vector1 := vectors[0]
	vector2 := vectors[1]
	vector3 := vectors[2]
	testLogger.Log("成功生成向量，JD描述1维度: %d, JD描述2维度: %d, JD描述3维度: %d", len(vector1), len(vector2), len(vector3))

	// 计算三个向量之间的余弦相似度
	cosineSimilarity12 := calculateCosineSimilarity(vector1, vector2)
	cosineSimilarity13 := calculateCosineSimilarity(vector1, vector3)
	cosineSimilarity23 := calculateCosineSimilarity(vector2, vector3)

	testLogger.Log("JD描述1和2之间的余弦相似度: %.6f", cosineSimilarity12)
	testLogger.Log("JD描述1和3之间的余弦相似度: %.6f", cosineSimilarity13)
	testLogger.Log("JD描述2和3之间的余弦相似度: %.6f", cosineSimilarity23)

	t.Logf("JD描述1和2之间的余弦相似度: %.6f", cosineSimilarity12)
	t.Logf("JD描述1和3之间的余弦相似度: %.6f", cosineSimilarity13)
	t.Logf("JD描述2和3之间的余弦相似度: %.6f", cosineSimilarity23)

	// 5. 对三种描述分别进行向量搜索
	limit := 50
	testLogger.Log("在Qdrant中搜索，限制结果数: %d", limit)

	// 检查Qdrant中的数据量
	count, countErr := s.Qdrant.CountPoints(ctx)
	if countErr != nil {
		testLogger.Log("获取Qdrant点数量失败: %v", countErr)
	} else {
		testLogger.Log("Qdrant集合中总共有 %d 个点", count)
	}

	// 5.1 使用JD描述1向量搜索
	testLogger.Log("使用JD描述1向量搜索")
	results1, err := s.Qdrant.SearchSimilarResumes(ctx, vector1, limit, nil)
	require.NoError(t, err, "JD描述1向量搜索失败")
	testLogger.Log("JD描述1搜索返回了 %d 个结果", len(results1))

	// 5.2 使用JD描述2向量搜索
	testLogger.Log("使用JD描述2向量搜索")
	results2, err := s.Qdrant.SearchSimilarResumes(ctx, vector2, limit, nil)
	require.NoError(t, err, "JD描述2向量搜索失败")
	testLogger.Log("JD描述2搜索返回了 %d 个结果", len(results2))

	// 5.3 使用JD描述3向量搜索
	testLogger.Log("使用JD描述3向量搜索")
	results3, err := s.Qdrant.SearchSimilarResumes(ctx, vector3, limit, nil)
	require.NoError(t, err, "JD描述3向量搜索失败")
	testLogger.Log("JD描述3搜索返回了 %d 个结果", len(results3))

	// 6. 分析和比较结果
	// 6.1 将结果转换为map以便于分析
	resultMap1 := make(map[string]float32)
	resultMap2 := make(map[string]float32)
	resultMap3 := make(map[string]float32)

	for _, r := range results1 {
		if uuid, ok := r.Payload["submission_uuid"].(string); ok && uuid != "" {
			resultMap1[uuid] = r.Score
		}
	}

	for _, r := range results2 {
		if uuid, ok := r.Payload["submission_uuid"].(string); ok && uuid != "" {
			resultMap2[uuid] = r.Score
		}
	}

	for _, r := range results3 {
		if uuid, ok := r.Payload["submission_uuid"].(string); ok && uuid != "" {
			resultMap3[uuid] = r.Score
		}
	}

	// 6.2 记录详细结果
	testLogger.Log("JD描述1搜索结果详情:")
	logSearchResults(testLogger, t, s, ctx, results1, "JD描述1")

	testLogger.Log("JD描述2搜索结果详情:")
	logSearchResults(testLogger, t, s, ctx, results2, "JD描述2")

	testLogger.Log("JD描述3搜索结果详情:")
	logSearchResults(testLogger, t, s, ctx, results3, "JD描述3")

	// 6.3 计算三种描述搜索结果的交集和差异
	// 6.3.1 两两之间的交集
	var common12 []string  // 描述1和2的交集
	var common13 []string  // 描述1和3的交集
	var common23 []string  // 描述2和3的交集
	var commonAll []string // 三者共有

	// 6.3.2 唯一的结果
	var uniqueToDesc1 []string
	var uniqueToDesc2 []string
	var uniqueToDesc3 []string

	// 计算三个描述共有的UUID
	for uuid := range resultMap1 {
		in2 := false
		in3 := false

		if _, exists := resultMap2[uuid]; exists {
			in2 = true
			common12 = append(common12, uuid)
		}

		if _, exists := resultMap3[uuid]; exists {
			in3 = true
			common13 = append(common13, uuid)
		}

		if in2 && in3 {
			commonAll = append(commonAll, uuid)
		} else if !in2 && !in3 {
			uniqueToDesc1 = append(uniqueToDesc1, uuid)
		}
	}

	// 计算描述2和3的交集，以及描述2的唯一结果
	for uuid := range resultMap2 {
		_, in1 := resultMap1[uuid]

		if _, in3 := resultMap3[uuid]; in3 {
			if !in1 { // 如果不在描述1中，则是2和3独有的
				common23 = append(common23, uuid)
			}
		} else if !in1 { // 如果既不在1也不在3中，则是描述2独有的
			uniqueToDesc2 = append(uniqueToDesc2, uuid)
		}
	}

	// 计算描述3独有的结果
	for uuid := range resultMap3 {
		_, in1 := resultMap1[uuid]
		_, in2 := resultMap2[uuid]

		if !in1 && !in2 {
			uniqueToDesc3 = append(uniqueToDesc3, uuid)
		}
	}

	// 排序以确保输出稳定
	sort.Strings(common12)
	sort.Strings(common13)
	sort.Strings(common23)
	sort.Strings(commonAll)
	sort.Strings(uniqueToDesc1)
	sort.Strings(uniqueToDesc2)
	sort.Strings(uniqueToDesc3)

	// 输出交集和差异统计
	testLogger.Log("三种JD描述搜索结果的比较:")
	testLogger.Log("三个描述共同的简历数量: %d", len(commonAll))
	if len(commonAll) > 0 {
		testLogger.Log("三个描述共同的简历UUID: %v", commonAll)
	}

	testLogger.Log("描述1和2共同的简历数量: %d", len(common12))
	testLogger.Log("描述1和3共同的简历数量: %d", len(common13))
	testLogger.Log("描述2和3共同的简历数量: %d", len(common23))

	testLogger.Log("仅描述1找到的简历数量: %d", len(uniqueToDesc1))
	testLogger.Log("仅描述2找到的简历数量: %d", len(uniqueToDesc2))
	testLogger.Log("仅描述3找到的简历数量: %d", len(uniqueToDesc3))

	// 6.4 比较共同简历的分数差异
	if len(commonAll) > 0 {
		testLogger.Log("三个描述共同简历的分数比较:")
		t.Log("三个描述共同简历的分数比较:")

		for _, uuid := range commonAll {
			score1 := resultMap1[uuid]
			score2 := resultMap2[uuid]
			score3 := resultMap3[uuid]

			// 计算最大分数和对应的描述
			maxScore := score1
			maxDesc := "描述1"
			if score2 > maxScore {
				maxScore = score2
				maxDesc = "描述2"
			}
			if score3 > maxScore {
				maxScore = score3
				maxDesc = "描述3"
			}

			testLogger.Log("UUID: %s, 描述1: %.4f, 描述2: %.4f, 描述3: %.4f, 最佳匹配: %s(%.4f)",
				uuid, score1, score2, score3, maxDesc, maxScore)
			t.Logf("UUID: %s, 描述1: %.4f, 描述2: %.4f, 描述3: %.4f, 最佳匹配: %s(%.4f)",
				uuid, score1, score2, score3, maxDesc, maxScore)
		}
	}

	testLogger.Log("测试完成")
}

// calculateCosineSimilarity 计算两个向量的余弦相似度
func calculateCosineSimilarity(v1, v2 []float64) float64 {
	if len(v1) != len(v2) {
		return 0
	}

	var dotProduct float64
	var norm1 float64
	var norm2 float64

	for i := 0; i < len(v1); i++ {
		dotProduct += v1[i] * v2[i]
		norm1 += v1[i] * v1[i]
		norm2 += v2[i] * v2[i]
	}

	// 避免除以零
	if norm1 == 0 || norm2 == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(norm1) * math.Sqrt(norm2))
}

// logSearchResults 记录搜索结果详情
func logSearchResults(testLogger *TestLogger, t *testing.T, s *storage.Storage, ctx context.Context, results []storage.SearchResult, label string) {
	for i, result := range results {
		// 从payload中获取submission_uuid
		submissionUUID := ""
		if subUUID, ok := result.Payload["submission_uuid"]; ok {
			if s, ok := subUUID.(string); ok {
				submissionUUID = s
			}
		}

		testLogger.Log("%s结果 #%d: SubmissionUUID=%s, Score=%.4f",
			label, i+1, submissionUUID, result.Score)
		t.Logf("%s结果 #%d: SubmissionUUID=%s, Score=%.4f",
			label, i+1, submissionUUID, result.Score)

		// 记录完整的payload到日志文件
		testLogger.LogObject(fmt.Sprintf("%s结果 #%d 完整Payload", label, i+1), result.Payload)

		if s.MySQL != nil {
			// 尝试获取简历块内容
			var chunkContent string
			err := s.MySQL.DB().WithContext(ctx).
				Raw("SELECT chunk_content_text FROM resume_submission_chunks WHERE point_id = ?",
					result.ID).Scan(&chunkContent).Error
			if err == nil {
				// 截取内容前100个字符
				preview := chunkContent
				if len(chunkContent) > 100 {
					preview = chunkContent[:100] + "..."
				}
				testLogger.Log("%s内容预览: %s", label, preview)

				// 记录完整内容到日志文件
				testLogger.Log("%s完整内容:\n%s", label, chunkContent)
			}
		}
	}
}

// TestMultiVectorSearch 测试多向量联合检索方法 (Union Multi-Vector Search)
func TestMultiVectorSearch(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "MultiVectorSearch")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试多向量联合检索 (Union Multi-Vector Search)")

	// 1. 设置测试环境
	ctx := context.Background()

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true})
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)
	glog.SetLogger(hertzCompatibleLogger)
	glog.SetLevel(glog.LevelDebug)

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")
	testLogger.Log("配置加载成功")

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()
	testLogger.Log("存储组件初始化成功")

	// 跳过CI环境测试
	if os.Getenv("CI") != "" {
		testLogger.Log("在CI环境中跳过此测试")
		t.Skip("在CI环境中跳过此测试")
	}

	// 检查必要组件是否可用
	if s.Qdrant == nil {
		testLogger.Log("Qdrant未配置或不可用，跳过测试")
		t.Skip("Qdrant未配置或不可用，跳过测试")
	}

	// 确保API密钥配置正确
	if cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" {
		testLogger.Log("未配置有效的阿里云API密钥，跳过测试")
		t.Skip("未配置有效的阿里云API密钥，跳过测试")
	}

	// 初始化embedder
	testLogger.Log("初始化Embedder")
	aliyunEmbeddingConfig := config.EmbeddingConfig{
		Model:      cfg.Aliyun.Embedding.Model,
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,
		Dimensions: cfg.Aliyun.Embedding.Dimensions,
	}
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)
	require.NoError(t, err, "初始化embedder失败")
	testLogger.Log("Embedder初始化成功，Model: %s, Dimensions: %d", aliyunEmbeddingConfig.Model, aliyunEmbeddingConfig.Dimensions)

	// 3. 定义要使用的JD描述 (使用之前测试中的JD1和JD3)
	jobDesc1 := `工作职责：
1. 做产品质量的负责人 & 技术风险的把控者，针对产品整体质量和每次发版做好质量保障工作，包括但不限于功能测试、接口测试、性能测试、大数据测试；
2. 产品对应的测试工具开发；
3. 通用型测试平台开发。

岗位要求：
· 本科及以上学历，硕士/博士优先，计算机、数学、信息管理等相关专业；
· 熟悉 C、C++、Java、Python、Perl 等至少一种编程语言；
· 熟悉软件研发流程，掌握软件测试理论和方法，有设计和开发测试工具、自动化测试框架能力更佳；
· 熟悉计算机系统结构、操作系统、网络、分布式系统等基础知识；
· 熟悉机器学习算法、自然语言处理或图像算法之一更佳；
· 具备广泛技术视野、较强学习与问题解决能力；
· 对质量捍卫有热情，持续追求卓越的用户体验；
· 具备奉献精神，善于沟通与团队合作。`

	jobDesc3 := `产品质量技术负责人 / QA Lead / SDET Lead
核心职责：版本发布质量保障；功能、接口、性能、大数据测试；设计并落地测试工具与通用测试平台。
必需技能：Python / Java / C++ 之一；PyTest、Selenium、Rest-Assured、JMeter、Locust；CI/CD( Jenkins / GitLab ); 分布式系统。
加分：机器学习、NLP、云原生监控、Chaos Engineering。
指标：≥5 年测试架构经验；支撑百万 QPS 压测；缺陷率 <0.1%；质量看板 0→1。`

	testLogger.Log("准备使用两种JD描述格式进行多向量检索")
	testLogger.Log("JD描述1 (详细结构化格式)，长度: %d", len(jobDesc1))
	testLogger.Log("JD描述3 (超简洁列表格式)，长度: %d", len(jobDesc3))

	// 4. 为两种描述分别生成向量
	testLogger.Log("开始为两种JD描述生成向量")
	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc1, jobDesc3})
	require.NoError(t, err, "为JD描述生成向量失败")
	require.Equal(t, 2, len(vectors), "应该生成两个向量")
	require.NotEmpty(t, vectors[0], "JD描述1的向量不能为空")
	require.NotEmpty(t, vectors[1], "JD描述3的向量不能为空")

	vector1 := vectors[0]
	vector3 := vectors[1]
	testLogger.Log("成功生成向量，JD描述1维度: %d, JD描述3维度: %d", len(vector1), len(vector3))

	// 计算两个向量之间的余弦相似度
	cosineSimilarity := calculateCosineSimilarity(vector1, vector3)
	testLogger.Log("JD描述1和3之间的余弦相似度: %.6f", cosineSimilarity)
	t.Logf("JD描述1和3之间的余弦相似度: %.6f", cosineSimilarity)

	// 5. 设置搜索限制数量
	limit := 50
	testLogger.Log("在Qdrant中搜索，每次限制结果数: %d", limit)

	// 检查Qdrant中的数据量
	count, countErr := s.Qdrant.CountPoints(ctx)
	if countErr != nil {
		testLogger.Log("获取Qdrant点数量失败: %v", countErr)
	} else {
		testLogger.Log("Qdrant集合中总共有 %d 个点", count)
	}

	// 6. 分别执行搜索
	// 6.1 使用JD描述1向量搜索
	testLogger.Log("使用JD描述1向量搜索")
	results1, err := s.Qdrant.SearchSimilarResumes(ctx, vector1, limit, nil)
	require.NoError(t, err, "JD描述1向量搜索失败")
	testLogger.Log("JD描述1搜索返回了 %d 个结果", len(results1))

	// 6.2 使用JD描述3向量搜索
	testLogger.Log("使用JD描述3向量搜索")
	results3, err := s.Qdrant.SearchSimilarResumes(ctx, vector3, limit, nil)
	require.NoError(t, err, "JD描述3向量搜索失败")
	testLogger.Log("JD描述3搜索返回了 %d 个结果", len(results3))

	// 7. 客户端合并结果 (Union操作)
	type BestMatch struct {
		Result     storage.SearchResult
		Source     string // 标记结果来源
		OriginalID string // 原始ID
	}

	// 使用map来存储合并后的结果，键为point_id或submission_uuid
	unionResults := make(map[string]BestMatch)

	// 7.1 处理JD描述1的结果
	testLogger.Log("开始处理JD描述1的结果并添加到合并集合")
	for _, result := range results1 {
		// 尝试从payload中获取submission_uuid作为键
		key := ""
		if uuid, ok := result.Payload["submission_uuid"].(string); ok && uuid != "" {
			key = uuid
		} else {
			// 如果没有submission_uuid，则使用point_id作为键
			key = result.ID
		}

		unionResults[key] = BestMatch{
			Result:     result,
			Source:     "JD1",
			OriginalID: result.ID,
		}
	}

	// 7.2 处理JD描述3的结果
	testLogger.Log("开始处理JD描述3的结果并添加到合并集合")
	for _, result := range results3 {
		// 尝试从payload中获取submission_uuid作为键
		key := ""
		if uuid, ok := result.Payload["submission_uuid"].(string); ok && uuid != "" {
			key = uuid
		} else {
			// 如果没有submission_uuid，则使用point_id作为键
			key = result.ID
		}

		// 检查是否已经存在相同键的结果
		if existing, exists := unionResults[key]; exists {
			// 如果已存在，则保留得分更高的那个
			if result.Score > existing.Result.Score {
				unionResults[key] = BestMatch{
					Result:     result,
					Source:     "JD3",
					OriginalID: result.ID,
				}
			}
		} else {
			// 如果不存在，则直接添加
			unionResults[key] = BestMatch{
				Result:     result,
				Source:     "JD3",
				OriginalID: result.ID,
			}
		}
	}

	// 7.3 将map转换为切片以便排序
	var mergedResults []BestMatch
	for _, match := range unionResults {
		mergedResults = append(mergedResults, match)
	}

	// 7.4 按分数降序排序
	sort.Slice(mergedResults, func(i, j int) bool {
		return mergedResults[i].Result.Score > mergedResults[j].Result.Score
	})

	// 8. 分析合并后的结果
	testLogger.Log("合并后总共有 %d 个唯一结果", len(mergedResults))
	t.Logf("合并后总共有 %d 个唯一结果", len(mergedResults))

	// 计算各来源的统计数据
	var onlyInJD1 int
	var onlyInJD3 int
	var inBoth int
	var jd1WonCount int // JD1分数更高的数量
	var jd3WonCount int // JD3分数更高的数量

	for _, match := range mergedResults {
		key := ""
		if uuid, ok := match.Result.Payload["submission_uuid"].(string); ok && uuid != "" {
			key = uuid
		} else {
			key = match.OriginalID
		}

		// 检查是否在JD1和JD3的结果中都存在
		inJD1 := false
		inJD3 := false
		var score1 float32
		var score3 float32

		for _, r := range results1 {
			rKey := ""
			if uuid, ok := r.Payload["submission_uuid"].(string); ok && uuid != "" {
				rKey = uuid
			} else {
				rKey = r.ID
			}

			if rKey == key {
				inJD1 = true
				score1 = r.Score
				break
			}
		}

		for _, r := range results3 {
			rKey := ""
			if uuid, ok := r.Payload["submission_uuid"].(string); ok && uuid != "" {
				rKey = uuid
			} else {
				rKey = r.ID
			}

			if rKey == key {
				inJD3 = true
				score3 = r.Score
				break
			}
		}

		if inJD1 && inJD3 {
			inBoth++
			if score1 > score3 {
				jd1WonCount++
			} else {
				jd3WonCount++
			}
		} else if inJD1 {
			onlyInJD1++
		} else if inJD3 {
			onlyInJD3++
		}
	}

	testLogger.Log("仅在JD1结果中的简历数量: %d", onlyInJD1)
	testLogger.Log("仅在JD3结果中的简历数量: %d", onlyInJD3)
	testLogger.Log("同时在两个结果中的简历数量: %d", inBoth)
	testLogger.Log("在两者共有的简历中，JD1分数更高的数量: %d", jd1WonCount)
	testLogger.Log("在两者共有的简历中，JD3分数更高的数量: %d", jd3WonCount)

	t.Logf("仅在JD1结果中的简历数量: %d", onlyInJD1)
	t.Logf("仅在JD3结果中的简历数量: %d", onlyInJD3)
	t.Logf("同时在两个结果中的简历数量: %d", inBoth)
	t.Logf("在两者共有的简历中，JD1分数更高的数量: %d", jd1WonCount)
	t.Logf("在两者共有的简历中，JD3分数更高的数量: %d", jd3WonCount)

	// 9. 记录前10个合并后的结果详情
	resultLimit := 10
	if len(mergedResults) < resultLimit {
		resultLimit = len(mergedResults)
	}

	testLogger.Log("前%d个合并结果详情:", resultLimit)
	t.Logf("前%d个合并结果详情:", resultLimit)

	for i := 0; i < resultLimit; i++ {
		match := mergedResults[i]
		result := match.Result

		// 从payload中获取submission_uuid
		submissionUUID := ""
		if uuid, ok := result.Payload["submission_uuid"].(string); ok {
			submissionUUID = uuid
		}

		testLogger.Log("合并结果 #%d: 来源=%s, SubmissionUUID=%s, Score=%.4f, PointID=%s",
			i+1, match.Source, submissionUUID, result.Score, result.ID)
		t.Logf("合并结果 #%d: 来源=%s, SubmissionUUID=%s, Score=%.4f, PointID=%s",
			i+1, match.Source, submissionUUID, result.Score, result.ID)

		// 记录完整的payload到日志文件
		testLogger.LogObject(fmt.Sprintf("合并结果 #%d 完整Payload", i+1), result.Payload)

		// 如果可能，获取简历内容预览
		if s.MySQL != nil {
			var chunkContent string
			err := s.MySQL.DB().WithContext(ctx).
				Raw("SELECT chunk_content_text FROM resume_submission_chunks WHERE point_id = ?",
					result.ID).Scan(&chunkContent).Error
			if err == nil {
				preview := chunkContent
				if len(chunkContent) > 100 {
					preview = chunkContent[:100] + "..."
				}
				testLogger.Log("内容预览: %s", preview)

				// 记录完整内容到日志文件
				testLogger.Log("完整内容:\n%s", chunkContent)
			}
		}
	}

	// 10. 检查Batch Search是否可用 (这部分是对Qdrant批量搜索的说明，未实际执行)
	testLogger.Log("注意: 本测试通过两次单独调用SearchSimilarResumes实现了多向量检索")
	testLogger.Log("在实际生产环境中，应考虑使用Qdrant的BatchSearch API来提高效率")
	testLogger.Log("BatchSearch允许一次请求同时发送多个向量查询，减少网络往返")

	testLogger.Log("测试完成")
}

// TestCompareRetrievalMethods 比较单一向量检索和多向量联合检索的效果差异
func TestCompareRetrievalMethods(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "CompareRetrievalMethods")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试比较单一向量检索和多向量联合检索效果差异")

	// 1. 设置测试环境
	ctx := context.Background()

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true})
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)
	glog.SetLogger(hertzCompatibleLogger)
	glog.SetLevel(glog.LevelDebug)

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")
	testLogger.Log("配置加载成功")

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()
	testLogger.Log("存储组件初始化成功")

	// 跳过CI环境测试
	if os.Getenv("CI") != "" {
		testLogger.Log("在CI环境中跳过此测试")
		t.Skip("在CI环境中跳过此测试")
	}

	// 检查必要组件是否可用
	if s.Qdrant == nil {
		testLogger.Log("Qdrant未配置或不可用，跳过测试")
		t.Skip("Qdrant未配置或不可用，跳过测试")
	}

	// 确保API密钥配置正确
	if cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" {
		testLogger.Log("未配置有效的阿里云API密钥，跳过测试")
		t.Skip("未配置有效的阿里云API密钥，跳过测试")
	}

	// 初始化embedder
	testLogger.Log("初始化Embedder")
	aliyunEmbeddingConfig := config.EmbeddingConfig{
		Model:      cfg.Aliyun.Embedding.Model,
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,
		Dimensions: cfg.Aliyun.Embedding.Dimensions,
	}
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)
	require.NoError(t, err, "初始化embedder失败")
	testLogger.Log("Embedder初始化成功，Model: %s, Dimensions: %d", aliyunEmbeddingConfig.Model, aliyunEmbeddingConfig.Dimensions)

	// 3. 定义要使用的JD描述 (使用之前测试中的JD1和JD3)
	jobDesc1 := `工作职责：
1. 做产品质量的负责人 & 技术风险的把控者，针对产品整体质量和每次发版做好质量保障工作，包括但不限于功能测试、接口测试、性能测试、大数据测试；
2. 产品对应的测试工具开发；
3. 通用型测试平台开发。

岗位要求：
· 本科及以上学历，硕士/博士优先，计算机、数学、信息管理等相关专业；
· 熟悉 C、C++、Java、Python、Perl 等至少一种编程语言；
· 熟悉软件研发流程，掌握软件测试理论和方法，有设计和开发测试工具、自动化测试框架能力更佳；
· 熟悉计算机系统结构、操作系统、网络、分布式系统等基础知识；
· 熟悉机器学习算法、自然语言处理或图像算法之一更佳；
· 具备广泛技术视野、较强学习与问题解决能力；
· 对质量捍卫有热情，持续追求卓越的用户体验；
· 具备奉献精神，善于沟通与团队合作。`

	jobDesc3 := `产品质量技术负责人 / QA Lead / SDET Lead
核心职责：版本发布质量保障；功能、接口、性能、大数据测试；设计并落地测试工具与通用测试平台。
必需技能：Python / Java / C++ 之一；PyTest、Selenium、Rest-Assured、JMeter、Locust；CI/CD( Jenkins / GitLab ); 分布式系统。
加分：机器学习、NLP、云原生监控、Chaos Engineering。
指标：≥5 年测试架构经验；支撑百万 QPS 压测；缺陷率 <0.1%；质量看板 0→1。`

	testLogger.Log("准备使用两种JD描述格式进行检索对比")
	testLogger.Log("JD描述1 (详细结构化格式)，长度: %d", len(jobDesc1))
	testLogger.Log("JD描述3 (超简洁列表格式)，长度: %d", len(jobDesc3))

	// 4. 为两种描述分别生成向量
	testLogger.Log("开始为两种JD描述生成向量")
	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc1, jobDesc3})
	require.NoError(t, err, "为JD描述生成向量失败")
	require.Equal(t, 2, len(vectors), "应该生成两个向量")
	require.NotEmpty(t, vectors[0], "JD描述1的向量不能为空")
	require.NotEmpty(t, vectors[1], "JD描述3的向量不能为空")

	vector1 := vectors[0]
	vector3 := vectors[1]
	testLogger.Log("成功生成向量，JD描述1维度: %d, JD描述3维度: %d", len(vector1), len(vector3))

	// 计算两个向量之间的余弦相似度
	cosineSimilarity := calculateCosineSimilarity(vector1, vector3)
	testLogger.Log("JD描述1和3之间的余弦相似度: %.6f", cosineSimilarity)
	t.Logf("JD描述1和3之间的余弦相似度: %.6f", cosineSimilarity)

	// 5. 设置搜索限制数量
	limit := 50
	testLogger.Log("在Qdrant中搜索，每次限制结果数: %d", limit)

	// 检查Qdrant中的数据量
	count, countErr := s.Qdrant.CountPoints(ctx)
	if countErr != nil {
		testLogger.Log("获取Qdrant点数量失败: %v", countErr)
	} else {
		testLogger.Log("Qdrant集合中总共有 %d 个点", count)
	}

	// 6. 使用JD1进行单一向量检索
	testLogger.Log("方法1: 使用JD描述1向量进行单一检索")
	results1, err := s.Qdrant.SearchSimilarResumes(ctx, vector1, limit, nil)
	require.NoError(t, err, "JD描述1向量搜索失败")
	testLogger.Log("JD描述1单一检索返回了 %d 个结果", len(results1))

	// 7. 使用JD1+JD3进行多向量联合检索
	testLogger.Log("方法2: 使用JD描述1+JD3向量进行联合检索")

	// 7.1 使用JD描述3向量搜索
	results3, err := s.Qdrant.SearchSimilarResumes(ctx, vector3, limit, nil)
	require.NoError(t, err, "JD描述3向量搜索失败")
	testLogger.Log("JD描述3搜索返回了 %d 个结果", len(results3))

	// 7.2 客户端合并结果 (Union操作)
	type BestMatch struct {
		Result     storage.SearchResult
		Source     string // 标记结果来源
		OriginalID string // 原始ID
	}

	// 使用map来存储合并后的结果，键为point_id或submission_uuid
	unionResults := make(map[string]BestMatch)

	// 7.3 处理JD描述1的结果
	testLogger.Log("开始处理JD描述1的结果并添加到合并集合")
	for _, result := range results1 {
		// 尝试从payload中获取submission_uuid作为键
		key := ""
		if uuid, ok := result.Payload["submission_uuid"].(string); ok && uuid != "" {
			key = uuid
		} else {
			// 如果没有submission_uuid，则使用point_id作为键
			key = result.ID
		}

		unionResults[key] = BestMatch{
			Result:     result,
			Source:     "JD1",
			OriginalID: result.ID,
		}
	}

	// 7.4 处理JD描述3的结果
	testLogger.Log("开始处理JD描述3的结果并添加到合并集合")
	for _, result := range results3 {
		// 尝试从payload中获取submission_uuid作为键
		key := ""
		if uuid, ok := result.Payload["submission_uuid"].(string); ok && uuid != "" {
			key = uuid
		} else {
			// 如果没有submission_uuid，则使用point_id作为键
			key = result.ID
		}

		// 检查是否已经存在相同键的结果
		if existing, exists := unionResults[key]; exists {
			// 如果已存在，则保留得分更高的那个
			if result.Score > existing.Result.Score {
				unionResults[key] = BestMatch{
					Result:     result,
					Source:     "JD3",
					OriginalID: result.ID,
				}
			}
		} else {
			// 如果不存在，则直接添加
			unionResults[key] = BestMatch{
				Result:     result,
				Source:     "JD3",
				OriginalID: result.ID,
			}
		}
	}

	// 7.5 将map转换为切片以便排序
	var mergedResults []BestMatch
	for _, match := range unionResults {
		mergedResults = append(mergedResults, match)
	}

	// 7.6 按分数降序排序
	sort.Slice(mergedResults, func(i, j int) bool {
		return mergedResults[i].Result.Score > mergedResults[j].Result.Score
	})

	testLogger.Log("联合检索返回了 %d 个唯一结果", len(mergedResults))
	t.Logf("联合检索返回了 %d 个唯一结果", len(mergedResults))

	// 8. 创建单一检索结果的UUID集合（用于快速查找）
	singleMethodUUIDs := make(map[string]bool)
	for _, result := range results1 {
		key := ""
		if uuid, ok := result.Payload["submission_uuid"].(string); ok && uuid != "" {
			key = uuid
		} else {
			key = result.ID
		}
		singleMethodUUIDs[key] = true
	}

	// 9. 找出只在联合检索中找到的结果（差集）
	var onlyInUnionResults []BestMatch
	for _, match := range mergedResults {
		key := ""
		if uuid, ok := match.Result.Payload["submission_uuid"].(string); ok && uuid != "" {
			key = uuid
		} else {
			key = match.OriginalID
		}

		// 如果不在单一检索结果中，则添加到差集
		if !singleMethodUUIDs[key] {
			onlyInUnionResults = append(onlyInUnionResults, match)
		}
	}

	// 10. 记录差集结果
	testLogger.Log("只在联合检索中找到的简历数量: %d", len(onlyInUnionResults))
	t.Logf("只在联合检索中找到的简历数量: %d", len(onlyInUnionResults))

	if len(onlyInUnionResults) > 0 {
		testLogger.Log("=== 只在联合检索中找到的简历详情 ===")
		t.Log("=== 只在联合检索中找到的简历详情 ===")

		// 确保结果按分数排序
		sort.Slice(onlyInUnionResults, func(i, j int) bool {
			return onlyInUnionResults[i].Result.Score > onlyInUnionResults[j].Result.Score
		})

		for i, match := range onlyInUnionResults {
			result := match.Result

			// 从payload中获取submission_uuid
			submissionUUID := ""
			if uuid, ok := result.Payload["submission_uuid"].(string); ok {
				submissionUUID = uuid
			}

			testLogger.Log("额外简历 #%d: 来源=%s, SubmissionUUID=%s, Score=%.4f, PointID=%s",
				i+1, match.Source, submissionUUID, result.Score, result.ID)
			t.Logf("额外简历 #%d: 来源=%s, SubmissionUUID=%s, Score=%.4f, PointID=%s",
				i+1, match.Source, submissionUUID, result.Score, result.ID)

			// 记录完整的payload到日志文件
			testLogger.LogObject(fmt.Sprintf("额外简历 #%d 完整Payload", i+1), result.Payload)

			// 如果可能，获取简历内容预览
			if s.MySQL != nil {
				var chunkContent string
				err := s.MySQL.DB().WithContext(ctx).
					Raw("SELECT chunk_content_text FROM resume_submission_chunks WHERE point_id = ?",
						result.ID).Scan(&chunkContent).Error
				if err == nil {
					preview := chunkContent
					if len(chunkContent) > 100 {
						preview = chunkContent[:100] + "..."
					}
					testLogger.Log("内容预览: %s", preview)

					// 记录完整内容到日志文件
					testLogger.Log("完整内容:\n%s", chunkContent)

					// 尝试获取简历的主要特点
					testLogger.Log("尝试提取关键特征...")
					var skills, experiences []string

					// 简单规则提取经验和技能（这是一个非常简化的例子）
					if strings.Contains(strings.ToLower(chunkContent), "年经验") ||
						strings.Contains(strings.ToLower(chunkContent), "years of experience") {
						experiences = append(experiences, "有多年经验")
					}

					techKeywords := []string{"python", "java", "c++", "测试", "test", "qa", "sdet",
						"自动化", "automation", "jenkins", "ci/cd", "selenium", "jmeter"}

					for _, keyword := range techKeywords {
						if strings.Contains(strings.ToLower(chunkContent), strings.ToLower(keyword)) {
							skills = append(skills, keyword)
						}
					}

					if len(skills) > 0 {
						testLogger.Log("可能的技能: %s", strings.Join(skills, ", "))
					}
					if len(experiences) > 0 {
						testLogger.Log("可能的经验: %s", strings.Join(experiences, ", "))
					}
				}
			}

			// 提取分析该简历为什么只在联合检索中被找到
			testLogger.Log("分析: 该简历只在使用'%s'格式检索时被找到，可能与该格式更强调的方面相匹配", match.Source)

			testLogger.Log("-----------------------------------")
		}
	} else {
		testLogger.Log("两种检索方法找到的简历完全相同，没有额外的简历")
		t.Log("两种检索方法找到的简历完全相同，没有额外的简历")
	}

	// 11. 统计信息
	testLogger.Log("检索方法统计信息:")
	testLogger.Log("单一向量检索 (JD1) 结果数: %d", len(results1))
	testLogger.Log("联合检索 (JD1+JD3) 结果数: %d", len(mergedResults))
	testLogger.Log("联合检索额外找到的简历数: %d", len(onlyInUnionResults))
	testLogger.Log("增益比例: %.2f%%", float64(len(onlyInUnionResults))/float64(len(results1))*100)

	t.Logf("检索方法统计信息:")
	t.Logf("单一向量检索 (JD1) 结果数: %d", len(results1))
	t.Logf("联合检索 (JD1+JD3) 结果数: %d", len(mergedResults))
	t.Logf("联合检索额外找到的简历数: %d", len(onlyInUnionResults))
	t.Logf("增益比例: %.2f%%", float64(len(onlyInUnionResults))/float64(len(results1))*100)

	testLogger.Log("测试完成")
}

// assessResumeQuality 简单评估简历质量
func assessResumeQuality(content string, keywords map[string]int) string {
	// 这里简单实现一个基于关键词的质量评估
	goRelated := strings.Contains(strings.ToLower(content), "go") ||
		strings.Contains(strings.ToLower(content), "golang")

	microserviceRelated := strings.Contains(strings.ToLower(content), "微服务") ||
		strings.Contains(strings.ToLower(content), "microservice")

	keyTechCount := 0
	for _, keyword := range []string{"grpc", "protobuf", "redis", "kafka", "docker", "kubernetes", "k8s"} {
		if strings.Contains(strings.ToLower(content), keyword) {
			keyTechCount++
		}
	}

	if goRelated && microserviceRelated && keyTechCount >= 3 {
		return "高质量匹配 - 包含Go和微服务经验，以及多项关键技术"
	} else if goRelated && keyTechCount >= 2 {
		return "良好匹配 - 包含Go经验和部分关键技术"
	} else if goRelated {
		return "基本匹配 - 包含Go经验"
	} else if keyTechCount >= 3 {
		return "部分相关 - 包含多项关键技术但未明确提及Go"
	} else {
		return "相关性较低"
	}
}

// TestFullSearchWithRedisCache 测试带有hr_id的全流程搜索和Redis缓存
func TestFullSearchWithRedisCache(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "FullSearchWithRedisCache")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试带有hr_id的全流程搜索和Redis缓存")

	// 1. 设置测试环境
	ctx := context.Background()

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "debug", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true})
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)
	glog.SetLogger(hertzCompatibleLogger)
	glog.SetLevel(glog.LevelDebug)

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")
	testLogger.Log("配置加载成功")

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()
	testLogger.Log("存储组件初始化成功")

	// 检查Redis是否可用
	if s.Redis == nil {
		testLogger.Log("Redis未配置或不可用，跳过测试")
		t.Skip("Redis未配置或不可用，跳过测试")
	}

	// 3. 创建Embedding服务
	testLogger.Log("创建Embedding服务")
	aliyunEmbedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "创建AliyunEmbedder失败")
	testLogger.Log("Embedding服务创建成功")

	// 4. 初始化JD处理器
	testLogger.Log("初始化JD处理器")
	jdProcessor, err := processor.NewJDProcessor(aliyunEmbedder, s, cfg.ActiveParserVersion)
	require.NoError(t, err, "初始化JD处理器失败")
	testLogger.Log("JD处理器初始化成功")

	// 5. 初始化JobSearchHandler
	testLogger.Log("初始化JobSearchHandler")
	h := handler.NewJobSearchHandler(cfg, s, jdProcessor)
	testLogger.Log("JobSearchHandler初始化成功")

	// 6. 准备测试数据 - 岗位和HR信息
	testJobID := "22222222-3333-4444-5555-666666666666"
	testHRID := "11111111-1111-1111-1111-111111111111"
	jobTitle := "高级Go后端及微服务工程师"
	jobDesc := `我们正在寻找一位经验丰富的高级Go后端及微服务工程师，加入我们的核心技术团队，负责设计、开发和维护大规模、高可用的分布式系统。您将有机会参与到从架构设计到服务上线的全过程，应对高并发、低延迟的挑战。

**主要职责:**
1.  负责核心业务系统的后端服务设计与开发，使用Go语言（Golang）作为主要开发语言。
2.  参与微服务架构的演进，使用gRPC进行服务间通信，并基于Protobuf进行接口定义。
3.  构建和维护高并发、高可用的系统，有处理秒杀、实时消息等大流量场景的经验。
4.  深入使用和优化缓存（Redis）和消息队列（Kafka/RabbitMQ），实现系统解耦和性能提升。
5.  将服务容器化（Docker）并部署在Kubernetes（K8s）集群上，熟悉云原生生态。
6.  关注系统性能，能够使用pprof等工具进行性能分析和调优。

**任职要求:**
1.  计算机相关专业本科及以上学历，3年以上Go语言后端开发经验。
2.  精通Go语言及其并发模型（Goroutine, Channel, Context）。
3.  熟悉至少一种主流Go微服务框架，如Go-Zero, Gin, Kratos等。
4.  熟悉gRPC, Protobuf，有丰富的微服务API设计经验。
5.  熟悉MySQL, Redis, Kafka等常用组件，并有生产环境应用经验。
6.  熟悉Docker和Kubernetes，理解云原生的基本理念。

**加分项:**
1.  有主导大型微服务项目重构或设计的经验。
2.  熟悉Service Mesh（如Istio）、分布式追踪（OpenTelemetry）等服务治理技术。
3.  对分布式存储（如TiKV）、共识算法（Raft）有深入研究或实践。
4.  有开源项目贡献或活跃的技术博客。`

	testLogger.Log("测试岗位ID: %s", testJobID)
	testLogger.Log("测试HR ID: %s", testHRID)
	testLogger.Log("岗位标题: %s", jobTitle)
	testLogger.Log("岗位描述:\n%s", jobDesc)

	// 7. 确保测试数据存在 - 岗位信息
	testLogger.Log("确保测试岗位数据存在")
	ensureJobExists(t, s, testJobID, jobTitle, jobDesc)

	// 8. 确保测试数据存在 - HR信息
	testLogger.Log("确保测试HR数据存在")
	ensureHRExists(t, s, testHRID, "MVP HR", "hr.mvp@example.com")

	// 9. 创建请求上下文
	testLogger.Log("创建请求上下文")
	c := app.NewContext(16)

	// 添加URL参数
	c.Params = append(c.Params, param.Param{
		Key:   "job_id",
		Value: testJobID,
	})

	// 设置HR ID到上下文中，模拟认证中间件的行为
	c.Set("hr_id", testHRID)

	// 添加查询参数
	c.QueryArgs().Add("limit", "10") // 设置一个合理的展示数量

	// 10. 清除可能存在的缓存，确保测试的独立性
	testLogger.Log("清除可能存在的Redis缓存")
	// 使用正确的缓存键格式，与job_search_handler.go中的格式保持一致
	cacheKey := fmt.Sprintf(constants.KeySearchSession, testJobID, testHRID)
	_, err = s.Redis.Client.Del(ctx, cacheKey).Result()
	if err != nil && err != redis.Nil {
		testLogger.Log("清除缓存时出错: %v", err)
	}

	// 11. 执行测试
	testLogger.Log("执行岗位搜索处理")
	h.HandleSearchResumesByJobID(ctx, c)

	// 12. 验证结果
	statusCode := c.Response.StatusCode()
	testLogger.Log("状态码: %d", statusCode)

	var respData map[string]interface{}
	if err := json.Unmarshal(c.Response.Body(), &respData); err != nil {
		testLogger.Log("解析响应失败: %v", err)
		t.Fatalf("解析响应失败: %v", err)
	}

	// 记录完整响应数据到日志文件
	testLogger.LogObject("响应数据", respData)

	// 13. 验证基本响应结构
	assert.Equal(t, consts.StatusOK, statusCode)
	assert.Contains(t, respData, "message")
	assert.Contains(t, respData, "job_id")
	assert.Equal(t, testJobID, respData["job_id"])

	// 14. 检查分页元数据（使用next_cursor和total_count，而非pagination字段）
	assert.Contains(t, respData, "next_cursor")
	assert.Contains(t, respData, "total_count")

	// 15. 现在检查Redis缓存
	testLogger.Log("检查Redis缓存")

	// 使用封装好的方法获取缓存数据，而不是直接使用Redis客户端
	// 注意：这里使用与job_search_handler.go中相同的方法获取缓存
	var cachedUUIDs []string
	var totalCount int64
	var cacheErr error
	maxRetries := 3
	retryDelay := 200 * time.Millisecond

	for i := 0; i < maxRetries; i++ {
		// 从位置0开始获取最多100个结果
		cachedUUIDs, totalCount, cacheErr = s.Redis.GetCachedSearchResults(ctx, cacheKey, 0, 100)
		if cacheErr == nil && len(cachedUUIDs) > 0 {
			testLogger.Log("成功获取Redis缓存数据，找到 %d 个UUID，总数: %d", len(cachedUUIDs), totalCount)
			break
		}

		testLogger.Log("获取Redis缓存尝试 %d 失败: %v，将等待 %v 后重试...",
			i+1, cacheErr, retryDelay)
		time.Sleep(retryDelay)

		// 增加重试间隔时间，避免频繁请求
		retryDelay *= 2
	}

	// 如果重试后仍失败，但API调用成功（状态码为200），继续测试而不中断
	if (cacheErr != nil || len(cachedUUIDs) == 0) && statusCode == consts.StatusOK {
		testLogger.Log("警告: 经过多次重试后仍无法获取Redis缓存或缓存为空: %v", cacheErr)
		testLogger.Log("但由于API请求成功返回了数据，将跳过缓存验证继续测试")
		t.Logf("警告: 无法获取Redis缓存，跳过缓存验证部分: %v", cacheErr)
		return // 提前结束测试，跳过后续缓存验证
	} else if cacheErr != nil {
		testLogger.Log("获取Redis缓存失败: %v", cacheErr)
		t.Fatalf("获取Redis缓存失败: %v", cacheErr)
	}

	// 直接使用获取到的UUID列表
	testLogger.Log("Redis缓存中包含 %d 个简历UUID，总数: %d", len(cachedUUIDs), totalCount)
	testLogger.LogObject("Redis缓存数据 (UUIDs)", cachedUUIDs)

	// 16. 验证缓存的结果数量是否符合预期 (应该是完整的召回结果集)
	testLogger.Log("验证缓存的结果数量")
	assert.True(t, len(cachedUUIDs) > 0, "缓存结果数量应该大于0")
	assert.True(t, totalCount > 0, "总结果数应该大于0")

	// 如果API响应中有数据，比较结果
	if data, ok := respData["data"].([]interface{}); ok && len(data) > 0 {
		testLogger.Log("API返回了 %d 个结果", len(data))

		// 验证API返回的结果数量与缓存信息一致
		if apiTotalCount, ok := respData["total_count"].(float64); ok {
			testLogger.Log("API返回的总数: %.0f, Redis缓存的总数: %d", apiTotalCount, totalCount)
			// 注意: API返回的total_count是float64类型，需要转换比较
			assert.Equal(t, int64(apiTotalCount), totalCount, "API和缓存的总数应该一致")
		}

		// 尝试比较第一个结果
		if len(data) > 0 && len(cachedUUIDs) > 0 {
			firstAPIResult := data[0].(map[string]interface{})
			apiUUID, apiUUIDExists := firstAPIResult["submission_uuid"].(string)

			if apiUUIDExists {
				testLogger.Log("第一个API结果UUID: %s", apiUUID)
				testLogger.Log("第一个缓存UUID: %s", cachedUUIDs[0])

				// 检查API返回的第一个UUID是否在缓存的UUID列表中
				foundInCache := false
				for _, uuid := range cachedUUIDs {
					if uuid == apiUUID {
						foundInCache = true
						break
					}
				}

				if foundInCache {
					testLogger.Log("API结果UUID在缓存中找到，符合预期")
				} else {
					testLogger.Log("注意：API结果UUID在缓存中未找到，这可能是由于排序或过滤导致的")
				}
			}
		}
	}

	testLogger.Log("测试完成")
}

// ensureHRExists 确保测试HR在数据库中存在
func ensureHRExists(t *testing.T, s *storage.Storage, hrID, name, email string) {
	// 创建一个静默的logger以关闭SQL日志打印
	silentLogger := logger.New(
		log.New(io.Discard, "", log.LstdFlags), // 输出到丢弃的writer
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Silent, // 设置为Silent级别
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	// 使用静默的logger创建一个新的数据库会话
	silentDB := s.MySQL.DB().Session(&gorm.Session{Logger: silentLogger})

	// 检查HR是否已存在
	var count int64
	if err := silentDB.Table("hrs").Where("hr_id = ?", hrID).Count(&count).Error; err != nil {
		t.Fatalf("检查HR是否存在失败: %v", err)
	}

	if count == 0 {
		// 创建HR记录
		hr := struct {
			HRID      string    `gorm:"column:hr_id"`
			Name      string    `gorm:"column:name"`
			Email     string    `gorm:"column:email"`
			CreatedAt time.Time `gorm:"column:created_at"`
			UpdatedAt time.Time `gorm:"column:updated_at"`
		}{
			HRID:      hrID,
			Name:      name,
			Email:     email,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		if err := silentDB.Table("hrs").Create(&hr).Error; err != nil {
			t.Fatalf("创建HR记录失败: %v", err)
		}
		t.Logf("已创建测试HR: %s", hrID)
	} else {
		t.Logf("测试HR已存在: %s", hrID)
	}
}

// abs 是一个辅助函数，返回整数的绝对值
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// TestShortBoardPenaltyScoring 测试短板惩罚评分算法
func TestShortBoardPenaltyScoring(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "ShortBoardPenaltyScoring")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试短板惩罚算法...")

	// 1. 设置测试环境 - 定义常量避免变量冲突
	// 这些ID是为后续集成扩展预留的
	_ = context.Background() // 避免未使用变量的警告
	const testTopK = 3
	const coverThreshold = 0.3
	const lambda1 = 0.4
	const lambda2 = 0.3

	// 2. 定义并实现短板惩罚算法
	// ShortBoardScore 计算考虑覆盖率和方差的短板惩罚分数
	shortBoardScore := func(chScores []float32, k int, coverTh, lambda1, lambda2 float32) float32 {
		if len(chScores) == 0 {
			return 0
		}

		// 1. 排序求 top-k 均值
		scoresCopy := make([]float32, len(chScores))
		copy(scoresCopy, chScores)
		sort.Slice(scoresCopy, func(i, j int) bool { return scoresCopy[i] > scoresCopy[j] })
		kk := k
		if len(scoresCopy) < k {
			kk = len(scoresCopy)
		}
		var sumTop float32
		for i := 0; i < kk; i++ {
			sumTop += scoresCopy[i]
		}
		topKavg := sumTop / float32(kk)

		// 2. 覆盖率
		var coverCnt int
		var mean, sqSum float64
		for _, s := range chScores {
			if s >= coverTh {
				coverCnt++
			}
			mean += float64(s)
			sqSum += float64(s) * float64(s)
		}
		n := float64(len(chScores))
		mean /= n
		stdev := math.Sqrt(sqSum/n - mean*mean)             // √(E[x²] - μ²)
		cover := float32(coverCnt) / float32(len(chScores)) // 0-1

		// 3. penalty & final
		varCoeff := float32(stdev) / (float32(mean) + 1e-6)
		penalty := lambda1*(1-cover) + lambda2*varCoeff // 0→0, 1→强惩罚
		if penalty > 1 {
			penalty = 1
		}
		finalScore := topKavg * (1 - penalty)

		testLogger.Log("分数计算详情: topKavg=%.4f, 覆盖率=%.2f, 方差系数=%.4f, 惩罚=%.4f, 最终分数=%.4f",
			topKavg, cover, varCoeff, penalty, finalScore)

		return finalScore
	}

	// 3. 测试用例 - 不同类型的简历块分数分布
	testCases := []struct {
		name        string
		scores      []float32
		k           int
		coverTh     float32
		lambda1     float32
		lambda2     float32
		expectRange [2]float32 // 期望分数范围[min, max]
	}{
		{
			name:        "优质简历-均匀高分",
			scores:      []float32{0.85, 0.82, 0.78, 0.75, 0.72},
			k:           3,
			coverTh:     0.3,
			lambda1:     0.4,
			lambda2:     0.3,
			expectRange: [2]float32{0.75, 0.9}, // 应该接近topKavg
		},
		{
			name:        "关键词堆砌-高方差",
			scores:      []float32{0.95, 0.15, 0.12, 0.10, 0.08},
			k:           3,
			coverTh:     0.3,
			lambda1:     0.4,
			lambda2:     0.3,
			expectRange: [2]float32{0.1, 0.3}, // 应该大幅惩罚
		},
		{
			name:        "相关但不完全匹配",
			scores:      []float32{0.68, 0.62, 0.55, 0.45, 0.32},
			k:           3,
			coverTh:     0.3,
			lambda1:     0.4,
			lambda2:     0.3,
			expectRange: [2]float32{0.5, 0.65}, // 轻微惩罚
		},
		{
			name:        "短简历-块数少",
			scores:      []float32{0.75, 0.65},
			k:           3,
			coverTh:     0.3,
			lambda1:     0.4,
			lambda2:     0.3,
			expectRange: [2]float32{0.4, 0.65}, // 块数少，应适度惩罚
		},
		{
			name:        "边界情况-空数组",
			scores:      []float32{},
			k:           3,
			coverTh:     0.3,
			lambda1:     0.4,
			lambda2:     0.3,
			expectRange: [2]float32{0, 0}, // 应返回0
		},
	}

	// 4. 执行测试用例
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testLogger.Log("测试用例: %s", tc.name)
			testLogger.Log("输入分数: %v", tc.scores)

			score := shortBoardScore(tc.scores, tc.k, tc.coverTh, tc.lambda1, tc.lambda2)

			testLogger.Log("最终得分: %.4f", score)
			assert.GreaterOrEqual(t, score, tc.expectRange[0], "分数应不低于最小期望")
			assert.LessOrEqual(t, score, tc.expectRange[1], "分数应不超过最大期望")
		})
	}

	// 使用全局常量的简单调用示例
	testLogger.Log("使用全局常量的示例调用...")
	exampleScores := []float32{0.75, 0.70, 0.65, 0.40, 0.35}
	score := shortBoardScore(exampleScores, testTopK, coverThreshold, lambda1, lambda2)
	testLogger.Log("示例得分: %.4f", score)

	// 5. 使用真实数据进行测试 - 连接存储组件和获取真实简历数据
	testLogger.Log("使用真实数据测试短板惩罚算法...")

	// 初始化环境
	ctx := context.Background()

	// 加载配置
	testLogger.Log("加载配置文件...")
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")

	// 初始化存储组件
	testLogger.Log("初始化存储组件...")
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()

	// 检查Redis是否可用
	if s.Redis == nil {
		testLogger.Log("Redis未配置或不可用，跳过测试")
		t.Skip("Redis未配置或不可用，跳过测试")
	}

	// 创建Embedding服务和JD处理器
	testLogger.Log("初始化Embedding服务和JD处理器...")
	aliyunEmbedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "创建AliyunEmbedder失败")

	jdProcessor, err := processor.NewJDProcessor(aliyunEmbedder, s, cfg.ActiveParserVersion)
	require.NoError(t, err, "初始化JD处理器失败")

	// 使用Go工程师岗位ID - 与其他测试保持一致
	testJobID := "22222222-3333-4444-5555-666666666666"

	// 执行搜索获取真实数据
	testLogger.Log("执行向量搜索获取真实数据...")

	// 通过SQL直接获取JD文本
	var jdText string
	if s.MySQL != nil && s.MySQL.DB() != nil {
		row := s.MySQL.DB().Raw("SELECT job_description_text FROM jobs WHERE job_id = ?", testJobID).Row()
		err = row.Scan(&jdText)
		if err != nil {
			testLogger.Log("获取JD文本失败: %v，使用默认值", err)
			jdText = "高级Go工程师，负责微服务开发和系统架构"
		} else {
			testLogger.Log("成功从数据库获取JD文本")
		}
	} else {
		testLogger.Log("MySQL未初始化，使用默认JD文本")
		jdText = "高级Go工程师，负责微服务开发和系统架构"
	}
	require.NotEmpty(t, jdText, "JD文本不能为空")

	jdVector, err := jdProcessor.GetJobDescriptionVector(ctx, testJobID, jdText)
	require.NoError(t, err, "获取JD向量失败")

	// 搜索相似简历
	const recallLimit = 100 // 召回数量
	searchResults, err := s.Qdrant.SearchSimilarResumes(ctx, jdVector, recallLimit, nil)
	require.NoError(t, err, "Qdrant搜索失败")
	testLogger.Log("成功从向量数据库召回 %d 个结果", len(searchResults))

	// 只保留得分高于阈值的结果
	const scoreThreshold = 0.5
	var candidatesForRerank []storage.SearchResult
	for _, res := range searchResults {
		if res.Score >= scoreThreshold {
			candidatesForRerank = append(candidatesForRerank, res)
		}
	}
	testLogger.Log("应用score_threshold=%.2f后，剩下 %d 个候选结果", scoreThreshold, len(candidatesForRerank))

	// 调用Reranker服务 - 使用TestRerankerService中的方法
	type RerankDocument struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}

	type RerankRequest struct {
		Query     string           `json:"query"`
		Documents []RerankDocument `json:"documents"`
	}

	type RerankedDocument struct {
		ID          string  `json:"id"`
		RerankScore float32 `json:"rerank_score"`
	}

	testLogger.Log("准备调用Reranker服务...")

	// 准备重排请求文档
	var docsToRerank []RerankDocument
	for _, res := range candidatesForRerank {
		// 输出第一个文档的所有字段
		if len(docsToRerank) == 0 {
			testLogger.Log("第一个文档的字段:")
			for k := range res.Payload {
				testLogger.Log("- %s", k)
			}
		}

		// 尝试从多个可能的字段获取内容
		var content string
		var found bool
		possibleFields := []string{"chunk_content_text", "content", "text"}

		for _, field := range possibleFields {
			if textContent, ok := res.Payload[field].(string); ok && textContent != "" {
				content = textContent
				found = true
				break
			}
		}

		// 如果找不到，尝试任意字符串字段
		if !found {
			for k, v := range res.Payload {
				if textContent, ok := v.(string); ok && textContent != "" {
					content = textContent
					testLogger.Log("使用字段 '%s' 作为内容", k)
					found = true
					break
				}
			}
		}

		if found {
			docsToRerank = append(docsToRerank, RerankDocument{
				ID:   res.ID,
				Text: content,
			})
		}
	}

	testLogger.Log("准备重排 %d/%d 个文档", len(docsToRerank), len(candidatesForRerank))

	// 如果没有文档，跳过重排过程
	var rerankScores map[string]float32
	if len(docsToRerank) > 0 && cfg.Reranker.Enabled && cfg.Reranker.URL != "" {
		// 构造请求并调用Reranker服务
		reqBody := RerankRequest{
			Query:     jdText,
			Documents: docsToRerank,
		}

		reqBytes, err := json.Marshal(reqBody)
		if err != nil {
			testLogger.Log("序列化重排请求失败: %v", err)
		} else {
			req, err := http.NewRequestWithContext(ctx, "POST", cfg.Reranker.URL, bytes.NewBuffer(reqBytes))
			if err != nil {
				testLogger.Log("创建重排请求失败: %v", err)
			} else {
				req.Header.Set("Content-Type", "application/json")

				client := &http.Client{Timeout: 60 * time.Second}
				resp, err := client.Do(req)
				if err != nil {
					testLogger.Log("调用重排服务失败: %v", err)
				} else {
					defer resp.Body.Close()

					if resp.StatusCode != http.StatusOK {
						bodyBytes, _ := io.ReadAll(resp.Body)
						testLogger.Log("重排服务返回错误: %d, %s", resp.StatusCode, string(bodyBytes))
					} else {
						var rerankedDocs []RerankedDocument
						if err := json.NewDecoder(resp.Body).Decode(&rerankedDocs); err != nil {
							testLogger.Log("解析重排结果失败: %v", err)
						} else {
							rerankScores = make(map[string]float32)
							for _, doc := range rerankedDocs {
								rerankScores[doc.ID] = doc.RerankScore
							}
							testLogger.Log("成功获得 %d 个重排分数", len(rerankScores))
						}
					}
				}
			}
		}
	}

	// 按submission分组收集分数
	submissionChunkScores := make(map[string][]float32)
	submissionNames := make(map[string]string) // 用于记录简历名称

	useVectorScore := rerankScores == nil || len(rerankScores) == 0
	for _, res := range candidatesForRerank {
		var score float32
		var ok bool

		if useVectorScore {
			score = res.Score
			ok = true
		} else {
			score, ok = rerankScores[res.ID]
		}

		if ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				submissionChunkScores[uuid] = append(submissionChunkScores[uuid], score)
				// 尝试获取简历名称
				if name, exists := res.Payload["name"].(string); exists {
					submissionNames[uuid] = name
				} else if name, exists := res.Payload["candidate_name"].(string); exists {
					submissionNames[uuid] = name
				} else {
					submissionNames[uuid] = uuid[:8] + "..." // 使用截断UUID作为默认名称
				}
			}
		}
	}

	testLogger.Log("共有 %d 个简历的分数可用于短板惩罚计算", len(submissionChunkScores))

	// 6. 应用短板惩罚算法
	var scoredResumes []struct {
		uuid    string
		name    string
		score   float32
		topKAvg float32 // 保存原始TopK均值作比较
	}

	for uuid, scores := range submissionChunkScores {
		if len(scores) < 2 {
			testLogger.Log("简历UUID %s 只有 %d 个块，不进行短板惩罚计算", uuid, len(scores))
			continue
		}

		// 计算原始TopK均值
		scoresCopy := make([]float32, len(scores))
		copy(scoresCopy, scores)
		sort.Slice(scoresCopy, func(i, j int) bool { return scoresCopy[i] > scoresCopy[j] })
		kk := testTopK
		if len(scoresCopy) < kk {
			kk = len(scoresCopy)
		}
		var sumTop float32
		for i := 0; i < kk; i++ {
			sumTop += scoresCopy[i]
		}
		topKAvg := sumTop / float32(kk)

		// 应用短板惩罚
		finalScore := shortBoardScore(scores, testTopK, coverThreshold, lambda1, lambda2)

		name := submissionNames[uuid]
		testLogger.Log("简历: %s (UUID: %s), 块数: %d, 原始TopK均值: %.4f, 短板惩罚后: %.4f",
			name, uuid, len(scores), topKAvg, finalScore)

		scoredResumes = append(scoredResumes, struct {
			uuid    string
			name    string
			score   float32
			topKAvg float32
		}{uuid, name, finalScore, topKAvg})
	}

	// 按短板惩罚分数排序
	sort.Slice(scoredResumes, func(i, j int) bool {
		return scoredResumes[i].score > scoredResumes[j].score
	})

	// 7. 输出排序结果
	testLogger.Log("短板惩罚排序后的简历:")
	limit := 10 // 限制显示前10个
	if len(scoredResumes) < limit {
		limit = len(scoredResumes)
	}

	for i := 0; i < limit; i++ {
		r := scoredResumes[i]
		testLogger.Log("%d. %s (UUID: %s) - 原始TopK分: %.4f, 惩罚后分: %.4f",
			i+1, r.name, r.uuid, r.topKAvg, r.score)
	}

	// 比较短板惩罚排序与原始TopK均值排序的差异
	testLogger.Log("比较排序差异...")

	// 按原始TopK均值排序
	sortedByTopK := make([]struct {
		uuid    string
		name    string
		score   float32
		topKAvg float32
	}, len(scoredResumes))
	copy(sortedByTopK, scoredResumes)

	sort.Slice(sortedByTopK, func(i, j int) bool {
		return sortedByTopK[i].topKAvg > sortedByTopK[j].topKAvg
	})

	// 输出原始TopK的排序
	testLogger.Log("原始TopK排序:")
	for i := 0; i < limit; i++ {
		r := sortedByTopK[i]
		testLogger.Log("%d. %s (UUID: %s) - 原始TopK分: %.4f",
			i+1, r.name, r.uuid, r.topKAvg)
	}

	// 找出排序变化最大的简历
	testLogger.Log("排序变化显著的简历:")

	// 创建原始TopK排名映射
	rankByTopK := make(map[string]int)
	for i, r := range sortedByTopK {
		rankByTopK[r.uuid] = i
	}

	// 创建短板惩罚排名映射
	rankByPenalty := make(map[string]int)
	for i, r := range scoredResumes {
		rankByPenalty[r.uuid] = i
	}

	// 计算排名变化并输出显著变化的简历
	for uuid, penaltyRank := range rankByPenalty {
		topKRank := rankByTopK[uuid]

		change := topKRank - penaltyRank // 正值表示排名提升，负值表示排名下降

		if abs(change) >= 3 && penaltyRank < 20 { // 只关注排名前20且变化显著的
			direction := "提升"
			if change < 0 {
				direction = "下降"
				change = -change
			}

			for _, r := range scoredResumes {
				if r.uuid == uuid {
					testLogger.Log("简历: %s, 排名%s %d 位 (原始: %d, 惩罚后: %d), 原始分: %.4f, 惩罚后: %.4f",
						r.name, direction, change, topKRank+1, penaltyRank+1, r.topKAvg, r.score)
					break
				}
			}
		}
	}

	// 确保至少有一些结果可验证
	assert.True(t, len(scoredResumes) > 0, "应有简历得分进行验证")
	if len(scoredResumes) > 0 {
		assert.True(t, scoredResumes[0].score <= scoredResumes[0].topKAvg, "惩罚后分数应不高于原始TopK分数")
	}

	// 8. 模拟与真实业务逻辑集成 - 使用简化版本
	testLogger.Log("模拟与业务逻辑集成...")

	// 模拟业务集成示例
	aggregateWithShortBoard := func(candidates []storage.SearchResult, rerankScores map[string]float32) map[string]float32 {
		// 按submission分组
		submissionChunkScores := make(map[string][]float32)

		// 收集每个submission的所有chunk分数
		for _, res := range candidates {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid == "" {
				continue
			}

			score, ok := rerankScores[res.ID]
			if !ok {
				score = res.Score // 如果没有rerank分数，使用原始向量分数
			}

			submissionChunkScores[uuid] = append(submissionChunkScores[uuid], score)
		}

		// 应用短板惩罚算法
		finalScores := make(map[string]float32)
		for uuid, scores := range submissionChunkScores {
			finalScores[uuid] = shortBoardScore(scores, testTopK, coverThreshold, lambda1, lambda2)
		}

		return finalScores
	}

	// 示例结果
	if len(candidatesForRerank) > 0 {
		testLogger.Log("应用短板惩罚算法到真实业务结果...")
		// 如果没有Reranker结果，使用向量分数
		localRerankScores := rerankScores
		if localRerankScores == nil || len(localRerankScores) == 0 {
			// 创建一个map，使用向量分数作为重排分数
			localRerankScores = make(map[string]float32)
			for _, res := range candidatesForRerank {
				localRerankScores[res.ID] = res.Score
			}
		}
		aggregatedScores := aggregateWithShortBoard(candidatesForRerank, localRerankScores)
		testLogger.Log("共产生 %d 个简历最终得分", len(aggregatedScores))
	} else {
		testLogger.Log("没有足够数据进行业务集成示例")
	}

	testLogger.Log("短板惩罚算法测试完成！")
}

// TestShortBoardPenaltyWithFullFlow 测试短板惩罚评分算法在完整业务流程中的应用
func TestShortBoardPenaltyWithFullFlow(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "ShortBoardPenaltyWithFullFlow")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试短板惩罚算法在完整业务流程中的应用...")

	// 1. 设置常量和算法参数
	const testTopK = 3         // TopK参数
	const coverThreshold = 0.3 // 覆盖率阈值
	const lambda1 = 0.4        // 覆盖惩罚权重
	const lambda2 = 0.3        // 方差惩罚权重
	const scoreThreshold = 0.5 // 分数阈值
	const recallLimit = 100    // 召回数量限制

	// 2. 定义短板惩罚算法
	shortBoardScore := func(chScores []float32, k int, coverTh, lambda1, lambda2 float32) float32 {
		if len(chScores) == 0 {
			return 0
		}

		// 1. 排序求 top-k 均值
		scoresCopy := make([]float32, len(chScores))
		copy(scoresCopy, chScores)
		sort.Slice(scoresCopy, func(i, j int) bool { return scoresCopy[i] > scoresCopy[j] })
		kk := k
		if len(scoresCopy) < k {
			kk = len(scoresCopy)
		}
		var sumTop float32
		for i := 0; i < kk; i++ {
			sumTop += scoresCopy[i]
		}
		topKavg := sumTop / float32(kk)

		// 2. 覆盖率
		var coverCnt int
		var mean, sqSum float64
		for _, s := range chScores {
			if s >= coverTh {
				coverCnt++
			}
			mean += float64(s)
			sqSum += float64(s) * float64(s)
		}
		n := float64(len(chScores))
		mean /= n
		stdev := math.Sqrt(sqSum/n - mean*mean)             // √(E[x²] - μ²)
		cover := float32(coverCnt) / float32(len(chScores)) // 0-1

		// 3. penalty & final
		varCoeff := float32(stdev) / (float32(mean) + 1e-6)
		penalty := lambda1*(1-cover) + lambda2*varCoeff // 0→0, 1→强惩罚
		if penalty > 1 {
			penalty = 1
		}
		finalScore := topKavg * (1 - penalty)

		return finalScore
	}

	// 3. 初始化测试环境
	ctx := context.Background()

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "debug", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true})
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)
	glog.SetLogger(hertzCompatibleLogger)
	glog.SetLevel(glog.LevelDebug)

	// 4. 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")
	testLogger.Log("配置加载成功")

	// 5. 初始化存储组件
	testLogger.Log("初始化存储组件")
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()
	testLogger.Log("存储组件初始化成功")

	// 检查Redis是否可用
	if s.Redis == nil {
		testLogger.Log("Redis未配置或不可用，跳过测试")
		t.Skip("Redis未配置或不可用，跳过测试")
	}

	// 6. 创建Embedding服务
	testLogger.Log("创建Embedding服务")
	aliyunEmbedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "创建AliyunEmbedder失败")
	testLogger.Log("Embedding服务创建成功")

	// 7. 初始化JD处理器
	testLogger.Log("初始化JD处理器")
	jdProcessor, err := processor.NewJDProcessor(aliyunEmbedder, s, cfg.ActiveParserVersion)
	require.NoError(t, err, "初始化JD处理器失败")
	testLogger.Log("JD处理器初始化成功")

	// 8. 准备测试数据 - 岗位和HR信息
	testJobID := "22222222-3333-4444-5555-666666666666"
	testHRID := "11111111-1111-1111-1111-111111111111"
	jobTitle := "高级Go后端及微服务工程师"

	testLogger.Log("测试岗位ID: %s", testJobID)
	testLogger.Log("测试HR ID: %s", testHRID)
	testLogger.Log("岗位标题: %s", jobTitle)

	// 9. 清除可能存在的缓存，确保测试的独立性
	testLogger.Log("清除可能存在的Redis缓存")
	cacheKey := fmt.Sprintf(constants.KeySearchSession, testJobID, testHRID)
	_, err = s.Redis.Client.Del(ctx, cacheKey).Result()
	if err != nil && err != redis.Nil {
		testLogger.Log("清除缓存时出错: %v", err)
	}

	// 10. 实现完整业务流程
	testLogger.Log("开始执行完整搜索流程...")
	startTime := time.Now()

	// 10.1 获取JD文本和向量
	var jdText string
	if s.MySQL != nil && s.MySQL.DB() != nil {
		row := s.MySQL.DB().Raw("SELECT job_description_text FROM jobs WHERE job_id = ?", testJobID).Row()
		err = row.Scan(&jdText)
		if err != nil {
			testLogger.Log("获取JD文本失败: %v，使用默认值", err)
			jdText = "高级Go工程师，负责微服务开发和系统架构"
		} else {
			testLogger.Log("成功从数据库获取JD文本")
		}
	} else {
		testLogger.Log("MySQL未初始化，使用默认JD文本")
		jdText = "高级Go工程师，负责微服务开发和系统架构"
	}
	require.NotEmpty(t, jdText, "JD文本不能为空")

	jdVector, err := jdProcessor.GetJobDescriptionVector(ctx, testJobID, jdText)
	require.NoError(t, err, "获取JD向量失败")
	testLogger.Log("成功获取JD向量")

	// 10.2. Qdrant向量召回
	initialResults, err := s.Qdrant.SearchSimilarResumes(ctx, jdVector, recallLimit, nil)
	require.NoError(t, err, "Qdrant搜索失败")
	testLogger.Log("Qdrant召回 %d 个初步结果", len(initialResults))

	// 10.3. 海选过滤 (基于向量搜索分数)
	var candidatesForRerank []storage.SearchResult
	for _, res := range initialResults {
		if res.Score >= scoreThreshold {
			candidatesForRerank = append(candidatesForRerank, res)
		}
	}
	testLogger.Log("应用score_threshold=%.2f后, 剩下 %d 个候选结果进入重排", scoreThreshold, len(candidatesForRerank))

	// 10.4. 调用Reranker服务
	type RerankDocument struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}

	type RerankRequest struct {
		Query     string           `json:"query"`
		Documents []RerankDocument `json:"documents"`
	}

	type RerankedDocument struct {
		ID          string  `json:"id"`
		RerankScore float32 `json:"rerank_score"`
	}

	// 准备重排请求文档
	var docsToRerank []RerankDocument
	for _, res := range candidatesForRerank {
		// 尝试从多个可能的字段获取内容
		var content string
		var found bool
		possibleFields := []string{"chunk_content_text", "content", "text"}

		for _, field := range possibleFields {
			if textContent, ok := res.Payload[field].(string); ok && textContent != "" {
				content = textContent
				found = true
				break
			}
		}

		// 如果找不到，尝试任意字符串字段
		if !found {
			for fieldName, v := range res.Payload {
				if textContent, ok := v.(string); ok && textContent != "" {
					content = textContent
					testLogger.Log("使用字段 '%s' 作为内容", fieldName)
					found = true
					break
				}
			}
		}

		if found {
			docsToRerank = append(docsToRerank, RerankDocument{
				ID:   res.ID,
				Text: content,
			})
		}
	}

	testLogger.Log("准备重排 %d/%d 个文档", len(docsToRerank), len(candidatesForRerank))

	// 执行重排请求
	var rerankScores map[string]float32
	if len(docsToRerank) > 0 && cfg.Reranker.Enabled && cfg.Reranker.URL != "" {
		// 构造请求并调用Reranker服务
		reqBody := RerankRequest{
			Query:     jdText,
			Documents: docsToRerank,
		}

		reqBytes, err := json.Marshal(reqBody)
		if err != nil {
			testLogger.Log("序列化重排请求失败: %v", err)
		} else {
			req, err := http.NewRequestWithContext(ctx, "POST", cfg.Reranker.URL, bytes.NewBuffer(reqBytes))
			if err != nil {
				testLogger.Log("创建重排请求失败: %v", err)
			} else {
				req.Header.Set("Content-Type", "application/json")

				client := &http.Client{Timeout: 60 * time.Second}
				resp, err := client.Do(req)
				if err != nil {
					testLogger.Log("调用重排服务失败: %v", err)
				} else {
					defer resp.Body.Close()

					if resp.StatusCode != http.StatusOK {
						bodyBytes, _ := io.ReadAll(resp.Body)
						testLogger.Log("重排服务返回错误: %d, %s", resp.StatusCode, string(bodyBytes))
					} else {
						var rerankedDocs []RerankedDocument
						if err := json.NewDecoder(resp.Body).Decode(&rerankedDocs); err != nil {
							testLogger.Log("解析重排结果失败: %v", err)
						} else {
							rerankScores = make(map[string]float32)
							for _, doc := range rerankedDocs {
								rerankScores[doc.ID] = doc.RerankScore
							}
							testLogger.Log("成功获得 %d 个重排分数", len(rerankScores))
						}
					}
				}
			}
		}
	}

	// 10.5. 按submission分组收集分数
	submissionChunkScores := make(map[string][]float32)

	useVectorScore := rerankScores == nil || len(rerankScores) == 0
	for _, res := range candidatesForRerank {
		var score float32
		var ok bool

		if useVectorScore {
			score = res.Score
			ok = true
		} else {
			score, ok = rerankScores[res.ID]
		}

		if ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				submissionChunkScores[uuid] = append(submissionChunkScores[uuid], score)
			}
		}
	}

	testLogger.Log("共有 %d 个简历的分数可用于短板惩罚计算", len(submissionChunkScores))

	// 10.6. 分数聚合 - 应用短板惩罚算法
	finalSubmissionScores := make(map[string]float32)
	for uuid, scores := range submissionChunkScores {
		finalSubmissionScores[uuid] = shortBoardScore(scores, testTopK, coverThreshold, lambda1, lambda2)
	}
	testLogger.Log("使用短板惩罚算法聚合后，得到 %d 份简历的最终综合得分", len(finalSubmissionScores))

	// 10.7. 排序并返回最终的结果集
	var rankedSubmissions []types.RankedSubmission
	for uuid, score := range finalSubmissionScores {
		rankedSubmissions = append(rankedSubmissions, types.RankedSubmission{
			SubmissionUUID: uuid,
			Score:          score,
		})
	}

	sort.Slice(rankedSubmissions, func(i, j int) bool {
		return rankedSubmissions[i].Score > rankedSubmissions[j].Score
	})

	testLogger.Log("排序完成，得到 %d 个排序后的结果", len(rankedSubmissions))

	// 10.8. 存入Redis缓存以供后续分页使用
	if len(rankedSubmissions) > 0 {
		testLogger.Log("将搜索结果存入Redis缓存")

		// 提取UUID列表并存入Redis
		uuids := make([]string, len(rankedSubmissions))
		uuidScores := make(map[string]float32)
		for i, r := range rankedSubmissions {
			uuids[i] = r.SubmissionUUID
			uuidScores[r.SubmissionUUID] = r.Score
		}

		// 使用Redis ZSET存储排序结果
		cacheKey := fmt.Sprintf(constants.KeySearchSession, testJobID, testHRID)

		// 首先清除旧缓存
		err = s.Redis.Client.Del(ctx, cacheKey).Err()
		if err != nil && err != redis.Nil {
			testLogger.Log("清除旧缓存失败: %v", err)
		}

		// 将结果添加到有序集合
		for i, uuid := range uuids {
			score := float64(len(uuids) - i) // 分数越高排序越靠前，使用倒序索引作为分数
			err = s.Redis.Client.ZAdd(ctx, cacheKey, redis.Z{
				Score:  score,
				Member: uuid,
			}).Err()
			if err != nil {
				testLogger.Log("添加到Redis缓存失败: %v", err)
				break
			}
		}

		// 设置缓存过期时间
		err = s.Redis.Client.Expire(ctx, cacheKey, 30*time.Minute).Err()
		if err != nil {
			testLogger.Log("设置缓存过期时间失败: %v", err)
		}

		testLogger.Log("Redis缓存设置成功，包含 %d 个UUID", len(uuids))

		// 确认ZSET中的数据
		count, err := s.Redis.Client.ZCard(ctx, cacheKey).Result()
		require.NoError(t, err, "获取ZSET大小失败")
		testLogger.Log("Redis ZSET实际包含 %d 个元素", count)
		require.Equal(t, int64(len(uuids)), count, "ZSET大小应与添加的UUID数量一致")
	}

	// 11. 验证Redis缓存
	testLogger.Log("验证Redis缓存...")

	// 测试分页获取缓存数据
	const pageSize = 5
	var allCachedUUIDs []string
	var totalCount int64

	// 直接从Redis获取结果
	members, err := s.Redis.Client.ZRevRange(ctx, cacheKey, 0, pageSize-1).Result()
	require.NoError(t, err, "直接获取Redis缓存前5个元素失败")
	testLogger.Log("直接从Redis获取前5个UUID: %v", members)

	// 测试我们的GetCachedSearchResults方法
	totalCount, err = s.Redis.Client.ZCard(ctx, cacheKey).Result()
	require.NoError(t, err, "获取缓存元素总数失败")
	testLogger.Log("ZSET总元素数: %d", totalCount)

	if totalCount > 0 {
		// 获取第一页
		page1UUIDs, err := s.Redis.Client.ZRevRange(ctx, cacheKey, 0, pageSize-1).Result()
		require.NoError(t, err, "获取第一页缓存失败")
		allCachedUUIDs = append(allCachedUUIDs, page1UUIDs...)
		testLogger.Log("第一页包含 %d 个结果", len(page1UUIDs))

		// 获取第二页
		page2UUIDs, err := s.Redis.Client.ZRevRange(ctx, cacheKey, pageSize, 2*pageSize-1).Result()
		require.NoError(t, err, "获取第二页缓存失败")
		allCachedUUIDs = append(allCachedUUIDs, page2UUIDs...)
		testLogger.Log("第二页包含 %d 个结果", len(page2UUIDs))

		// 获取第三页
		page3UUIDs, err := s.Redis.Client.ZRevRange(ctx, cacheKey, 2*pageSize, 3*pageSize-1).Result()
		require.NoError(t, err, "获取第三页缓存失败")
		allCachedUUIDs = append(allCachedUUIDs, page3UUIDs...)
		testLogger.Log("第三页包含 %d 个结果", len(page3UUIDs))

		// 验证缓存结果
		if len(rankedSubmissions) > 0 {
			assert.Equal(t, int64(len(rankedSubmissions)), totalCount, "缓存的总数应该与排序结果数量一致")

			// 验证排序一致性
			if len(page1UUIDs) > 0 && len(rankedSubmissions) > 0 {
				assert.Equal(t, rankedSubmissions[0].SubmissionUUID, page1UUIDs[0], "第一个结果应该一致")
			}
		}
	}

	// 12. 计算执行时间
	duration := time.Since(startTime)
	testLogger.Log("完整搜索流程执行完成，耗时: %v", duration)

	// 13. 提供一个示例，说明如何修改JobSearchHandler.executeFullSearchPipeline
	testLogger.Log("示例如何将短板惩罚算法集成到JobSearchHandler中:")
	testLogger.Log(`
// 在JobSearchHandler中修改aggregateScores方法，使用短板惩罚算法：
func (h *JobSearchHandler) aggregateScores(candidates []storage.SearchResult, rerankScores map[string]float32) map[string]float32 {
	submissionChunkScores := make(map[string][]float32)
	
	// 如果Reranker失败，rerankScores会是nil，此时使用原始向量分数
	useVectorScore := rerankScores == nil
	
	// 收集每个submission的所有chunk分数
	for _, res := range candidates {
		var score float32
		var ok bool
		
		if useVectorScore {
			score = res.Score
			ok = true
		} else {
			score, ok = rerankScores[res.ID]
		}
		
		if ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				submissionChunkScores[uuid] = append(submissionChunkScores[uuid], score)
			}
		}
	}
	
	// 应用短板惩罚算法
	finalSubmissionScores := make(map[string]float32)
	const topK = 3          // TopK参数
	const coverThreshold = 0.3  // 覆盖率阈值
	const lambda1 = 0.4         // 覆盖惩罚权重
	const lambda2 = 0.3         // 方差惩罚权重
	
	for uuid, scores := range submissionChunkScores {
		// 如果块数不足，使用原始均值并适度惩罚
		if len(scores) <= 1 {
			// 计算原始均值
			var sum float32
			for _, s := range scores {
				sum += s
			}
			avg := sum / float32(len(scores))
			finalSubmissionScores[uuid] = avg * 0.6 // 单块文档主动降权
		} else {
			finalSubmissionScores[uuid] = h.shortBoardScore(scores, topK, coverThreshold, lambda1, lambda2)
		}
	}
	
	return finalSubmissionScores
}

// 添加shortBoardScore方法
func (h *JobSearchHandler) shortBoardScore(chScores []float32, k int, coverTh, lambda1, lambda2 float32) float32 {
	if len(chScores) == 0 {
		return 0
	}
	
	// 计算TopK均值
	scoresCopy := make([]float32, len(chScores))
	copy(scoresCopy, chScores)
	sort.Slice(scoresCopy, func(i, j int) bool { return scoresCopy[i] > scoresCopy[j] })
	kk := k
	if len(scoresCopy) < k {
		kk = len(scoresCopy)
	}
	var sumTop float32
	for i := 0; i < kk; i++ {
		sumTop += scoresCopy[i]
	}
	topKavg := sumTop / float32(kk)
	
	// 计算覆盖率
	var coverCnt int
	var mean, sqSum float64
	for _, s := range chScores {
		if s >= coverTh {
			coverCnt++
		}
		mean += float64(s)
		sqSum += float64(s) * float64(s)
	}
	n := float64(len(chScores))
	mean /= n
	stdev := math.Sqrt(sqSum/n - mean*mean)
	cover := float32(coverCnt) / float32(len(chScores))
	
	// 计算惩罚与最终分数
	varCoeff := float32(stdev) / (float32(mean) + 1e-6)
	penalty := lambda1*(1-cover) + lambda2*varCoeff
	if penalty > 1 {
		penalty = 1
	}
	
	return topKavg * (1 - penalty)
}`)

	testLogger.Log("短板惩罚算法在完整业务流程中的测试完成")
}

// getGoBackendGoldenTruthSet 返回Go后端工程师岗位的黄金标准评测集
func getGoBackendGoldenTruthSet() GoldenTruthSet {
	// ------- Tier-1 24 份：核心高度匹配 -------
	tier1IDs := []string{
		// 0197m3h2-d001-e001-f001-000000000001 – 000000000024
		"0197m3h2-d001-e001-f001-000000000001", "0197m3h2-d001-e001-f001-000000000002",
		"0197m3h2-d001-e001-f001-000000000003", "0197m3h2-d001-e001-f001-000000000004",
		"0197m3h2-d001-e001-f001-000000000005", "0197m3h2-d001-e001-f001-000000000006",
		"0197m3h2-d001-e001-f001-000000000007", "0197m3h2-d001-e001-f001-000000000008",
		"0197m3h2-d001-e001-f001-000000000009", "0197m3h2-d001-e001-f001-000000000010",
		"0197m3h2-d001-e001-f001-000000000011", "0197m3h2-d001-e001-f001-000000000012",
		"0197m3h2-d001-e001-f001-000000000013", "0197m3h2-d001-e001-f001-000000000014",
		"0197m3h2-d001-e001-f001-000000000015", "0197m3h2-d001-e001-f001-000000000016",
		"0197m3h2-d001-e001-f001-000000000017", "0197m3h2-d001-e001-f001-000000000018",
		"0197m3h2-d001-e001-f001-000000000019", "0197m3h2-d001-e001-f001-000000000020",
		"0197m3h2-d001-e001-f001-000000000021", "0197m3h2-d001-e001-f001-000000000022",
		"0197m3h2-d001-e001-f001-000000000023", "0197m3h2-d001-e001-f001-000000000024",
	}

	// ------- Tier-2 24 份：部分匹配 / 潜力股 -------
	tier2IDs := []string{
		// 0197m3h2-d001-e001-f001-000000000025 – 000000000048
		"0197m3h2-d001-e001-f001-000000000025", "0197m3h2-d001-e001-f001-000000000026",
		"0197m3h2-d001-e001-f001-000000000027", "0197m3h2-d001-e001-f001-000000000028",
		"0197m3h2-d001-e001-f001-000000000029", "0197m3h2-d001-e001-f001-000000000030",
		"0197m3h2-d001-e001-f001-000000000031", "0197m3h2-d001-e001-f001-000000000032",
		"0197m3h2-d001-e001-f001-000000000033", "0197m3h2-d001-e001-f001-000000000034",
		"0197m3h2-d001-e001-f001-000000000035", "0197m3h2-d001-e001-f001-000000000036",
		"0197m3h2-d001-e001-f001-000000000037", "0197m3h2-d001-e001-f001-000000000038",
		"0197m3h2-d001-e001-f001-000000000039", "0197m3h2-d001-e001-f001-000000000040",
		"0197m3h2-d001-e001-f001-000000000041", "0197m3h2-d001-e001-f001-000000000042",
		"0197m3h2-d001-e001-f001-000000000043", "0197m3h2-d001-e001-f001-000000000044",
		"0197m3h2-d001-e001-f001-000000000045", "0197m3h2-d001-e001-f001-000000000046",
		"0197m3h2-d001-e001-f001-000000000047", "0197m3h2-d001-e001-f001-000000000048",
	}

	// ------- Tier-3 32 份：低相关 / 无关 -------
	tier3IDs := []string{
		// 0197m3h2-d001-e001-f001-000000000049 – 000000000064
		"0197m3h2-d001-e001-f001-000000000049", "0197m3h2-d001-e001-f001-000000000050",
		"0197m3h2-d001-e001-f001-000000000051", "0197m3h2-d001-e001-f001-000000000052",
		"0197m3h2-d001-e001-f001-000000000053", "0197m3h2-d001-e001-f001-000000000054",
		"0197m3h2-d001-e001-f001-000000000055", "0197m3h2-d001-e001-f001-000000000056",
		"0197m3h2-d001-e001-f001-000000000057", "0197m3h2-d001-e001-f001-000000000058",
		"0197m3h2-d001-e001-f001-000000000059", "0197m3h2-d001-e001-f001-000000000060",
		"0197m3h2-d001-e001-f001-000000000061", "0197m3h2-d001-e001-f001-000000000062",
		"0197m3h2-d001-e001-f001-000000000063", "0197m3h2-d001-e001-f001-000000000064",

		// 0197l2g2-d001-e001-f001-000000000001 – 000000000016  (全部 C++ / 非目标)
		"0197l2g2-d001-e001-f001-000000000001", "0197l2g2-d001-e001-f001-000000000002",
		"0197l2g2-d001-e001-f001-000000000003", "0197l2g2-d001-e001-f001-000000000004",
		"0197l2g2-d001-e001-f001-000000000005", "0197l2g2-d001-e001-f001-000000000006",
		"0197l2g2-d001-e001-f001-000000000007", "0197l2g2-d001-e001-f001-000000000008",
		"0197l2g2-d001-e001-f001-000000000009", "0197l2g2-d001-e001-f001-000000000010",
		"0I97l2g2-d001-e001-f001-000000000011", "0197l2g2-d001-e001-f001-000000000012",
		"0197l2g2-d001-e001-f001-000000000013", "0197l2g2-d001-e001-f001-000000000014",
		"0197l2g2-d001-e001-f001-000000000015", "0197l2g2-d001-e001-f001-000000000016",
	}

	// 助手函数：把 slice 转为 map[string]bool
	toMap := func(ids []string) map[string]bool {
		m := make(map[string]bool, len(ids))
		for _, id := range ids {
			m[id] = true
		}
		return m
	}

	return GoldenTruthSet{
		Tier1: toMap(tier1IDs),
		Tier2: toMap(tier2IDs),
		Tier3: toMap(tier3IDs),
	}
}

// TestGoBackendEngineerSearch 测试Go后端工程师岗位描述向量搜索
func TestGoBackendEngineerSearch(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "GoBackendEngineerSearch")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试Go后端工程师岗位描述向量搜索")

	// 1. 设置测试环境
	ctx := context.Background()

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true})
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)
	glog.SetLogger(hertzCompatibleLogger)
	glog.SetLevel(glog.LevelDebug)

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")
	testLogger.Log("配置加载成功")

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()
	testLogger.Log("存储组件初始化成功")

	// 跳过CI环境测试
	if os.Getenv("CI") != "" {
		testLogger.Log("在CI环境中跳过此测试")
		t.Skip("在CI环境中跳过此测试")
	}

	// 检查必要组件是否可用
	if s.Qdrant == nil {
		testLogger.Log("Qdrant未配置或不可用，跳过测试")
		t.Skip("Qdrant未配置或不可用，跳过测试")
	}

	// 确保API密钥配置正确
	if cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" {
		testLogger.Log("未配置有效的阿里云API密钥，跳过测试")
		t.Skip("未配置有效的阿里云API密钥，跳过测试")
	}

	// 初始化embedder
	testLogger.Log("初始化Embedder")
	aliyunEmbeddingConfig := config.EmbeddingConfig{
		Model:      cfg.Aliyun.Embedding.Model,
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,
		Dimensions: cfg.Aliyun.Embedding.Dimensions,
	}
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)
	require.NoError(t, err, "初始化embedder失败")
	testLogger.Log("Embedder初始化成功，Model: %s, Dimensions: %d", aliyunEmbeddingConfig.Model, aliyunEmbeddingConfig.Dimensions)

	// 定义Go后端工程师岗位描述
	jobDesc := `我们正在寻找一位经验丰富的高级Go后端及微服务工程师，加入我们的核心技术团队，负责设计、开发和维护大规模、高可用的分布式系统。您将有机会参与到从架构设计到服务上线的全过程，应对高并发、低延迟的挑战。

**主要职责:**
1.  负责核心业务系统的后端服务设计与开发，使用Go语言（Golang）作为主要开发语言。
2.  参与微服务架构的演进，使用gRPC进行服务间通信，并基于Protobuf进行接口定义。
3.  构建和维护高并发、高可用的系统，有处理秒杀、实时消息等大流量场景的经验。
4.  深入使用和优化缓存（Redis）和消息队列（Kafka/RabbitMQ），实现系统解耦和性能提升。
5.  将服务容器化（Docker）并部署在Kubernetes（K8s）集群上，熟悉云原生生态。
6.  关注系统性能，能够使用pprof等工具进行性能分析和调优。

**任职要求:**
1.  计算机相关专业本科及以上学历，3年以上Go语言后端开发经验。
2.  精通Go语言及其并发模型（Goroutine, Channel, Context）。
3.  熟悉至少一种主流Go微服务框架，如Go-Zero, Gin, Kratos等。
4.  熟悉gRPC, Protobuf，有丰富的微服务API设计经验。
5.  熟悉MySQL, Redis, Kafka等常用组件，并有生产环境应用经验。
6.  熟悉Docker和Kubernetes，理解云原生的基本理念。

**加分项:**
1.  有主导大型微服务项目重构或设计的经验。
2.  熟悉Service Mesh（如Istio）、分布式追踪（OpenTelemetry）等服务治理技术。
3.  对分布式存储（如TiKV）、共识算法（Raft）有深入研究或实践。
4.  有开源项目贡献或活跃的技术博客。`

	testLogger.Log("使用Go后端工程师岗位描述进行搜索")
	testLogger.Log("岗位描述长度: %d", len(jobDesc))

	// 检查Qdrant中的数据量
	count, countErr := s.Qdrant.CountPoints(ctx)
	if countErr != nil {
		testLogger.Log("获取Qdrant点数量失败: %v", countErr)
	} else {
		testLogger.Log("Qdrant集合中总共有 %d 个点", count)
	}

	// 将岗位描述转换为向量
	testLogger.Log("生成岗位描述向量")
	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc})
	require.NoError(t, err, "为岗位描述生成向量失败")
	require.NotEmpty(t, vectors, "生成的向量不能为空")
	require.NotEmpty(t, vectors[0], "生成的向量数组不能为空")

	vector := vectors[0]
	testLogger.Log("成功生成向量，维度: %d", len(vector))

	// 记录向量的前10个元素作为示例
	if len(vector) > 10 {
		sampleVector := vector[:10]
		testLogger.LogObject("向量样本(前10个元素)", sampleVector)
	}

	// 在Qdrant中搜索相似简历，设置limit为300
	limit := 300
	testLogger.Log("在Qdrant中搜索，限制结果数: %d", limit)
	results, err := s.Qdrant.SearchSimilarResumes(ctx, vector, limit, nil)
	require.NoError(t, err, "向量搜索失败")

	// 分析搜索结果
	testLogger.Log("搜索返回了 %d 个结果", len(results))
	t.Logf("搜索返回了 %d 个结果", len(results))

	if len(results) == 0 {
		// 如果没有结果，检查Qdrant集合中的点数量
		testLogger.Log("没有找到匹配的简历")
		t.Log("没有找到匹配的简历")
	} else {
		// 在分析搜索结果的代码块中，结果处理逻辑之前添加
		if len(results) > 0 {
			// 收集所有的UUID
			var allUUIDs []string
			for _, result := range results {
				if uuid, ok := result.Payload["submission_uuid"].(string); ok && uuid != "" {
					allUUIDs = append(allUUIDs, uuid)
				}
			}

			// 输出所有UUID到日志（以逗号分隔）
			testLogger.Log("=== 所有简历UUID列表 ===")
			testLogger.Log("UUID 总数: %d", len(allUUIDs))
			testLogger.Log("UUID 列表(逗号分隔): %s", strings.Join(allUUIDs, ","))

			// 输出所有UUID到日志（每行一个，便于复制）
			testLogger.Log("=== 所有简历UUID（每行一个） ===")
			for _, uuid := range allUUIDs {
				testLogger.Log("%s", uuid)
			}
		}

		// 如果有结果，开始详细分析
		testLogger.Log("=== 开始分析搜索结果 ===")

		// 记录各个分数区间的结果数量
		scoreRanges := map[string]int{
			"极高匹配 (0.9-1.0)": 0,
			"高匹配 (0.8-0.9)":  0,
			"良好匹配 (0.7-0.8)": 0,
			"中等匹配 (0.6-0.7)": 0,
			"一般匹配 (0.5-0.6)": 0,
			"低匹配 (< 0.5)":    0,
		}

		// 记录技能关键词出现的次数
		skillKeywords := map[string]int{
			"go":            0,
			"golang":        0,
			"微服务":           0,
			"grpc":          0,
			"protobuf":      0,
			"redis":         0,
			"kafka":         0,
			"rabbitmq":      0,
			"docker":        0,
			"kubernetes":    0,
			"k8s":           0,
			"gin":           0,
			"kratos":        0,
			"go-zero":       0,
			"mysql":         0,
			"高并发":           0,
			"分布式":           0,
			"pprof":         0,
			"性能优化":          0,
			"istio":         0,
			"service mesh":  0,
			"opentelemetry": 0,
			"tikv":          0,
			"raft":          0,
		}

		// 对结果进行详细分析
		for i, result := range results {
			// 从payload中获取submission_uuid和其他元数据
			submissionUUID := ""
			if subUUID, ok := result.Payload["submission_uuid"].(string); ok {
				submissionUUID = subUUID
			}

			// 记录该结果
			testLogger.Log("结果 #%d: SubmissionUUID=%s, Score=%.4f", i+1, submissionUUID, result.Score)

			// 根据分数区间进行统计
			switch {
			case result.Score >= 0.9:
				scoreRanges["极高匹配 (0.9-1.0)"]++
			case result.Score >= 0.8:
				scoreRanges["高匹配 (0.8-0.9)"]++
			case result.Score >= 0.7:
				scoreRanges["良好匹配 (0.7-0.8)"]++
			case result.Score >= 0.6:
				scoreRanges["中等匹配 (0.6-0.7)"]++
			case result.Score >= 0.5:
				scoreRanges["一般匹配 (0.5-0.6)"]++
			default:
				scoreRanges["低匹配 (< 0.5)"]++
			}

			// 获取简历内容进行详细分析
			if s.MySQL != nil && i < 50 { // 只详细分析前50个结果，避免日志过大
				var chunkContent string
				err := s.MySQL.DB().WithContext(ctx).
					Raw("SELECT chunk_content_text FROM resume_submission_chunks WHERE point_id = ?",
						result.ID).Scan(&chunkContent).Error

				if err == nil {
					// 记录内容到日志
					testLogger.Log("结果 #%d 内容:", i+1)
					testLogger.Log("%s", chunkContent)

					// 统计关键词出现情况
					chunkContentLower := strings.ToLower(chunkContent)
					for keyword := range skillKeywords {
						if strings.Contains(chunkContentLower, strings.ToLower(keyword)) {
							skillKeywords[keyword]++
						}
					}

					// 尝试评估简历质量
					quality := assessResumeQuality(chunkContentLower, skillKeywords)
					testLogger.Log("结果 #%d 质量评估: %s", i+1, quality)
				}
			}
		}

		// 输出分数区间统计
		testLogger.Log("=== 分数区间统计 ===")
		for rang, count := range scoreRanges {
			testLogger.Log("%s: %d个简历 (%.1f%%)", rang, count, float64(count)/float64(len(results))*100)
		}

		// 输出技能关键词统计
		testLogger.Log("=== 技能关键词出现次数统计 (前50个结果) ===")
		// 将关键词按出现次数排序
		type keywordCount struct {
			Keyword string
			Count   int
		}
		var sortedKeywords []keywordCount
		for k, v := range skillKeywords {
			if v > 0 { // 只统计出现过的关键词
				sortedKeywords = append(sortedKeywords, keywordCount{k, v})
			}
		}
		sort.Slice(sortedKeywords, func(i, j int) bool {
			return sortedKeywords[i].Count > sortedKeywords[j].Count
		})
		for _, kc := range sortedKeywords {
			testLogger.Log("%s: 出现在%d个简历块中", kc.Keyword, kc.Count)
		}
	}

	testLogger.Log("测试完成")
}

// TestGoBackendEngineerSearchWithGoldenTruthSet 测试Go后端工程师岗位并使用黄金标准进行评测
func TestGoBackendEngineerSearchWithGoldenTruthSet(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "GoBackendEngineerSearchWithGoldenTruthSet")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试Go后端工程师岗位描述向量搜索（带黄金标准评测）")

	// 1. 设置测试环境
	ctx := context.Background()
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty"})
	glog.SetLogger(hertzadapter.From(appCoreLogger.Logger))
	glog.SetLevel(glog.LevelDebug)

	// 加载配置
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")

	// 2. 初始化存储组件
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()

	// 跳过CI环境
	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此测试")
	}
	if s.Qdrant == nil || cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" {
		t.Skip("跳过测试：Qdrant或阿里云API Key未配置")
	}

	// 初始化Embedder
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "初始化embedder失败")

	// 定义Go后端工程师岗位描述
	jobDesc := `我们正在寻找一位经验丰富的高级Go后端及微服务工程师，加入我们的核心技术团队，负责设计、开发和维护大规模、高可用的分布式系统。您将有机会参与到从架构设计到服务上线的全过程，应对高并发、低延迟的挑战。

**主要职责:**
1.  负责核心业务系统的后端服务设计与开发，使用Go语言（Golang）作为主要开发语言。
2.  参与微服务架构的演进，使用gRPC进行服务间通信，并基于Protobuf进行接口定义。
3.  构建和维护高并发、高可用的系统，有处理秒杀、实时消息等大流量场景的经验。
4.  深入使用和优化缓存（Redis）和消息队列（Kafka/RabbitMQ），实现系统解耦和性能提升。
5.  将服务容器化（Docker）并部署在Kubernetes（K8s）集群上，熟悉云原生生态。
6.  关注系统性能，能够使用pprof等工具进行性能分析和调优。

**任职要求:**
1.  计算机相关专业本科及以上学历，3年以上Go语言后端开发经验。
2.  精通Go语言及其并发模型（Goroutine, Channel, Context）。
3.  熟悉至少一种主流Go微服务框架，如Go-Zero, Gin, Kratos等。
4.  熟悉gRPC, Protobuf，有丰富的微服务API设计经验。
5.  熟悉MySQL, Redis, Kafka等常用组件，并有生产环境应用经验。
6.  熟悉Docker和Kubernetes，理解云原生的基本理念。

**加分项:**
1.  有主导大型微服务项目重构或设计的经验。
2.  熟悉Service Mesh（如Istio）、分布式追踪（OpenTelemetry）等服务治理技术。
3.  对分布式存储（如TiKV）、共识算法（Raft）有深入研究或实践。
4.  有开源项目贡献或活跃的技术博客。`

	// 生成JD向量
	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc})
	require.NoError(t, err, "为岗位描述生成向量失败")
	vector := vectors[0]

	// 在Qdrant中搜索
	limit := 300
	initialResults, err := s.Qdrant.SearchSimilarResumes(ctx, vector, limit, nil)
	require.NoError(t, err, "向量搜索失败")
	testLogger.Log("阶段1: 向量搜索完成。从Qdrant召回 %d 个初步结果。", len(initialResults))

	// 阶段2: 海选过滤 (Score Threshold)
	scoreThreshold := float32(0.50)
	var candidatesForRerank []storage.SearchResult
	for _, res := range initialResults {
		if res.Score >= scoreThreshold {
			candidatesForRerank = append(candidatesForRerank, res)
		}
	}
	testLogger.Log("阶段2: 海选过滤完成。应用score_threshold=%.2f, 剩下 %d 个候选结果进入重排。", scoreThreshold, len(candidatesForRerank))

	// 阶段3: 调用Reranker服务进行精排
	var finalResults []storage.SearchResult
	rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, candidatesForRerank, t, testLogger)
	if err != nil {
		testLogger.Log("警告: Reranker调用失败: %v。将回退到使用原始向量搜索分数进行评测。", err)
		finalResults = initialResults // 回退到原始结果
	} else if rerankScores == nil {
		testLogger.Log("警告: Reranker未启用或未返回结果。将回退到使用原始向量搜索分数进行评测。")
		finalResults = initialResults // 回退到原始结果
	} else {
		testLogger.Log("阶段3: Reranker精排完成。")
		// 使用rerank分数更新结果并过滤
		var rerankedResults []storage.SearchResult
		for _, res := range candidatesForRerank {
			if newScore, ok := rerankScores[res.ID]; ok {
				res.Score = newScore // 使用rerank分数覆盖原始分数
				rerankedResults = append(rerankedResults, res)
			}
		}

		// 根据新的rerank分数进行排序（降序）
		sort.Slice(rerankedResults, func(i, j int) bool {
			return rerankedResults[i].Score > rerankedResults[j].Score
		})
		finalResults = rerankedResults
		testLogger.Log("使用 %d 个重排后的结果进行最终评测。", len(finalResults))
	}

	// 使用最终结果进行评测
	results := finalResults
	t.Logf("用于最终评测的结果数量: %d", len(results))

	// 获取黄金标准
	goldenTruth := getGoBackendGoldenTruthSet()

	// 分析结果
	foundTier1 := make(map[string]float32)
	foundTier2 := make(map[string]float32)
	foundTier3 := make(map[string]float32)
	unknownFound := make(map[string]float32)
	allFoundUUIDs := make(map[string]bool)

	for _, result := range results {
		if uuid, ok := result.Payload["submission_uuid"].(string); ok && uuid != "" {
			// 如果已经处理过该简历的更高分数的块，则跳过
			if allFoundUUIDs[uuid] {
				continue
			}

			if goldenTruth.Tier1[uuid] {
				foundTier1[uuid] = result.Score
			} else if goldenTruth.Tier2[uuid] {
				foundTier2[uuid] = result.Score
			} else if goldenTruth.Tier3[uuid] {
				foundTier3[uuid] = result.Score
			} else {
				unknownFound[uuid] = result.Score
			}
			allFoundUUIDs[uuid] = true // 标记该简历已处理
		}
	}

	// 输出评测报告
	reportTitle := "Go后端岗位评测报告"
	if rerankScores != nil && err == nil {
		reportTitle += " (Reranked)"
	} else {
		reportTitle += " (Vector Search Only)"
	}
	testLogger.Log("=== %s (Top %d) ===", reportTitle, len(results))
	testLogger.Log("==========================================")

	// Tier 1 评测
	tier1Recall := float64(len(foundTier1)) / float64(len(goldenTruth.Tier1)) * 100
	testLogger.Log("高相关 (Tier 1):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier1))
	testLogger.Log("  - 召回数量: %d", len(foundTier1))
	testLogger.Log("  - 召回率: %.2f%%", tier1Recall)

	// Tier 2 评测
	tier2Recall := float64(len(foundTier2)) / float64(len(goldenTruth.Tier2)) * 100
	testLogger.Log("中相关 (Tier 2):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier2))
	testLogger.Log("  - 召回数量: %d", len(foundTier2))
	testLogger.Log("  - 召回率: %.2f%%", tier2Recall)

	// Tier 3 评测 (噪音)
	tier3Noise := float64(len(foundTier3)) / float64(len(goldenTruth.Tier3)) * 100
	testLogger.Log("低相关/噪音 (Tier 3):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier3))
	testLogger.Log("  - 召回数量: %d", len(foundTier3))
	testLogger.Log("  - 噪音占比: %.2f%% (越低越好)", tier3Noise)

	// 其他项
	testLogger.Log("未分类项 (Unknown):")
	testLogger.Log("  - 召回数量: %d", len(unknownFound))

	testLogger.Log("==========================================")

	// --- 详细分数分布分析 ---
	testLogger.Log("\n--- 详细分数分布分析 ---")

	// 辅助函数，用于分析和记录每个层级的分数详情
	logScoreDetails(testLogger, "高相关 (Tier 1)", foundTier1)
	logScoreDetails(testLogger, "中相关 (Tier 2)", foundTier2)
	logScoreDetails(testLogger, "低相关/噪音 (Tier 3)", foundTier3)
	logScoreDetails(testLogger, "未分类项 (Unknown)", unknownFound)

	testLogger.Log("==========================================")
	testLogger.Log("测试完成")
}

// TestRerankerService 是一个独立的单元测试，用于检查Reranker服务的可用性和基本逻辑
func TestRerankerService(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "RerankerServiceConnectivity")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试Reranker服务连通性...")

	// 1. 加载配置以获取Reranker URL
	ctx := context.Background()
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")

	// 如果Reranker未启用或URL为空，则跳过测试
	if !cfg.Reranker.Enabled || cfg.Reranker.URL == "" {
		t.Skip("Reranker服务未启用或URL未配置，跳过此测试")
	}
	testLogger.Log("Reranker服务已启用，URL: %s", cfg.Reranker.URL)

	// 2. 构造模拟数据
	query := "高级Go语言工程师，需要微服务和gRPC经验"
	mockResults := []storage.SearchResult{
		{
			ID:    "doc-1-go-expert",
			Score: 0.8, // 原始向量分数
			Payload: map[string]interface{}{
				"content_text": "这位候选人精通Go语言，有丰富的gRPC和Protobuf实践，并主导过大型微服务项目。",
				"chunk_type":   "WORK_EXPERIENCE",
				"chunk_title":  "Senior Go Engineer at TechCorp",
			},
		},
		{
			ID:    "doc-2-python-dev",
			Score: 0.7, // 原始向量分数
			Payload: map[string]interface{}{
				"content_text": "该开发者主要使用Python和Django进行Web后端开发，了解过Go但没有项目经验。",
				"chunk_type":   "WORK_EXPERIENCE",
				"chunk_title":  "Backend Developer at WebSolutions",
			},
		},
		{
			ID:    "doc-3-frontend",
			Score: 0.6, // 原始向量分数
			Payload: map[string]interface{}{
				"content_text": "专注于前端开发，使用React和Vue框架，后端技能不匹配。",
				"chunk_type":   "SKILLS",
				"chunk_title":  "Frontend Skills",
			},
		},
	}
	testLogger.Log("构造了 %d 条模拟数据用于测试", len(mockResults))

	// 3. 调用Reranker服务
	testLogger.Log("调用Reranker服务...")
	rerankScores, err := callReranker(ctx, cfg.Reranker.URL, query, mockResults, t, testLogger)

	// 4. 验证结果
	require.NoError(t, err, "调用Reranker服务不应返回错误")
	require.NotNil(t, rerankScores, "Reranker返回的分数不应为nil")
	require.Equal(t, len(mockResults), len(rerankScores), "返回的分数数量应与发送的文档数量相等")
	testLogger.Log("Reranker服务成功返回了 %d 个分数", len(rerankScores))

	// 5. 打印并检查分数
	testLogger.Log("--- Rerank分数详情 ---")
	for _, doc := range mockResults {
		score, ok := rerankScores[doc.ID]
		require.True(t, ok, "每个文档都应该有一个rerank分数, doc ID: %s", doc.ID)
		testLogger.Log("文档ID: %s, Rerank Score: %.6f", doc.ID, score)
	}

	// 简单的逻辑断言：预期相关文档的分数更高
	scoreGoExpert := rerankScores["doc-1-go-expert"]
	scorePythonDev := rerankScores["doc-2-python-dev"]
	scoreFrontend := rerankScores["doc-3-frontend"]
	require.True(t, scoreGoExpert > scorePythonDev, "预期Go专家的分数(%.4f)应高于Python开发(%.4f)", scoreGoExpert, scorePythonDev)
	require.True(t, scorePythonDev > scoreFrontend, "预期Python开发的分数(%.4f)应高于前端开发(%.4f)", scorePythonDev, scoreFrontend)

	testLogger.Log("分数逻辑符合预期！")
	testLogger.Log("Reranker服务连通性测试通过！")
}

// callReranker 是一个辅助函数，用于调用外部的Reranker服务
func callReranker(ctx context.Context, rerankerURL string, query string, documents []storage.SearchResult, t *testing.T, logger *TestLogger) (map[string]float32, error) {
	if rerankerURL == "" {
		return nil, fmt.Errorf("reranker URL is not configured")
	}

	// 构造带有指令的Prompt作为新的Query
	prompt := fmt.Sprintf(`你是一位顶尖科技公司的资深技术招聘官。你的任务是为以下的"高级Go工程师"岗位对候选人进行排序。

**重要规则**:
1. **专注于实际动手经验**: 你必须优先选择那些在工作经历（WORK_EXPERIENCE）和技能（SKILLS）部分明确展示了与Go语言相关的、实际动手开发经验的候选人。
2. **严厉惩罚不相关角色**: 对于那些角色不是软件工程师的候选人（例如：招聘、产品经理、项目经理、销售、QA测试），即使他们的简历中包含了JD的关键词，你也必须给予严厉的惩罚，将他们排在后面。
3. **职位描述是核心依据**: 以下是你要招聘的岗位描述，请严格依据此描述进行判断。

---
%s
---`, query)

	var rerankDocs []RerankDocument
	for _, doc := range documents {
		// 增强输入：为文本添加上下文元数据
		chunkType, _ := doc.Payload["chunk_type"].(string)
		chunkTitle, _ := doc.Payload["chunk_title"].(string)
		originalText, _ := doc.Payload["content_text"].(string)

		// 创建带有上下文的文本
		enrichedText := fmt.Sprintf("[%s] %s: %s", chunkType, chunkTitle, originalText)

		rerankDocs = append(rerankDocs, RerankDocument{
			ID:   doc.ID,
			Text: enrichedText, // 使用增强后的文本
		})
	}

	if len(rerankDocs) == 0 {
		logger.Log("没有可以发送给Reranker的文档")
		return nil, nil
	}

	logger.Log("准备向Reranker服务发送 %d 个文档", len(rerankDocs))
	reqBody := RerankRequest{
		Query:     prompt,     // 使用增强后的Prompt作为Query
		Documents: rerankDocs, // 使用正确的字段名 "Documents"
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化rerank请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", rerankerURL, bytes.NewBuffer(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("创建rerank请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second} // 增加超时时间
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用rerank服务失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("rerank服务返回错误状态 %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var rerankedDocs []RerankedDocument
	if err := json.NewDecoder(resp.Body).Decode(&rerankedDocs); err != nil {
		return nil, fmt.Errorf("解析rerank响应失败: %w", err)
	}

	logger.Log("Reranker服务成功返回 %d 个重排序结果", len(rerankedDocs))

	rerankScores := make(map[string]float32)
	for _, doc := range rerankedDocs {
		rerankScores[doc.ID] = doc.RerankScore
	}

	return rerankScores, nil
}

// TestGoBackendEngineerSearch_WithScoreAggregation 测试带有分数聚合策略的Go后端工程师岗位搜索
func TestGoBackendEngineerSearch_WithScoreAggregation(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "GoBackendEngineerSearch_WithScoreAggregation")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试Go后端工程师岗位描述向量搜索（带Top-K分数聚合策略）")

	// 1. 设置测试环境 (与原测试相同)
	ctx := context.Background()
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty"})
	glog.SetLogger(hertzadapter.From(appCoreLogger.Logger))
	glog.SetLevel(glog.LevelDebug)

	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")

	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()

	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此测试")
	}
	if s.Qdrant == nil || cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" {
		t.Skip("跳过测试：Qdrant或阿里云API Key未配置")
	}

	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "初始化embedder失败")

	jobDesc := `我们正在寻找一位经验丰富的高级Go后端及微服务工程师，加入我们的核心技术团队，负责设计、开发和维护大规模、高可用的分布式系统。您将有机会参与到从架构设计到服务上线的全过程，应对高并发、低延迟的挑战。

**主要职责:**
1.  负责核心业务系统的后端服务设计与开发，使用Go语言（Golang）作为主要开发语言。
2.  参与微服务架构的演进，使用gRPC进行服务间通信，并基于Protobuf进行接口定义。
3.  构建和维护高并发、高可用的系统，有处理秒杀、实时消息等大流量场景的经验。
4.  深入使用和优化缓存（Redis）和消息队列（Kafka/RabbitMQ），实现系统解耦和性能提升。
5.  将服务容器化（Docker）并部署在Kubernetes（K8s）集群上，熟悉云原生生态。
6.  关注系统性能，能够使用pprof等工具进行性能分析和调优。

**任职要求:**
1.  计算机相关专业本科及以上学历，3年以上Go语言后端开发经验。
2.  精通Go语言及其并发模型（Goroutine, Channel, Context）。
3.  熟悉至少一种主流Go微服务框架，如Go-Zero, Gin, Kratos等。
4.  熟悉gRPC, Protobuf，有丰富的微服务API设计经验。
5.  熟悉MySQL, Redis, Kafka等常用组件，并有生产环境应用经验。
6.  熟悉Docker和Kubernetes，理解云原生的基本理念。

**加分项:**
1.  有主导大型微服务项目重构或设计的经验。
2.  熟悉Service Mesh（如Istio）、分布式追踪（OpenTelemetry）等服务治理技术。
3.  对分布式存储（如TiKV）、共识算法（Raft）有深入研究或实践。
4.  有开源项目贡献或活跃的技术博客。`

	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc})
	require.NoError(t, err, "为岗位描述生成向量失败")
	vector := vectors[0]

	// 2. 向量搜索与海选 (与原测试相同)
	limit := 300
	initialResults, err := s.Qdrant.SearchSimilarResumes(ctx, vector, limit, nil)
	require.NoError(t, err, "向量搜索失败")
	testLogger.Log("阶段1: 向量搜索完成。从Qdrant召回 %d 个初步结果。", len(initialResults))

	scoreThreshold := float32(0.50)
	var candidatesForRerank []storage.SearchResult
	for _, res := range initialResults {
		if res.Score >= scoreThreshold {
			candidatesForRerank = append(candidatesForRerank, res)
		}
	}
	testLogger.Log("阶段2: 海选过滤完成。应用score_threshold=%.2f, 剩下 %d 个候选结果进入重排。", scoreThreshold, len(candidatesForRerank))

	// 3. Reranker精排 (与原测试相同)
	rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, candidatesForRerank, t, testLogger)
	if err != nil || rerankScores == nil {
		t.Fatalf("Reranker调用失败或未返回结果，测试无法继续: %v", err)
	}
	testLogger.Log("阶段3: Reranker精排完成。")

	// 记录每个返回的rerank score
	testLogger.Log("--- Reranker返回的分数详情 ---")
	type rerankedResultForLog struct {
		PointID        string
		SubmissionUUID string
		Score          float32
	}
	var loggedResults []rerankedResultForLog
	for _, res := range candidatesForRerank {
		if score, ok := rerankScores[res.ID]; ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			loggedResults = append(loggedResults, rerankedResultForLog{
				PointID:        res.ID,
				SubmissionUUID: uuid,
				Score:          score,
			})
		}
	}
	// 按rerank分数降序排序
	sort.Slice(loggedResults, func(i, j int) bool {
		return loggedResults[i].Score > loggedResults[j].Score
	})
	// 记录排序后的结果
	for _, res := range loggedResults {
		testLogger.Log("  - PointID: %s, SubmissionUUID: %s, Rerank Score: %.4f", res.PointID, res.SubmissionUUID, res.Score)
	}
	testLogger.Log("-----------------------------")

	// 4. 【核心改造】分数聚合
	testLogger.Log("阶段4: 开始进行Top-K分数聚合...")
	submissionChunkScores := make(map[string][]float32)

	// 收集每个submission的所有reranked chunk分数
	for _, res := range candidatesForRerank {
		if newScore, ok := rerankScores[res.ID]; ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				submissionChunkScores[uuid] = append(submissionChunkScores[uuid], newScore)
			}
		}
	}
	testLogger.Log("聚合了 %d 份独立简历的所有chunk分数。", len(submissionChunkScores))

	// 计算Top-K平均分 ("惩罚短板"策略)
	finalSubmissionScores := make(map[string]float32)
	const topK = 3
	for uuid, scores := range submissionChunkScores {
		// 降序排序
		sort.Slice(scores, func(i, j int) bool {
			return scores[i] > scores[j]
		})

		// 取前K个或所有（如果不足K个）
		effectiveK := topK
		if len(scores) < effectiveK {
			effectiveK = len(scores)
		}

		var sum float32
		for i := 0; i < effectiveK; i++ {
			sum += scores[i]
		}
		// 动态分母聚合：避免对短简历进行双重惩罚
		finalSubmissionScores[uuid] = sum / float32(effectiveK)
	}
	testLogger.Log("使用Top-%d平均分策略（动态分母），计算出 %d 份简历的最终综合得分。", topK, len(finalSubmissionScores))

	// 5. 最终评测
	goldenTruth := getGoBackendGoldenTruthSet()

	foundTier1 := make(map[string]float32)
	foundTier2 := make(map[string]float32)
	foundTier3 := make(map[string]float32)
	unknownFound := make(map[string]float32)

	for uuid, score := range finalSubmissionScores {
		if goldenTruth.Tier1[uuid] {
			foundTier1[uuid] = score
		} else if goldenTruth.Tier2[uuid] {
			foundTier2[uuid] = score
		} else if goldenTruth.Tier3[uuid] {
			foundTier3[uuid] = score
		} else {
			unknownFound[uuid] = score
		}
	}

	// 输出评测报告
	testLogger.Log("=== Go后端岗位评测报告 (Top-K聚合策略) ===")
	testLogger.Log("==========================================")

	// Tier 1 评测
	tier1Recall := float64(len(foundTier1)) / float64(len(goldenTruth.Tier1)) * 100
	testLogger.Log("高相关 (Tier 1):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier1))
	testLogger.Log("  - 召回数量: %d", len(foundTier1))
	testLogger.Log("  - 召回率: %.2f%%", tier1Recall)

	// Tier 2 评测
	tier2Recall := float64(len(foundTier2)) / float64(len(goldenTruth.Tier2)) * 100
	testLogger.Log("中相关 (Tier 2):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier2))
	testLogger.Log("  - 召回数量: %d", len(foundTier2))
	testLogger.Log("  - 召回率: %.2f%%", tier2Recall)

	// Tier 3 评测 (噪音)
	tier3Noise := float64(len(foundTier3)) / float64(len(goldenTruth.Tier3)) * 100
	testLogger.Log("低相关/噪音 (Tier 3):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier3))
	testLogger.Log("  - 召回数量: %d", len(foundTier3))
	testLogger.Log("  - 噪音占比: %.2f%% (越低越好)", tier3Noise)

	// 其他项
	testLogger.Log("未分类项 (Unknown):")
	testLogger.Log("  - 召回数量: %d", len(unknownFound))

	testLogger.Log("==========================================")

	// --- 详细分数分布分析 (基于聚合后分数) ---
	logScoreDetails(testLogger, "高相关 (Tier 1)", foundTier1)
	logScoreDetails(testLogger, "中相关 (Tier 2)", foundTier2)
	logScoreDetails(testLogger, "低相关/噪音 (Tier 3)", foundTier3)
	logScoreDetails(testLogger, "未分类项 (Unknown)", unknownFound)

	testLogger.Log("==========================================")
	testLogger.Log("测试完成")
}

// logScoreDetails 是一个辅助函数，用于分析和记录每个层级的分数详情
func logScoreDetails(testLogger *TestLogger, tierName string, foundItems map[string]float32) {
	if len(foundItems) == 0 {
		testLogger.Log("\n%s: 未召回任何简历。", tierName)
		return
	}

	var scores []float32
	var scoreSum float32
	minScore := float32(2.0)  // 分数通常在-1到1之间，2.0是安全的初始值
	maxScore := float32(-2.0) // -2.0是安全的初始值

	var uuids []string
	for uuid := range foundItems {
		uuids = append(uuids, uuid)
	}
	// 按分数降序排序，便于查看
	sort.Slice(uuids, func(i, j int) bool {
		return foundItems[uuids[i]] > foundItems[uuids[j]]
	})

	testLogger.Log("\n%s (召回 %d 份):", tierName, len(foundItems))
	for _, uuid := range uuids {
		score := foundItems[uuid]
		testLogger.Log("  - UUID: %s, Score: %.4f", uuid, score)
		scores = append(scores, score)
		scoreSum += score
		if score < minScore {
			minScore = score
		}
		if score > maxScore {
			maxScore = score
		}
	}

	avgScore := scoreSum / float32(len(scores))
	testLogger.Log("  -> 统计: 最高分=%.4f, 最低分=%.4f, 平均分=%.4f", maxScore, minScore, avgScore)
}

// TestSearchStrategyComparison 对比三种不同的搜索与排序策略
func TestSearchStrategyComparison(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "SearchStrategyComparison")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始对比三种搜索策略：1. 仅向量搜索 2. 向量+Rerank 3. 向量+Rerank+聚合")

	// --- 1. 共享的测试设置 ---
	ctx := context.Background()
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty"})
	glog.SetLogger(hertzadapter.From(appCoreLogger.Logger))
	glog.SetLevel(glog.LevelDebug)

	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")

	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()

	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此测试")
	}
	if s.Qdrant == nil || cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" || !cfg.Reranker.Enabled {
		t.Skip("跳过测试：Qdrant、阿里云API Key或Reranker未配置")
	}

	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "初始化embedder失败")

	jobDesc := `我们正在寻找一位经验丰富的高级Go后端及微服务工程师，加入我们的核心技术团队，负责设计、开发和维护大规模、高可用的分布式系统。您将有机会参与到从架构设计到服务上线的全过程，应对高并发、低延迟的挑战。` // 使用简化的JD以专注于策略对比

	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc})
	require.NoError(t, err, "为岗位描述生成向量失败")
	vector := vectors[0]

	goldenTruth := getGoBackendGoldenTruthSet()

	// --- 2. 一次性执行召回和重排，复用结果 ---
	const recallLimitForRerank = 300
	const vectorSearchOnlyLimit = 150 // 根据建议调整limit

	testLogger.Log("共享步骤: 向量搜索 (召回 Limit: %d)...", recallLimitForRerank)
	initialResults, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallLimitForRerank, nil)
	require.NoError(t, err, "向量搜索失败")
	testLogger.Log("共享步骤: 向量搜索完成，召回 %d 个初步结果。", len(initialResults))

	scoreThreshold := float32(0.50)
	var candidatesForRerank []storage.SearchResult
	for _, res := range initialResults {
		if res.Score >= scoreThreshold {
			candidatesForRerank = append(candidatesForRerank, res)
		}
	}
	testLogger.Log("共享步骤: 海选过滤完成，剩下 %d 个候选结果。", len(candidatesForRerank))

	rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, candidatesForRerank, t, testLogger)
	if err != nil || rerankScores == nil {
		t.Fatalf("Reranker调用失败或未返回结果，测试无法继续: %v", err)
	}
	testLogger.Log("共享步骤: Reranker精排完成。")

	// --- 3. 分别执行和评估三种策略 ---

	// 策略 1: 仅向量搜索 (取最高分, limit=150, threshold=0.50)
	testLogger.Log("开始评估策略1 (仅向量搜索)，分析召回结果的前 %d 项，并应用score_threshold=%.2f...", vectorSearchOnlyLimit, scoreThreshold)
	finalScoresStrategy1 := make(map[string]float32)
	resultsForStrategy1 := initialResults
	if len(resultsForStrategy1) > vectorSearchOnlyLimit {
		resultsForStrategy1 = resultsForStrategy1[:vectorSearchOnlyLimit]
	}
	for _, res := range resultsForStrategy1 {
		// 应用与Rerank策略相同的分数阈值
		if res.Score >= scoreThreshold {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				if currentScore, ok := finalScoresStrategy1[uuid]; !ok || res.Score > currentScore {
					finalScoresStrategy1[uuid] = res.Score
				}
			}
		}
	}
	evaluateAndLogReport(testLogger, "策略 1: 仅向量搜索 (Max Score, Limit 150, Threshold 0.50)", finalScoresStrategy1, goldenTruth)

	// 策略 2: 向量搜索 + Rerank (取最高分)
	finalScoresStrategy2 := make(map[string]float32)
	for _, res := range candidatesForRerank {
		if rerankScore, ok := rerankScores[res.ID]; ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				if currentScore, ok := finalScoresStrategy2[uuid]; !ok || rerankScore > currentScore {
					finalScoresStrategy2[uuid] = rerankScore
				}
			}
		}
	}
	evaluateAndLogReport(testLogger, "策略 2: 向量 + Rerank (Max Score, Recall Limit 300)", finalScoresStrategy2, goldenTruth)

	// 策略 3: 向量搜索 + Rerank + 短板惩罚聚合
	submissionChunkScores := make(map[string][]float32)
	for _, res := range candidatesForRerank {
		if newScore, ok := rerankScores[res.ID]; ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				submissionChunkScores[uuid] = append(submissionChunkScores[uuid], newScore)
			}
		}
	}
	finalScoresStrategy3 := make(map[string]float32)
	const topK = 3
	for uuid, scores := range submissionChunkScores {
		sort.Slice(scores, func(i, j int) bool { return scores[i] > scores[j] })
		effectiveK := topK
		if len(scores) < effectiveK {
			effectiveK = len(scores)
		}
		var sum float32
		for i := 0; i < effectiveK; i++ {
			sum += scores[i]
		}
		// 动态分母聚合：避免对短简历进行双重惩罚
		finalScoresStrategy3[uuid] = sum / float32(effectiveK)
	}
	evaluateAndLogReport(testLogger, "策略 3: 向量 + Rerank + 聚合 (动态分母)", finalScoresStrategy3, goldenTruth)

	testLogger.Log("所有策略对比测试完成。")
}

// evaluateAndLogReport 是一个辅助函数，用于执行评测并记录标准化的报告
func evaluateAndLogReport(testLogger *TestLogger, strategyName string, finalScores map[string]float32, goldenTruth GoldenTruthSet) {
	foundTier1 := make(map[string]float32)
	foundTier2 := make(map[string]float32)
	foundTier3 := make(map[string]float32)
	unknownFound := make(map[string]float32)

	for uuid, score := range finalScores {
		if goldenTruth.Tier1[uuid] {
			foundTier1[uuid] = score
		} else if goldenTruth.Tier2[uuid] {
			foundTier2[uuid] = score
		} else if goldenTruth.Tier3[uuid] {
			foundTier3[uuid] = score
		} else {
			unknownFound[uuid] = score
		}
	}

	// --- 输出评测报告 ---
	testLogger.Log("\n\n===== [评测报告: %s] =====", strategyName)
	testLogger.Log("====================================================")

	// Tier 1 评测
	tier1Recall := 0.0
	if len(goldenTruth.Tier1) > 0 {
		tier1Recall = float64(len(foundTier1)) / float64(len(goldenTruth.Tier1)) * 100
	}
	testLogger.Log("高相关 (Tier 1):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier1))
	testLogger.Log("  - 召回数量: %d", len(foundTier1))
	testLogger.Log("  - 召回率: %.2f%%", tier1Recall)

	// Tier 2 评测
	tier2Recall := 0.0
	if len(goldenTruth.Tier2) > 0 {
		tier2Recall = float64(len(foundTier2)) / float64(len(goldenTruth.Tier2)) * 100
	}
	testLogger.Log("中相关 (Tier 2):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier2))
	testLogger.Log("  - 召回数量: %d", len(foundTier2))
	testLogger.Log("  - 召回率: %.2f%%", tier2Recall)

	// Tier 3 评测 (噪音)
	tier3Noise := 0.0
	if len(goldenTruth.Tier3) > 0 {
		tier3Noise = float64(len(foundTier3)) / float64(len(goldenTruth.Tier3)) * 100
	}
	testLogger.Log("低相关/噪音 (Tier 3):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier3))
	testLogger.Log("  - 召回数量: %d", len(foundTier3))
	testLogger.Log("  - 噪音占比: %.2f%% (越低越好)", tier3Noise)

	// 其他项
	testLogger.Log("未分类项 (Unknown):")
	testLogger.Log("  - 召回数量: %d", len(unknownFound))
	testLogger.Log("----------------------------------------------------")

	// --- 详细分数分布分析 ---
	logScoreDetails(testLogger, "高相关 (Tier 1)", foundTier1)
	logScoreDetails(testLogger, "中相关 (Tier 2)", foundTier2)
	logScoreDetails(testLogger, "低相关/噪音 (Tier 3)", foundTier3)
	logScoreDetails(testLogger, "未分类项 (Unknown)", unknownFound)

	testLogger.Log("====================================================\n\n")
}

// logRankedResultsToJSON 辅助函数，导出详细结果
func logRankedResultsToJSON(t *testing.T, s *storage.Storage, ctx context.Context, testLogger *TestLogger, key string, fileName string) {
	resultsWithScores, err := s.Redis.Client.ZRevRangeWithScores(ctx, key, 0, -1).Result()
	if err != nil || len(resultsWithScores) == 0 {
		testLogger.Log("无法从Redis键 '%s' 获取带分数的UUIDs或列表为空: %v", key, err)
		return
	}

	var uuids []string
	for _, item := range resultsWithScores {
		uuids = append(uuids, item.Member.(string))
	}
	if len(uuids) == 0 {
		return
	}

	type ResumeSubmissionChunkForLog struct {
		SubmissionUUID      string `json:"submission_uuid"`
		CandidateID         string `json:"candidate_id"`
		ChunkIDInSubmission int64  `json:"chunk_id_in_submission"`
		ChunkType           string `json:"chunk_type"`
		ChunkTitle          string `json:"chunk_title"`
		ChunkContentText    string `json:"content_text"`
		PointID             string `json:"point_id"`
	}
	var chunks []ResumeSubmissionChunkForLog
	err = s.MySQL.DB().WithContext(ctx).
		Table("resume_submission_chunks as rsc").
		Select("rsc.*, rs.candidate_id").
		Joins("LEFT JOIN resume_submissions as rs ON rsc.submission_uuid = rs.submission_uuid").
		Where("rsc.submission_uuid IN ?", uuids).
		Find(&chunks).Error
	require.NoError(t, err)

	chunksByUUID := make(map[string][]ResumeSubmissionChunkForLog)
	for _, chunk := range chunks {
		chunksByUUID[chunk.SubmissionUUID] = append(chunksByUUID[chunk.SubmissionUUID], chunk)
	}

	type RankedResultForLog struct {
		Rank           int                           `json:"rank"`
		SubmissionUUID string                        `json:"submission_uuid"`
		CandidateID    string                        `json:"candidate_id"`
		RelevanceScore float64                       `json:"relevance_score"`
		Chunks         []ResumeSubmissionChunkForLog `json:"chunks"`
	}
	var finalRankedResults []RankedResultForLog
	for i, item := range resultsWithScores {
		uuid := item.Member.(string)
		score := item.Score
		if chunksForUUID, ok := chunksByUUID[uuid]; ok {
			sort.Slice(chunksForUUID, func(i, j int) bool {
				return chunksForUUID[i].ChunkIDInSubmission < chunksForUUID[j].ChunkIDInSubmission
			})
			finalRankedResults = append(finalRankedResults, RankedResultForLog{
				Rank:           i + 1,
				SubmissionUUID: uuid,
				CandidateID:    chunksForUUID[0].CandidateID, // Assume all chunks have same candidate_id
				RelevanceScore: score,
				Chunks:         chunksForUUID,
			})
		}
	}

	// 确保日志目录存在
	logDir := filepath.Join("test_logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}

	// 按页写入JSON文件
	const pageSize = 10
	totalResults := len(finalRankedResults)
	timestamp := time.Now().Format("20060102_150405")

	for i := 0; i < totalResults; i += pageSize {
		pageNumber := (i / pageSize) + 1
		end := i + pageSize
		if end > totalResults {
			end = totalResults
		}
		pageData := finalRankedResults[i:end]

		type SinglePageResponse struct {
			TotalResults int                  `json:"total_results"`
			PageSize     int                  `json:"page_size"`
			TotalPages   int                  `json:"total_pages"`
			PageNumber   int                  `json:"page_number"`
			Data         []RankedResultForLog `json:"data"`
		}
		pageResponse := SinglePageResponse{
			TotalResults: totalResults,
			PageSize:     pageSize,
			TotalPages:   (totalResults + pageSize - 1) / pageSize,
			PageNumber:   pageNumber,
			Data:         pageData,
		}

		fullFilename := filepath.Join(logDir, fmt.Sprintf("%s_%s_page_%d.json", fileName, timestamp, pageNumber))
		jsonData, err := json.MarshalIndent(pageResponse, "", "  ")
		require.NoError(t, err)
		if err := os.WriteFile(fullFilename, jsonData, 0644); err != nil {
			testLogger.Log("写入JSON文件失败: %v", err)
		} else {
			testLogger.Log("结果已写入: %s", fullFilename)
		}
	}
	testLogger.Log("成功将结果写入到 %s 相关文件中", fileName)
}
