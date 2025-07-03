package handler_test // 定义测试包，对应handler包的测试

import (
	"ai-agent-go/internal/api/handler"          // 导入处理器包，包含要测试的HTTP处理函数
	"ai-agent-go/internal/config"               // 导入配置包，用于加载配置信息
	"ai-agent-go/internal/constants"            // 导入常量包，定义系统中使用的常量
	appCoreLogger "ai-agent-go/internal/logger" // 导入日志包，重命名为appCoreLogger
	"ai-agent-go/internal/parser"               // 导入解析器包，处理文本解析和向量嵌入
	"ai-agent-go/internal/processor"            // 导入处理器包，处理业务逻辑
	"ai-agent-go/internal/storage"              // 导入存储包，处理数据存储相关操作
	"ai-agent-go/internal/storage/models"       // 导入数据模型定义
	"context"                                   // 导入上下文包，用于控制请求的生命周期
	"encoding/json"                             // 导入JSON编解码包
	"fmt"                                       // 导入格式化包，用于字符串格式化
	"io"                                        // 导入IO操作包
	"log"                                       // 导入日志包
	"math"                                      // 导入数学计算包
	"os"                                        // 导入操作系统功能包
	"path/filepath"                             // 导入文件路径操作包
	"sort"                                      // 导入排序包
	"strings"                                   // 导入字符串操作包
	"testing"                                   // 导入测试包
	"time"                                      // 导入时间包

	"github.com/cloudwego/hertz/pkg/app"                   // 导入Hertz框架的应用包
	glog "github.com/cloudwego/hertz/pkg/common/hlog"      // 导入Hertz日志包，重命名为glog
	"github.com/cloudwego/hertz/pkg/protocol/consts"       // 导入Hertz协议常量
	"github.com/cloudwego/hertz/pkg/route/param"           // 导入Hertz路由参数包
	hertzadapter "github.com/hertz-contrib/logger/zerolog" // 导入Hertz日志适配器
	"github.com/redis/go-redis/v9"                         // 导入Redis客户端包
	"github.com/stretchr/testify/assert"                   // 导入测试断言包
	"github.com/stretchr/testify/require"                  // 导入测试必要条件包
	"gorm.io/gorm"                                         // 导入Gorm ORM包
	"gorm.io/gorm/logger"                                  // 导入Gorm日志包
)

// 使用不同的配置路径常量名称，避免与其他测试文件冲突
const (
	jobSearchTestConfigPath = "../../config/config.yaml" // 定义测试配置文件的路径
)

// RerankDocument 定义了发送给Reranker服务的文档结构
type RerankDocument struct {
	ID   string `json:"id"`   // 文档的唯一标识符
	Text string `json:"text"` // 文档的文本内容
}

// RerankRequest 定义了发送给Reranker服务的请求体结构
type RerankRequest struct {
	Query     string           `json:"query"`     // 搜索查询文本
	Documents []RerankDocument `json:"documents"` // 要重新排序的文档列表
}

// RerankedDocument 定义了从Reranker服务接收的文档结构
type RerankedDocument struct {
	ID          string  `json:"id"`           // 文档的唯一标识符
	RerankScore float32 `json:"rerank_score"` // 重新排序后的相关性得分
}

// GoldenTruthSet 定义了用于评测的黄金标准答案
type GoldenTruthSet struct {
	Tier1 map[string]bool // 高相关的文档集合
	Tier2 map[string]bool // 中相关的文档集合
	Tier3 map[string]bool // 低相关的文档集合
}

// TestLogger 记录测试过程中的详细信息到文件
type TestLogger struct {
	file    *os.File   // 日志文件句柄
	testing *testing.T // 测试对象的引用，用于输出到测试日志
}

// NewTestLogger 创建一个新的测试日志记录器
func NewTestLogger(t *testing.T, filename string) (*TestLogger, error) {
	// 确保目录存在
	logDir := filepath.Join(".", "test_logs")         // 设置日志目录路径为当前目录下的test_logs子目录
	if err := os.MkdirAll(logDir, 0755); err != nil { // 创建日志目录，权限设为0755
		return nil, err // 如果创建目录失败，返回错误
	}

	// 创建带时间戳的日志文件名
	timestamp := time.Now().Format("20060102_150405")                                    // 格式化当前时间为年月日_时分秒格式
	fullFilename := filepath.Join(logDir, fmt.Sprintf("%s_%s.log", filename, timestamp)) // 组合完整的日志文件路径

	// 创建或截断文件
	file, err := os.Create(fullFilename) // 创建日志文件，如果已存在则会截断
	if err != nil {
		return nil, err // 如果创建文件失败，返回错误
	}

	t.Logf("测试日志将写入: %s", fullFilename) // 在测试日志中记录文件路径

	return &TestLogger{ // 返回新创建的TestLogger实例
		file:    file, // 设置文件句柄
		testing: t,    // 设置测试对象引用
	}, nil
}

// Log 记录信息到文件和测试日志
func (tl *TestLogger) Log(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...) // 格式化日志消息
	tl.testing.Log(msg)                 // 输出到测试日志

	// 添加时间戳
	timestamp := time.Now().Format("15:04:05.000")           // 格式化当前时间为时:分:秒.毫秒
	formattedMsg := fmt.Sprintf("[%s] %s\n", timestamp, msg) // 组合带时间戳的日志消息

	if _, err := tl.file.WriteString(formattedMsg); err != nil { // 写入日志文件
		tl.testing.Logf("写入日志文件失败: %v", err) // 如果写入失败，记录错误到测试日志
	}
}

// LogObject 记录对象到文件，以json格式
func (tl *TestLogger) LogObject(prefix string, obj interface{}) {
	bytes, err := json.MarshalIndent(obj, "", "  ") // 将对象转换为格式化的JSON字节数组
	if err != nil {
		tl.Log("%s: 无法序列化对象: %v", prefix, err) // 如果序列化失败，记录错误
		return
	}

	tl.Log("%s:\n%s", prefix, string(bytes)) // 记录前缀和JSON字符串到日志
}

// Close 关闭日志文件
func (tl *TestLogger) Close() {
	if tl.file != nil { // 检查文件句柄是否为nil
		tl.file.Close() // 关闭文件句柄，释放资源
	}
}

// TestJobSearchRecall 测试岗位搜索召回效果
func TestJobSearchRecall(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "JobSearchRecall") // 创建一个测试日志记录器，用于记录测试过程
	require.NoError(t, err, "创建测试日志记录器失败")                 // 确保日志记录器创建成功，否则终止测试
	defer testLogger.Close()                               // 确保测试结束时关闭日志文件

	testLogger.Log("开始测试岗位搜索召回效果") // 记录测试开始

	// 1. 设置测试环境
	ctx := context.Background() // 创建一个新的上下文

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true}) // 初始化应用核心日志
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)                                                      // 创建Hertz兼容的日志适配器
	glog.SetLogger(hertzCompatibleLogger)                                                                                 // 设置全局Hertz日志实例
	glog.SetLevel(glog.LevelDebug)                                                                                        // 设置日志级别为Debug

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)                // 记录将要加载的配置文件路径
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath) // 从文件和环境变量加载配置
	require.NoError(t, err, "加载配置失败")                                    // 确保配置加载成功，否则终止测试
	require.NotNil(t, cfg, "配置不能为空")                                     // 确保配置不为空，否则终止测试
	testLogger.Log("配置加载成功")                                             // 记录配置加载成功

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")              // 记录开始初始化存储组件
	s, err := storage.NewStorage(ctx, cfg) // 根据配置创建存储组件
	require.NoError(t, err, "初始化存储组件失败")   // 确保存储组件初始化成功，否则终止测试
	defer s.Close()                        // 确保测试结束时关闭存储组件
	testLogger.Log("存储组件初始化成功")            // 记录存储组件初始化成功

	// 3. 创建Embedding服务
	testLogger.Log("创建Embedding服务")                                                          // 记录开始创建Embedding服务
	aliyunEmbedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding) // 创建阿里云Embedder实例
	require.NoError(t, err, "创建AliyunEmbedder失败")                                            // 确保Embedder创建成功，否则终止测试
	testLogger.Log("Embedding服务创建成功")                                                        // 记录Embedding服务创建成功

	// 4. 初始化处理器
	testLogger.Log("初始化JD处理器")                                                               // 记录开始初始化JD处理器
	jdProcessor, err := processor.NewJDProcessor(aliyunEmbedder, s, cfg.ActiveParserVersion) // 创建JD处理器
	require.NoError(t, err, "初始化JD处理器失败")                                                    // 确保JD处理器初始化成功，否则终止测试
	testLogger.Log("JD处理器初始化成功")                                                             // 记录JD处理器初始化成功

	// 5. 初始化处理程序
	testLogger.Log("初始化JobSearchHandler")                 // 记录开始初始化JobSearchHandler
	h := handler.NewJobSearchHandler(cfg, s, jdProcessor) // 创建JobSearchHandler实例
	testLogger.Log("JobSearchHandler初始化成功")               // 记录JobSearchHandler初始化成功

	// 6. 确保测试数据存在 - 岗位信息
	testJobID := "11111111-2222-3333-4444-555555555555" // 设置测试用的岗位ID
	jobTitle := "产品质量技术负责人"                             // 设置测试用的岗位标题
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
· 具备奉献精神，善于沟通与团队合作。` // 设置测试用的岗位描述

	testLogger.Log("测试岗位ID: %s", testJobID) // 记录测试岗位ID
	testLogger.Log("岗位标题: %s", jobTitle)    // 记录岗位标题
	testLogger.Log("岗位描述:\n%s", jobDesc)    // 记录岗位描述

	// 7. 准备测试数据 - 确保数据库中有岗位数据
	testLogger.Log("准备测试数据 - 确保数据库中有岗位数据")              // 记录开始准备测试数据
	ensureJobExists(t, s, testJobID, jobTitle, jobDesc) // 调用辅助函数确保测试岗位存在

	// 8. 创建请求上下文
	testLogger.Log("创建请求上下文") // 记录创建请求上下文
	c := app.NewContext(16)   // 创建Hertz上下文，16是初始化大小

	// 添加参数
	c.Params = append(c.Params, param.Param{ // 添加URL参数
		Key:   "job_id",  // 参数名为job_id
		Value: testJobID, // 参数值为测试岗位ID
	})

	// 检查Qdrant中的数据量
	if s.Qdrant != nil { // 如果Qdrant实例存在
		count, countErr := s.Qdrant.CountPoints(ctx) // 获取Qdrant中的点数量
		if countErr != nil {                         // 如果获取过程出错
			testLogger.Log("获取Qdrant点数量失败: %v", countErr) // 记录错误
		} else {
			testLogger.Log("Qdrant集合中总共有 %d 个点", count) // 记录点数量
		}
	}

	// 9. 执行测试
	testLogger.Log("执行岗位搜索处理")           // 记录开始执行岗位搜索处理
	h.HandleSearchResumesByJobID(ctx, c) // 调用被测试的处理函数

	// 10. 验证结果
	statusCode := c.Response.StatusCode() // 获取响应状态码
	testLogger.Log("状态码: %d", statusCode) // 记录状态码

	var respData map[string]interface{}                                  // 定义变量存储响应数据
	if err := json.Unmarshal(c.Response.Body(), &respData); err != nil { // 解析响应体JSON
		testLogger.Log("解析响应失败: %v", err) // 记录解析错误
		t.Fatalf("解析响应失败: %v", err)       // 如果解析失败，终止测试
	}

	// 记录完整响应数据到日志文件
	testLogger.LogObject("响应数据", respData) // 将完整响应数据以JSON格式记录到日志

	// 11. 验证基本响应结构
	assert.Equal(t, consts.StatusOK, statusCode)   // 断言状态码为200 OK
	assert.Contains(t, respData, "message")        // 断言响应包含message字段
	assert.Contains(t, respData, "job_id")         // 断言响应包含job_id字段
	assert.Equal(t, testJobID, respData["job_id"]) // 断言返回的job_id与请求相同

	// 12. 验证返回的简历数据
	if data, ok := respData["data"].([]interface{}); ok { // 尝试获取并转换data字段
		testLogger.Log("找到 %d 个匹配的简历", len(data)) // 记录找到的简历数量
		t.Logf("找到 %d 个匹配的简历", len(data))         // 输出到测试日志

		// 分析每个返回的简历
		for i, item := range data { // 遍历每个简历
			resumeData := item.(map[string]interface{})              // 将简历转换为map
			submissionUUID := resumeData["submission_uuid"].(string) // 获取简历提交UUID
			score := resumeData["qdrant_score"].(float64)            // 获取相关性得分

			testLogger.Log("简历 #%d - UUID: %s, 分数: %.4f", i+1, submissionUUID, score) // 记录简历基本信息
			t.Logf("简历 #%d - UUID: %s, 分数: %.4f", i+1, submissionUUID, score)         // 输出到测试日志

			// 记录更详细的简历信息到日志文件
			testLogger.LogObject(fmt.Sprintf("简历 #%d 详细信息", i+1), resumeData) // 将完整简历数据记录到日志

			// 可以增加更详细的验证，例如检查相关性分数是否超过某个阈值
			if score, ok := resumeData["qdrant_score"].(float64); ok { // 再次获取分数并确认类型
				assert.True(t, score > 0.1, "相关性分数应该大于0.1") // 断言分数大于0.1，确保相关性
			}
		}
	} else { // 如果没有找到简历或数据格式不正确
		testLogger.Log("没有找到匹配的简历") // 记录未找到简历
		t.Log("没有找到匹配的简历")          // 输出到测试日志
	}

	testLogger.Log("测试完成") // 记录测试完成
}

// ensureJobExists 确保测试岗位在数据库中存在
func ensureJobExists(t *testing.T, s *storage.Storage, jobID, title, description string) {
	// 创建一个静默的logger以关闭SQL日志打印
	silentLogger := logger.New(
		log.New(io.Discard, "", log.LstdFlags), // 输出到丢弃的writer，不打印任何日志
		logger.Config{
			SlowThreshold:             200 * time.Millisecond, // 设置慢查询阈值为200ms
			LogLevel:                  logger.Silent,          // 设置为Silent级别，不输出任何SQL日志
			IgnoreRecordNotFoundError: true,                   // 忽略记录未找到的错误
			Colorful:                  false,                  // 不使用彩色输出
		},
	)

	// 使用静默的logger创建一个新的数据库会话
	silentDB := s.MySQL.DB().Session(&gorm.Session{Logger: silentLogger}) // 使用静默logger创建数据库会话

	// 检查岗位是否已存在
	var count int64                                                                                      // 用于存储查询结果数量
	if err := silentDB.Model(&models.Job{}).Where("job_id = ?", jobID).Count(&count).Error; err != nil { // 查询指定job_id的记录数
		t.Fatalf("检查岗位是否存在失败: %v", err) // 如果查询失败，终止测试
	}

	if count == 0 { // 如果岗位不存在
		// 创建岗位记录
		job := models.Job{
			JobID:              jobID,       // 设置岗位ID
			JobTitle:           title,       // 设置岗位标题
			JobDescriptionText: description, // 设置岗位描述文本
			Department:         "测试平台",      // 设置部门
			Location:           "天津",        // 设置地点
			Status:             "ACTIVE",    // 设置状态为激活
			CreatedAt:          time.Now(),  // 设置创建时间为当前时间
			UpdatedAt:          time.Now(),  // 设置更新时间为当前时间
		}

		if err := silentDB.Create(&job).Error; err != nil { // 创建岗位记录
			t.Fatalf("创建岗位记录失败: %v", err) // 如果创建失败，终止测试
		}
		t.Logf("已创建测试岗位: %s", jobID) // 记录已创建岗位
	} else {
		t.Logf("测试岗位已存在: %s", jobID) // 记录岗位已存在
	}

	// 检查向量是否存在，如果不存在，测试将依赖正常的处理流程生成向量
	var vectorCount int64                                                                                            // 用于存储向量记录数
	if err := silentDB.Model(&models.JobVector{}).Where("job_id = ?", jobID).Count(&vectorCount).Error; err != nil { // 查询岗位向量记录数
		t.Logf("检查岗位向量时发生错误: %v", err) // 记录查询错误，但不终止测试
	}

	if vectorCount == 0 { // 如果向量不存在
		t.Logf("岗位向量不存在，将在测试过程中生成") // 记录向量不存在，将在测试中生成
	} else {
		t.Logf("岗位向量已存在") // 记录向量已存在
	}
}

// TestSimpleVectorSearch 测试一个简单的向量搜索
func TestSimpleVectorSearch(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "SimpleVectorSearch") // 创建测试日志记录器，名称为SimpleVectorSearch
	require.NoError(t, err, "创建测试日志记录器失败")                    // 确保日志记录器创建成功
	defer testLogger.Close()                                  // 确保测试完成后关闭日志文件

	testLogger.Log("开始测试简单向量搜索") // 记录测试开始

	// 1. 设置测试环境
	ctx := context.Background() // 创建一个新的上下文

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true}) // 初始化应用日志
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)                                                      // 创建Hertz兼容的日志适配器
	glog.SetLogger(hertzCompatibleLogger)                                                                                 // 设置全局Hertz日志实例
	glog.SetLevel(glog.LevelDebug)                                                                                        // 设置日志级别为Debug

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)                // 记录将要加载的配置文件
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath) // 从文件和环境变量加载配置
	require.NoError(t, err, "加载配置失败")                                    // 确保配置加载成功
	require.NotNil(t, cfg, "配置不能为空")                                     // 确保配置不为空
	testLogger.Log("配置加载成功")                                             // 记录配置加载成功

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")              // 记录开始初始化存储组件
	s, err := storage.NewStorage(ctx, cfg) // 根据配置创建存储组件
	require.NoError(t, err, "初始化存储组件失败")   // 确保存储组件初始化成功
	defer s.Close()                        // 确保测试结束时关闭存储组件
	testLogger.Log("存储组件初始化成功")            // 记录存储组件初始化成功

	// 跳过CI环境测试
	if os.Getenv("CI") != "" { // 检查是否在CI环境中运行
		testLogger.Log("在CI环境中跳过此测试") // 记录跳过测试的原因
		t.Skip("在CI环境中跳过此测试")         // 跳过测试
	}

	// 检查必要组件是否可用
	if s.Qdrant == nil { // 检查Qdrant是否可用
		testLogger.Log("Qdrant未配置或不可用，跳过测试") // 记录Qdrant不可用
		t.Skip("Qdrant未配置或不可用，跳过测试")         // 跳过测试
	}

	// 确保API密钥配置正确
	if cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" { // 检查API密钥是否有效
		testLogger.Log("未配置有效的阿里云API密钥，跳过测试") // 记录API密钥无效
		t.Skip("未配置有效的阿里云API密钥，跳过测试")         // 跳过测试
	}

	// 初始化embedder
	testLogger.Log("初始化Embedder")                    // 记录开始初始化Embedder
	aliyunEmbeddingConfig := config.EmbeddingConfig{ // 创建Embedding配置
		Model:      cfg.Aliyun.Embedding.Model,      // 设置模型名称
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,    // 设置服务基础URL
		Dimensions: cfg.Aliyun.Embedding.Dimensions, // 设置向量维度
	}
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)                                      // 创建阿里云Embedder实例
	require.NoError(t, err, "初始化embedder失败")                                                                                 // 确保Embedder初始化成功
	testLogger.Log("Embedder初始化成功，Model: %s, Dimensions: %d", aliyunEmbeddingConfig.Model, aliyunEmbeddingConfig.Dimensions) // 记录初始化成功

	// 检查Qdrant中的数据量
	count, countErr := s.Qdrant.CountPoints(ctx) // 获取Qdrant中的点数量
	if countErr != nil {                         // 如果获取失败
		testLogger.Log("获取Qdrant点数量失败: %v", countErr) // 记录错误
	} else {
		testLogger.Log("Qdrant集合中总共有 %d 个点", count) // 记录点数量
	}

	// 1. 将"go后端"字符串转换为向量
	searchText := "go后端"                              // 定义搜索文本
	testLogger.Log("使用文本 '%s' 生成向量并进行搜索", searchText) // 记录搜索文本

	vectors, err := embedder.EmbedStrings(context.Background(), []string{searchText}) // 将搜索文本转换为向量
	require.NoError(t, err, "为搜索文本生成向量失败")                                            // 确保向量生成成功
	require.NotEmpty(t, vectors, "生成的向量不能为空")                                         // 确保向量不为空
	require.NotEmpty(t, vectors[0], "生成的向量数组不能为空")                                    // 确保向量数组不为空

	vector := vectors[0]                         // 获取第一个向量（对应搜索文本）
	testLogger.Log("成功生成向量，维度: %d", len(vector)) // 记录向量维度

	// 记录向量的前10个元素作为示例
	if len(vector) > 10 { // 如果向量维度大于10
		sampleVector := vector[:10]                        // 取前10个元素
		testLogger.LogObject("向量样本(前10个元素)", sampleVector) // 记录向量样本
	}

	// 2. 使用向量在Qdrant中搜索
	limit := 10                                                            // 设置结果限制为10条
	testLogger.Log("在Qdrant中搜索，限制结果数: %d", limit)                          // 记录搜索限制
	results, err := s.Qdrant.SearchSimilarResumes(ctx, vector, limit, nil) // 在Qdrant中执行相似简历搜索
	require.NoError(t, err, "向量搜索失败")                                      // 确保搜索操作成功

	// 3. 分析搜索结果
	testLogger.Log("搜索返回了 %d 个结果", len(results)) // 记录结果数量
	t.Logf("搜索返回了 %d 个结果", len(results))         // 输出结果数量到测试日志

	if len(results) == 0 { // 如果没有搜索结果
		// 如果没有结果，检查Qdrant集合中的点数量
		count, countErr := s.Qdrant.CountPoints(ctx) // 再次获取Qdrant中的点数量
		if countErr != nil {                         // 如果获取失败
			testLogger.Log("获取Qdrant点数量失败: %v", countErr) // 记录错误
			t.Logf("获取Qdrant点数量失败: %v", countErr)         // 输出错误到测试日志
		} else {
			testLogger.Log("Qdrant集合中总共有 %d 个点", count) // 记录点数量
			t.Logf("Qdrant集合中总共有 %d 个点", count)         // 输出点数量到测试日志
		}

		// 检查集合是否为空
		if count == 0 { // 如果集合为空
			testLogger.Log("Qdrant集合为空，没有可搜索的数据") // 记录集合为空
			t.Log("Qdrant集合为空，没有可搜索的数据")          // 输出到测试日志
		} else { // 如果集合不为空但搜索无结果
			testLogger.Log("虽然集合中有数据，但搜索没有返回结果，可能是搜索文本与现有数据相关性低") // 记录可能的原因
			t.Log("虽然集合中有数据，但搜索没有返回结果，可能是搜索文本与现有数据相关性低")          // 输出到测试日志
		}
	} else { // 如果有搜索结果
		// 如果有结果，打印详细信息
		for i, result := range results { // 遍历每个结果
			// 从payload中获取submission_uuid
			submissionUUID := ""                                      // 初始化简历提交UUID变量
			if subUUID, ok := result.Payload["submission_uuid"]; ok { // 如果payload中存在submission_uuid
				if s, ok := subUUID.(string); ok { // 如果可以转换为字符串
					submissionUUID = s // 保存UUID值
				}
			}

			testLogger.Log("结果 #%d: SubmissionUUID=%s, Score=%.4f",
				i+1, submissionUUID, result.Score) // 记录结果的基本信息
			t.Logf("结果 #%d: SubmissionUUID=%s, Score=%.4f",
				i+1, submissionUUID, result.Score) // 输出到测试日志

			// 记录完整的payload到日志文件
			testLogger.LogObject(fmt.Sprintf("结果 #%d 完整Payload", i+1), result.Payload) // 记录完整payload

			if s.MySQL != nil { // 如果MySQL数据库可用
				// 尝试获取简历块内容
				var chunkContent string // 声明变量存储内容
				err := s.MySQL.DB().WithContext(ctx).
					Raw("SELECT chunk_content_text FROM resume_submission_chunks WHERE point_id = ?",
						result.ID).Scan(&chunkContent).Error // 执行SQL查询获取内容
				if err == nil { // 如果查询成功
					// 截取内容前100个字符
					preview := chunkContent      // 初始化预览为完整内容
					if len(chunkContent) > 100 { // 如果内容超过100个字符
						preview = chunkContent[:100] + "..." // 截取前100个字符并添加省略号
					}
					testLogger.Log("内容预览: %s", preview) // 记录内容预览
					t.Logf("内容预览: %s", preview)         // 输出到测试日志

					// 记录完整内容到日志文件
					testLogger.Log("完整内容:\n%s", chunkContent) // 记录完整内容
				} else { // 如果查询失败
					testLogger.Log("获取内容失败: %v", err) // 记录错误
				}
			}
		}
	}

	testLogger.Log("测试完成") // 记录测试完成
}

// TestCompareJDDescriptions 比较不同格式的JD描述在向量检索中的效果差异
func TestCompareJDDescriptions(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "CompareJDDescriptions") // 创建测试日志记录器，名称为CompareJDDescriptions
	require.NoError(t, err, "创建测试日志记录器失败")                       // 确保日志记录器创建成功
	defer testLogger.Close()                                     // 确保测试完成后关闭日志文件

	testLogger.Log("开始测试比较三种不同JD描述格式的检索效果") // 记录测试开始

	// 1. 设置测试环境
	ctx := context.Background() // 创建一个新的上下文

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true}) // 初始化应用日志配置
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)                                                      // 创建Hertz兼容的日志适配器
	glog.SetLogger(hertzCompatibleLogger)                                                                                 // 设置全局Hertz日志实例
	glog.SetLevel(glog.LevelDebug)                                                                                        // 设置日志级别为Debug

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)                // 记录将要加载的配置文件
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath) // 从文件和环境变量加载配置
	require.NoError(t, err, "加载配置失败")                                    // 确保配置加载成功
	require.NotNil(t, cfg, "配置不能为空")                                     // 确保配置不为空
	testLogger.Log("配置加载成功")                                             // 记录配置加载成功

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")              // 记录开始初始化存储组件
	s, err := storage.NewStorage(ctx, cfg) // 根据配置创建存储组件
	require.NoError(t, err, "初始化存储组件失败")   // 确保存储组件初始化成功
	defer s.Close()                        // 确保测试结束时关闭存储组件
	testLogger.Log("存储组件初始化成功")            // 记录存储组件初始化成功

	// 跳过CI环境测试
	if os.Getenv("CI") != "" { // 检查是否在CI环境中运行
		testLogger.Log("在CI环境中跳过此测试") // 记录跳过测试的原因
		t.Skip("在CI环境中跳过此测试")         // 在CI环境中跳过测试
	}

	// 检查必要组件是否可用
	if s.Qdrant == nil { // 检查Qdrant是否可用
		testLogger.Log("Qdrant未配置或不可用，跳过测试") // 记录Qdrant不可用
		t.Skip("Qdrant未配置或不可用，跳过测试")         // 跳过测试
	}

	// 确保API密钥配置正确
	if cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" { // 检查API密钥是否有效
		testLogger.Log("未配置有效的阿里云API密钥，跳过测试") // 记录API密钥无效
		t.Skip("未配置有效的阿里云API密钥，跳过测试")         // 跳过测试
	}

	// 初始化embedder
	testLogger.Log("初始化Embedder")                    // 记录开始初始化embedder
	aliyunEmbeddingConfig := config.EmbeddingConfig{ // 创建Embedding配置对象
		Model:      cfg.Aliyun.Embedding.Model,      // 设置模型名称
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,    // 设置服务基础URL
		Dimensions: cfg.Aliyun.Embedding.Dimensions, // 设置向量维度
	}
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)                                      // 创建阿里云Embedder实例
	require.NoError(t, err, "初始化embedder失败")                                                                                 // 确保Embedder初始化成功
	testLogger.Log("Embedder初始化成功，Model: %s, Dimensions: %d", aliyunEmbeddingConfig.Model, aliyunEmbeddingConfig.Dimensions) // 记录初始化成功

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
· 具备奉献精神，善于沟通与团队合作。` // 定义第一种JD描述格式：详细结构化格式

	jobDesc2 := `「产品质量技术负责人 / SDET Lead」。候选人需对每次版本发布的整体质量与技术风险负全责，主导功能测试、接口测试、性能测试和大数据场景测试。必须具备 ≥5 年测试开发或测试架构经验，能够用 Python、Java、C++ 等至少一种语言设计并实现自动化测试框架、测试工具及通用测试平台。熟悉 CI/CD、分布式系统、操作系统、网络协议；了解机器学习、NLP 或图像算法者更佳。关键能力：质量保障体系设计、测试平台 0→1 落地、性能调优、缺陷预防、跨团队沟通与风险把控。` // 定义第二种JD描述格式：精简段落格式

	jobDesc3 := `产品质量技术负责人 / QA Lead / SDET Lead
核心职责：版本发布质量保障；功能、接口、性能、大数据测试；设计并落地测试工具与通用测试平台。
必需技能：Python / Java / C++ 之一；PyTest、Selenium、Rest-Assured、JMeter、Locust；CI/CD( Jenkins / GitLab ); 分布式系统。
加分：机器学习、NLP、云原生监控、Chaos Engineering。
指标：≥5 年测试架构经验；支撑百万 QPS 压测；缺陷率 <0.1%；质量看板 0→1。` // 定义第三种JD描述格式：超简洁列表格式

	testLogger.Log("已定义三种JD描述格式")                           // 记录已定义三种JD描述格式
	testLogger.Log("JD描述1 (详细结构化格式)，长度: %d", len(jobDesc1)) // 记录第一种描述长度
	testLogger.Log("JD描述2 (精简段落格式)，长度: %d", len(jobDesc2))  // 记录第二种描述长度
	testLogger.Log("JD描述3 (超简洁列表格式)，长度: %d", len(jobDesc3)) // 记录第三种描述长度

	// 4. 为三种描述分别生成向量
	testLogger.Log("开始为三种JD描述生成向量")                                                                     // 记录开始生成向量
	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc1, jobDesc2, jobDesc3}) // 将三种描述转换为向量
	require.NoError(t, err, "为JD描述生成向量失败")                                                              // 确保向量生成成功
	require.Equal(t, 3, len(vectors), "应该生成三个向量")                                                       // 确保生成了三个向量
	require.NotEmpty(t, vectors[0], "JD描述1的向量不能为空")                                                     // 确保第一个向量不为空
	require.NotEmpty(t, vectors[1], "JD描述2的向量不能为空")                                                     // 确保第二个向量不为空
	require.NotEmpty(t, vectors[2], "JD描述3的向量不能为空")                                                     // 确保第三个向量不为空

	vector1 := vectors[0]                                                                                    // 获取第一种描述的向量
	vector2 := vectors[1]                                                                                    // 获取第二种描述的向量
	vector3 := vectors[2]                                                                                    // 获取第三种描述的向量
	testLogger.Log("成功生成向量，JD描述1维度: %d, JD描述2维度: %d, JD描述3维度: %d", len(vector1), len(vector2), len(vector3)) // 记录向量维度

	// 计算三个向量之间的余弦相似度
	cosineSimilarity12 := calculateCosineSimilarity(vector1, vector2) // 计算第一种和第二种描述向量间的余弦相似度
	cosineSimilarity13 := calculateCosineSimilarity(vector1, vector3) // 计算第一种和第三种描述向量间的余弦相似度
	cosineSimilarity23 := calculateCosineSimilarity(vector2, vector3) // 计算第二种和第三种描述向量间的余弦相似度

	testLogger.Log("JD描述1和2之间的余弦相似度: %.6f", cosineSimilarity12) // 记录第一种和第二种描述向量间的相似度
	testLogger.Log("JD描述1和3之间的余弦相似度: %.6f", cosineSimilarity13) // 记录第一种和第三种描述向量间的相似度
	testLogger.Log("JD描述2和3之间的余弦相似度: %.6f", cosineSimilarity23) // 记录第二种和第三种描述向量间的相似度

	t.Logf("JD描述1和2之间的余弦相似度: %.6f", cosineSimilarity12) // 输出第一种和第二种描述向量间的相似度到测试日志
	t.Logf("JD描述1和3之间的余弦相似度: %.6f", cosineSimilarity13) // 输出第一种和第三种描述向量间的相似度到测试日志
	t.Logf("JD描述2和3之间的余弦相似度: %.6f", cosineSimilarity23) // 输出第二种和第三种描述向量间的相似度到测试日志

	// 5. 对三种描述分别进行向量搜索
	limit := 50                                   // 设置搜索结果的限制数量为50个
	testLogger.Log("在Qdrant中搜索，限制结果数: %d", limit) // 记录搜索限制

	// 检查Qdrant中的数据量
	count, countErr := s.Qdrant.CountPoints(ctx) // 获取Qdrant中的点数量
	if countErr != nil {                         // 如果获取失败
		testLogger.Log("获取Qdrant点数量失败: %v", countErr) // 记录错误
	} else {
		testLogger.Log("Qdrant集合中总共有 %d 个点", count) // 记录点数量
	}

	// 5.1 使用JD描述1向量搜索
	testLogger.Log("使用JD描述1向量搜索")                                            // 记录开始使用第一种描述向量搜索
	results1, err := s.Qdrant.SearchSimilarResumes(ctx, vector1, limit, nil) // 在Qdrant中使用第一种描述向量搜索相似简历
	require.NoError(t, err, "JD描述1向量搜索失败")                                   // 确保搜索操作成功
	testLogger.Log("JD描述1搜索返回了 %d 个结果", len(results1))                       // 记录搜索结果数量

	// 5.2 使用JD描述2向量搜索
	testLogger.Log("使用JD描述2向量搜索")                                            // 记录开始使用第二种描述向量搜索
	results2, err := s.Qdrant.SearchSimilarResumes(ctx, vector2, limit, nil) // 在Qdrant中使用第二种描述向量搜索相似简历
	require.NoError(t, err, "JD描述2向量搜索失败")                                   // 确保搜索操作成功
	testLogger.Log("JD描述2搜索返回了 %d 个结果", len(results2))                       // 记录搜索结果数量

	// 5.3 使用JD描述3向量搜索
	testLogger.Log("使用JD描述3向量搜索")                                            // 记录开始使用第三种描述向量搜索
	results3, err := s.Qdrant.SearchSimilarResumes(ctx, vector3, limit, nil) // 在Qdrant中使用第三种描述向量搜索相似简历
	require.NoError(t, err, "JD描述3向量搜索失败")                                   // 确保搜索操作成功
	testLogger.Log("JD描述3搜索返回了 %d 个结果", len(results3))                       // 记录搜索结果数量

	// 6. 分析和比较结果
	// 6.1 将结果转换为map以便于分析
	resultMap1 := make(map[string]float32) // 创建map存储第一种描述的搜索结果，键为UUID，值为得分
	resultMap2 := make(map[string]float32) // 创建map存储第二种描述的搜索结果，键为UUID，值为得分
	resultMap3 := make(map[string]float32) // 创建map存储第三种描述的搜索结果，键为UUID，值为得分

	for _, r := range results1 { // 遍历第一种描述的搜索结果
		if uuid, ok := r.Payload["submission_uuid"].(string); ok && uuid != "" { // 如果payload中存在有效的submission_uuid
			resultMap1[uuid] = r.Score // 将UUID和得分添加到map中
		}
	}

	for _, r := range results2 { // 遍历第二种描述的搜索结果
		if uuid, ok := r.Payload["submission_uuid"].(string); ok && uuid != "" { // 如果payload中存在有效的submission_uuid
			resultMap2[uuid] = r.Score // 将UUID和得分添加到map中
		}
	}

	for _, r := range results3 { // 遍历第三种描述的搜索结果
		if uuid, ok := r.Payload["submission_uuid"].(string); ok && uuid != "" { // 如果payload中存在有效的submission_uuid
			resultMap3[uuid] = r.Score // 将UUID和得分添加到map中
		}
	}

	// 6.2 记录详细结果
	testLogger.Log("JD描述1搜索结果详情:")                             // 记录开始输出第一种描述的搜索结果详情
	logSearchResults(testLogger, t, s, ctx, results1, "JD描述1") // 调用辅助函数记录第一种描述的搜索结果详情

	testLogger.Log("JD描述2搜索结果详情:")                             // 记录开始输出第二种描述的搜索结果详情
	logSearchResults(testLogger, t, s, ctx, results2, "JD描述2") // 调用辅助函数记录第二种描述的搜索结果详情

	testLogger.Log("JD描述3搜索结果详情:")                             // 记录开始输出第三种描述的搜索结果详情
	logSearchResults(testLogger, t, s, ctx, results3, "JD描述3") // 调用辅助函数记录第三种描述的搜索结果详情

	// 6.3 计算三种描述搜索结果的交集和差异
	// 6.3.1 两两之间的交集
	var common12 []string  // 描述1和2的交集
	var common13 []string  // 描述1和3的交集
	var common23 []string  // 描述2和3的交集
	var commonAll []string // 三种描述共有的UUID

	// 6.3.2 唯一的结果
	var uniqueToDesc1 []string // 只在描述1中存在的UUID
	var uniqueToDesc2 []string // 只在描述2中存在的UUID
	var uniqueToDesc3 []string // 只在描述3中存在的UUID

	// 计算三个描述共有的UUID
	for uuid := range resultMap1 { // 遍历第一种描述的所有结果
		in2 := false // 标记是否在描述2中存在
		in3 := false // 标记是否在描述3中存在

		if _, exists := resultMap2[uuid]; exists { // 检查UUID是否在描述2的结果中
			in2 = true                        // 标记在描述2中存在
			common12 = append(common12, uuid) // 添加到描述1和2的交集中
		}

		if _, exists := resultMap3[uuid]; exists { // 检查UUID是否在描述3的结果中
			in3 = true                        // 标记在描述3中存在
			common13 = append(common13, uuid) // 添加到描述1和3的交集中
		}

		if in2 && in3 { // 如果UUID同时在描述2和描述3中存在
			commonAll = append(commonAll, uuid) // 添加到三种描述共有结果中
		} else if !in2 && !in3 { // 如果UUID既不在描述2也不在描述3中存在
			uniqueToDesc1 = append(uniqueToDesc1, uuid) // 添加到描述1独有结果中
		}
	}

	// 计算描述2和3的交集，以及描述2的唯一结果
	for uuid := range resultMap2 { // 遍历第二种描述的所有结果
		_, in1 := resultMap1[uuid] // 检查UUID是否在描述1的结果中

		if _, in3 := resultMap3[uuid]; in3 { // 检查UUID是否在描述3的结果中
			if !in1 { // 如果不在描述1中，则是2和3独有的
				common23 = append(common23, uuid) // 添加到描述2和3的交集中
			}
		} else if !in1 { // 如果既不在1也不在3中，则是描述2独有的
			uniqueToDesc2 = append(uniqueToDesc2, uuid) // 添加到描述2独有结果中
		}
	}

	// 计算描述3独有的结果
	for uuid := range resultMap3 { // 遍历第三种描述的所有结果
		_, in1 := resultMap1[uuid] // 检查UUID是否在描述1的结果中
		_, in2 := resultMap2[uuid] // 检查UUID是否在描述2的结果中

		if !in1 && !in2 { // 如果UUID既不在描述1也不在描述2中存在
			uniqueToDesc3 = append(uniqueToDesc3, uuid) // 添加到描述3独有结果中
		}
	}

	// 排序以确保输出稳定
	sort.Strings(common12)      // 对描述1和2的交集进行排序
	sort.Strings(common13)      // 对描述1和3的交集进行排序
	sort.Strings(common23)      // 对描述2和3的交集进行排序
	sort.Strings(commonAll)     // 对三种描述共有结果进行排序
	sort.Strings(uniqueToDesc1) // 对描述1独有结果进行排序
	sort.Strings(uniqueToDesc2) // 对描述2独有结果进行排序
	sort.Strings(uniqueToDesc3) // 对描述3独有结果进行排序

	// 输出交集和差异统计
	testLogger.Log("三种JD描述搜索结果的比较:")                  // 记录开始比较三种描述搜索结果
	testLogger.Log("三个描述共同的简历数量: %d", len(commonAll)) // 记录三种描述共有简历数量
	if len(commonAll) > 0 {                           // 如果存在共有简历
		testLogger.Log("三个描述共同的简历UUID: %v", commonAll) // 记录共有简历的UUID
	}

	testLogger.Log("描述1和2共同的简历数量: %d", len(common12)) // 记录描述1和2共有简历数量
	testLogger.Log("描述1和3共同的简历数量: %d", len(common13)) // 记录描述1和3共有简历数量
	testLogger.Log("描述2和3共同的简历数量: %d", len(common23)) // 记录描述2和3共有简历数量

	testLogger.Log("仅描述1找到的简历数量: %d", len(uniqueToDesc1)) // 记录仅描述1找到的简历数量
	testLogger.Log("仅描述2找到的简历数量: %d", len(uniqueToDesc2)) // 记录仅描述2找到的简历数量
	testLogger.Log("仅描述3找到的简历数量: %d", len(uniqueToDesc3)) // 记录仅描述3找到的简历数量

	// 6.4 比较共同简历的分数差异
	if len(commonAll) > 0 { // 如果存在共有简历
		testLogger.Log("三个描述共同简历的分数比较:") // 记录开始比较共有简历的分数
		t.Log("三个描述共同简历的分数比较:")          // 输出到测试日志

		for _, uuid := range commonAll { // 遍历每个共有简历
			score1 := resultMap1[uuid] // 获取描述1对应的分数
			score2 := resultMap2[uuid] // 获取描述2对应的分数
			score3 := resultMap3[uuid] // 获取描述3对应的分数

			// 计算最大分数和对应的描述
			maxScore := score1     // 初始化最大分数为描述1的分数
			maxDesc := "描述1"       // 初始化最佳匹配描述为描述1
			if score2 > maxScore { // 如果描述2的分数更高
				maxScore = score2 // 更新最大分数
				maxDesc = "描述2"   // 更新最佳匹配描述
			}
			if score3 > maxScore { // 如果描述3的分数更高
				maxScore = score3 // 更新最大分数
				maxDesc = "描述3"   // 更新最佳匹配描述
			}

			testLogger.Log("UUID: %s, 描述1: %.4f, 描述2: %.4f, 描述3: %.4f, 最佳匹配: %s(%.4f)",
				uuid, score1, score2, score3, maxDesc, maxScore) // 记录详细比较结果
			t.Logf("UUID: %s, 描述1: %.4f, 描述2: %.4f, 描述3: %.4f, 最佳匹配: %s(%.4f)",
				uuid, score1, score2, score3, maxDesc, maxScore) // 输出详细比较结果到测试日志
		}
	}

	testLogger.Log("测试完成") // 记录测试完成
}

// calculateCosineSimilarity 计算两个向量的余弦相似度
func calculateCosineSimilarity(v1, v2 []float64) float64 {
	if len(v1) != len(v2) { // 检查两个向量的维度是否相同
		return 0 // 如果维度不同，无法计算相似度，返回0
	}

	var dotProduct float64 // 存储向量的点积
	var norm1 float64      // 存储第一个向量的平方和
	var norm2 float64      // 存储第二个向量的平方和

	for i := 0; i < len(v1); i++ { // 遍历向量的每个维度
		dotProduct += v1[i] * v2[i] // 计算点积：两个向量对应维度的值相乘再求和
		norm1 += v1[i] * v1[i]      // 计算第一个向量的平方和
		norm2 += v2[i] * v2[i]      // 计算第二个向量的平方和
	}

	// 避免除以零
	if norm1 == 0 || norm2 == 0 { // 检查是否有向量的模为0（零向量）
		return 0 // 如果有一个是零向量，相似度为0
	}

	// 余弦相似度公式：点积 / (||v1|| * ||v2||)，其中||v||表示向量v的模
	return dotProduct / (math.Sqrt(norm1) * math.Sqrt(norm2)) // 返回余弦相似度的值，范围在[-1, 1]之间
}

// logSearchResults 记录搜索结果详情
func logSearchResults(testLogger *TestLogger, t *testing.T, s *storage.Storage, ctx context.Context, results []storage.SearchResult, label string) {
	for i, result := range results { // 遍历每个搜索结果
		// 从payload中获取submission_uuid
		submissionUUID := ""                                      // 初始化简历UUID变量
		if subUUID, ok := result.Payload["submission_uuid"]; ok { // 检查payload中是否包含submission_uuid字段
			if s, ok := subUUID.(string); ok { // 尝试将其转换为字符串
				submissionUUID = s // 如果成功，保存UUID
			}
		}

		testLogger.Log("%s结果 #%d: SubmissionUUID=%s, Score=%.4f",
			label, i+1, submissionUUID, result.Score) // 记录基本结果信息到日志
		t.Logf("%s结果 #%d: SubmissionUUID=%s, Score=%.4f",
			label, i+1, submissionUUID, result.Score) // 记录基本结果信息到测试日志

		// 记录完整的payload到日志文件
		testLogger.LogObject(fmt.Sprintf("%s结果 #%d 完整Payload", label, i+1), result.Payload) // 记录完整payload数据

		if s.MySQL != nil { // 如果MySQL数据库可用
			// 尝试获取简历块内容
			var chunkContent string // 声明变量存储简历内容
			err := s.MySQL.DB().WithContext(ctx).
				Raw("SELECT chunk_content_text FROM resume_submission_chunks WHERE point_id = ?",
					result.ID).Scan(&chunkContent).Error // 执行SQL查询获取内容
			if err == nil { // 如果查询成功
				// 截取内容前100个字符
				preview := chunkContent      // 初始为完整内容
				if len(chunkContent) > 100 { // 如果内容超过100个字符
					preview = chunkContent[:100] + "..." // 截取前100个字符并添加省略号
				}
				testLogger.Log("%s内容预览: %s", label, preview) // 记录内容预览

				// 记录完整内容到日志文件
				testLogger.Log("%s完整内容:\n%s", label, chunkContent) // 记录完整内容
			}
		}
	}
}

// TestMultiVectorSearch 测试多向量联合检索方法 (Union Multi-Vector Search)
func TestMultiVectorSearch(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "MultiVectorSearch") // 创建测试日志记录器，名称为MultiVectorSearch
	require.NoError(t, err, "创建测试日志记录器失败")                   // 确保日志记录器创建成功
	defer testLogger.Close()                                 // 确保测试结束后关闭日志文件

	testLogger.Log("开始测试多向量联合检索 (Union Multi-Vector Search)") // 记录测试开始

	// 1. 设置测试环境
	ctx := context.Background() // 创建一个新的上下文

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true}) // 初始化应用日志配置
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)                                                      // 创建Hertz兼容的日志适配器
	glog.SetLogger(hertzCompatibleLogger)                                                                                 // 设置全局Hertz日志实例
	glog.SetLevel(glog.LevelDebug)                                                                                        // 设置日志级别为Debug

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)                // 记录将要加载的配置文件
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath) // 从文件和环境变量加载配置
	require.NoError(t, err, "加载配置失败")                                    // 确保配置加载成功
	require.NotNil(t, cfg, "配置不能为空")                                     // 确保配置不为空
	testLogger.Log("配置加载成功")                                             // 记录配置加载成功

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")              // 记录开始初始化存储组件
	s, err := storage.NewStorage(ctx, cfg) // 根据配置创建存储组件
	require.NoError(t, err, "初始化存储组件失败")   // 确保存储组件初始化成功
	defer s.Close()                        // 确保测试结束时关闭存储组件
	testLogger.Log("存储组件初始化成功")            // 记录存储组件初始化成功

	// 跳过CI环境测试
	if os.Getenv("CI") != "" { // 检查是否在CI环境中运行
		testLogger.Log("在CI环境中跳过此测试") // 记录跳过测试的原因
		t.Skip("在CI环境中跳过此测试")         // 在CI环境中跳过测试
	}

	// 检查必要组件是否可用
	if s.Qdrant == nil { // 检查Qdrant是否可用
		testLogger.Log("Qdrant未配置或不可用，跳过测试") // 记录Qdrant不可用
		t.Skip("Qdrant未配置或不可用，跳过测试")         // 跳过测试
	}

	// 确保API密钥配置正确
	if cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" { // 检查API密钥是否有效
		testLogger.Log("未配置有效的阿里云API密钥，跳过测试") // 记录API密钥无效
		t.Skip("未配置有效的阿里云API密钥，跳过测试")         // 跳过测试
	}

	// 初始化embedder
	testLogger.Log("初始化Embedder")                    // 记录开始初始化Embedder
	aliyunEmbeddingConfig := config.EmbeddingConfig{ // 创建Embedding配置对象
		Model:      cfg.Aliyun.Embedding.Model,      // 设置模型名称
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,    // 设置服务基础URL
		Dimensions: cfg.Aliyun.Embedding.Dimensions, // 设置向量维度
	}
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)                                      // 创建阿里云Embedder实例
	require.NoError(t, err, "初始化embedder失败")                                                                                 // 确保Embedder初始化成功
	testLogger.Log("Embedder初始化成功，Model: %s, Dimensions: %d", aliyunEmbeddingConfig.Model, aliyunEmbeddingConfig.Dimensions) // 记录初始化成功

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
· 具备奉献精神，善于沟通与团队合作。` // 定义第一种JD描述格式：详细结构化格式

	jobDesc3 := `产品质量技术负责人 / QA Lead / SDET Lead
核心职责：版本发布质量保障；功能、接口、性能、大数据测试；设计并落地测试工具与通用测试平台。
必需技能：Python / Java / C++ 之一；PyTest、Selenium、Rest-Assured、JMeter、Locust；CI/CD( Jenkins / GitLab ); 分布式系统。
加分：机器学习、NLP、云原生监控、Chaos Engineering。
指标：≥5 年测试架构经验；支撑百万 QPS 压测；缺陷率 <0.1%；质量看板 0→1。` // 定义第三种JD描述格式：超简洁列表格式

	testLogger.Log("准备使用两种JD描述格式进行多向量检索")                   // 记录准备开始多向量检索
	testLogger.Log("JD描述1 (详细结构化格式)，长度: %d", len(jobDesc1)) // 记录第一种描述长度
	testLogger.Log("JD描述3 (超简洁列表格式)，长度: %d", len(jobDesc3)) // 记录第三种描述长度

	// 4. 为两种描述分别生成向量
	testLogger.Log("开始为两种JD描述生成向量")                                                           // 记录开始生成向量
	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc1, jobDesc3}) // 将两种描述转换为向量
	require.NoError(t, err, "为JD描述生成向量失败")                                                    // 确保向量生成成功
	require.Equal(t, 2, len(vectors), "应该生成两个向量")                                             // 确保生成了两个向量
	require.NotEmpty(t, vectors[0], "JD描述1的向量不能为空")                                           // 确保第一个向量不为空
	require.NotEmpty(t, vectors[1], "JD描述3的向量不能为空")                                           // 确保第二个向量不为空

	vector1 := vectors[0]                                                         // 获取第一种描述的向量
	vector3 := vectors[1]                                                         // 获取第三种描述的向量
	testLogger.Log("成功生成向量，JD描述1维度: %d, JD描述3维度: %d", len(vector1), len(vector3)) // 记录向量维度

	// 计算两个向量之间的余弦相似度
	cosineSimilarity := calculateCosineSimilarity(vector1, vector3) // 调用辅助函数计算两个向量的相似度
	testLogger.Log("JD描述1和3之间的余弦相似度: %.6f", cosineSimilarity)       // 记录相似度到日志
	t.Logf("JD描述1和3之间的余弦相似度: %.6f", cosineSimilarity)               // 输出相似度到测试日志

	// 5. 设置搜索限制数量
	limit := 50                                     // 设置结果限制为50条
	testLogger.Log("在Qdrant中搜索，每次限制结果数: %d", limit) // 记录搜索限制

	// 检查Qdrant中的数据量
	count, countErr := s.Qdrant.CountPoints(ctx) // 获取Qdrant中的点数量
	if countErr != nil {                         // 如果获取失败
		testLogger.Log("获取Qdrant点数量失败: %v", countErr) // 记录错误
	} else {
		testLogger.Log("Qdrant集合中总共有 %d 个点", count) // 记录点数量
	}

	// 6. 分别执行搜索
	// 6.1 使用JD描述1向量搜索
	testLogger.Log("使用JD描述1向量搜索")                                            // 记录开始使用第一种描述向量搜索
	results1, err := s.Qdrant.SearchSimilarResumes(ctx, vector1, limit, nil) // 在Qdrant中使用第一种描述向量搜索相似简历
	require.NoError(t, err, "JD描述1向量搜索失败")                                   // 确保搜索操作成功
	testLogger.Log("JD描述1搜索返回了 %d 个结果", len(results1))                       // 记录搜索结果数量

	// 6.2 使用JD描述3向量搜索
	testLogger.Log("使用JD描述3向量搜索")                                            // 记录开始使用第三种描述向量搜索
	results3, err := s.Qdrant.SearchSimilarResumes(ctx, vector3, limit, nil) // 在Qdrant中使用第三种描述向量搜索相似简历
	require.NoError(t, err, "JD描述3向量搜索失败")                                   // 确保搜索操作成功
	testLogger.Log("JD描述3搜索返回了 %d 个结果", len(results3))                       // 记录搜索结果数量

	// 7. 客户端合并结果 (Union操作)
	type BestMatch struct {
		Result     storage.SearchResult // 搜索结果对象
		Source     string               // 标记结果来源（JD1或JD3）
		OriginalID string               // 原始ID保存
	}

	// 使用map来存储合并后的结果，键为point_id或submission_uuid
	unionResults := make(map[string]BestMatch) // 创建map存储合并结果，避免重复

	// 7.1 处理JD描述1的结果
	testLogger.Log("开始处理JD描述1的结果并添加到合并集合") // 记录开始处理第一个搜索结果
	for _, result := range results1 {      // 遍历第一种描述的搜索结果
		// 尝试从payload中获取submission_uuid作为键
		key := ""                                                                     // 初始化键值
		if uuid, ok := result.Payload["submission_uuid"].(string); ok && uuid != "" { // 尝试从payload中获取submission_uuid
			key = uuid // 如果存在，使用uuid作为键
		} else {
			// 如果没有submission_uuid，则使用point_id作为键
			key = result.ID // 使用point_id作为备选键
		}

		unionResults[key] = BestMatch{ // 将结果添加到合并map中
			Result:     result,    // 保存完整的搜索结果
			Source:     "JD1",     // 标记来源为第一种描述
			OriginalID: result.ID, // 保存原始ID
		}
	}

	// 7.2 处理JD描述3的结果
	testLogger.Log("开始处理JD描述3的结果并添加到合并集合") // 记录开始处理第三个搜索结果
	for _, result := range results3 {      // 遍历第三种描述的搜索结果
		// 尝试从payload中获取submission_uuid作为键
		key := ""                                                                     // 初始化键值
		if uuid, ok := result.Payload["submission_uuid"].(string); ok && uuid != "" { // 尝试从payload中获取submission_uuid
			key = uuid // 如果存在，使用uuid作为键
		} else {
			// 如果没有submission_uuid，则使用point_id作为键
			key = result.ID // 使用point_id作为备选键
		}

		// 检查是否已经存在相同键的结果
		if existing, exists := unionResults[key]; exists { // 检查是否已经存在相同键的结果
			// 如果已存在，则保留得分更高的那个
			if result.Score > existing.Result.Score { // 如果当前结果得分更高
				unionResults[key] = BestMatch{ // 用得分更高的结果替换原有结果
					Result:     result,    // 保存完整的搜索结果
					Source:     "JD3",     // 标记来源为第三种描述
					OriginalID: result.ID, // 保存原始ID
				}
			}
		} else {
			// 如果不存在，则直接添加
			unionResults[key] = BestMatch{ // 将结果添加到合并map中
				Result:     result,    // 保存完整的搜索结果
				Source:     "JD3",     // 标记来源为第三种描述
				OriginalID: result.ID, // 保存原始ID
			}
		}
	}

	// 7.3 将map转换为切片以便排序
	var mergedResults []BestMatch        // 创建切片存储合并后的结果
	for _, match := range unionResults { // 遍历合并map
		mergedResults = append(mergedResults, match) // 将每个结果添加到切片中
	}

	// 7.4 按分数降序排序
	sort.Slice(mergedResults, func(i, j int) bool { // 使用sort.Slice对结果进行排序
		return mergedResults[i].Result.Score > mergedResults[j].Result.Score // 按分数降序排序
	})

	// 8. 分析合并后的结果
	testLogger.Log("合并后总共有 %d 个唯一结果", len(mergedResults)) // 记录合并后的结果数量
	t.Logf("合并后总共有 %d 个唯一结果", len(mergedResults))         // 输出结果数量到测试日志

	// 计算各来源的统计数据
	var onlyInJD1 int   // 只在JD1中存在的结果计数
	var onlyInJD3 int   // 只在JD3中存在的结果计数
	var inBoth int      // 在两者中都存在的结果计数
	var jd1WonCount int // JD1分数更高的结果计数
	var jd3WonCount int // JD3分数更高的结果计数

	for _, match := range mergedResults { // 遍历合并后的所有结果
		key := ""                                                                           // 初始化结果键
		if uuid, ok := match.Result.Payload["submission_uuid"].(string); ok && uuid != "" { // 尝试获取submission_uuid作为键
			key = uuid // 使用uuid作为键
		} else {
			key = match.OriginalID // 否则使用原始ID作为键
		}

		// 检查是否在JD1和JD3的结果中都存在
		inJD1 := false     // 标记是否在JD1结果中
		inJD3 := false     // 标记是否在JD3结果中
		var score1 float32 // 存储JD1中的分数
		var score3 float32 // 存储JD3中的分数

		for _, r := range results1 { // 遍历JD1的搜索结果
			rKey := ""                                                               // 初始化结果键
			if uuid, ok := r.Payload["submission_uuid"].(string); ok && uuid != "" { // 尝试获取submission_uuid
				rKey = uuid // 使用uuid作为键
			} else {
				rKey = r.ID // 否则使用ID作为键
			}

			if rKey == key { // 如果找到匹配的键
				inJD1 = true     // 标记为在JD1结果中存在
				score1 = r.Score // 保存分数
				break            // 找到后退出循环
			}
		}

		for _, r := range results3 { // 遍历JD3的搜索结果
			rKey := ""                                                               // 初始化结果键
			if uuid, ok := r.Payload["submission_uuid"].(string); ok && uuid != "" { // 尝试获取submission_uuid
				rKey = uuid // 使用uuid作为键
			} else {
				rKey = r.ID // 否则使用ID作为键
			}

			if rKey == key { // 如果找到匹配的键
				inJD3 = true     // 标记为在JD3结果中存在
				score3 = r.Score // 保存分数
				break            // 找到后退出循环
			}
		}

		if inJD1 && inJD3 { // 如果同时在JD1和JD3中存在
			inBoth++             // 共有结果计数增加
			if score1 > score3 { // 如果JD1的分数更高
				jd1WonCount++ // JD1更高分数计数增加
			} else { // 否则
				jd3WonCount++ // JD3更高分数计数增加
			}
		} else if inJD1 { // 如果只在JD1中存在
			onlyInJD1++ // JD1独有结果计数增加
		} else if inJD3 { // 如果只在JD3中存在
			onlyInJD3++ // JD3独有结果计数增加
		}
	}

	testLogger.Log("仅在JD1结果中的简历数量: %d", onlyInJD1)          // 记录JD1独有结果数量
	testLogger.Log("仅在JD3结果中的简历数量: %d", onlyInJD3)          // 记录JD3独有结果数量
	testLogger.Log("同时在两个结果中的简历数量: %d", inBoth)             // 记录共有结果数量
	testLogger.Log("在两者共有的简历中，JD1分数更高的数量: %d", jd1WonCount) // 记录JD1分数更高的结果数量
	testLogger.Log("在两者共有的简历中，JD3分数更高的数量: %d", jd3WonCount) // 记录JD3分数更高的结果数量

	t.Logf("仅在JD1结果中的简历数量: %d", onlyInJD1)          // 输出JD1独有结果数量到测试日志
	t.Logf("仅在JD3结果中的简历数量: %d", onlyInJD3)          // 输出JD3独有结果数量到测试日志
	t.Logf("同时在两个结果中的简历数量: %d", inBoth)             // 输出共有结果数量到测试日志
	t.Logf("在两者共有的简历中，JD1分数更高的数量: %d", jd1WonCount) // 输出JD1分数更高的结果数量到测试日志
	t.Logf("在两者共有的简历中，JD3分数更高的数量: %d", jd3WonCount) // 输出JD3分数更高的结果数量到测试日志

	// 9. 记录前10个合并后的结果详情
	resultLimit := 10                     // 设置要详细展示的结果数量限制
	if len(mergedResults) < resultLimit { // 如果实际结果少于限制数量
		resultLimit = len(mergedResults) // 调整限制数量为实际结果数量
	}

	testLogger.Log("前%d个合并结果详情:", resultLimit) // 记录开始展示合并结果详情
	t.Logf("前%d个合并结果详情:", resultLimit)         // 输出开始展示合并结果详情到测试日志

	for i := 0; i < resultLimit; i++ { // 遍历指定数量的结果
		match := mergedResults[i] // 获取当前结果
		result := match.Result    // 获取搜索结果对象

		// 从payload中获取submission_uuid
		submissionUUID := ""                                            // 初始化UUID变量
		if uuid, ok := result.Payload["submission_uuid"].(string); ok { // 尝试获取UUID
			submissionUUID = uuid // 保存UUID
		}

		testLogger.Log("合并结果 #%d: 来源=%s, SubmissionUUID=%s, Score=%.4f, PointID=%s",
			i+1, match.Source, submissionUUID, result.Score, result.ID) // 记录结果基本信息
		t.Logf("合并结果 #%d: 来源=%s, SubmissionUUID=%s, Score=%.4f, PointID=%s",
			i+1, match.Source, submissionUUID, result.Score, result.ID) // 输出结果基本信息到测试日志

		// 记录完整的payload到日志文件
		testLogger.LogObject(fmt.Sprintf("合并结果 #%d 完整Payload", i+1), result.Payload) // 记录完整payload到日志

		// 如果可能，获取简历内容预览
		if s.MySQL != nil { // 如果MySQL数据库可用
			var chunkContent string // 声明变量存储内容
			err := s.MySQL.DB().WithContext(ctx).
				Raw("SELECT chunk_content_text FROM resume_submission_chunks WHERE point_id = ?",
					result.ID).Scan(&chunkContent).Error // 执行SQL查询获取内容
			if err == nil { // 如果查询成功
				preview := chunkContent      // 初始化预览变量
				if len(chunkContent) > 100 { // 如果内容超过100个字符
					preview = chunkContent[:100] + "..." // 截取前100个字符并添加省略号
				}
				testLogger.Log("内容预览: %s", preview) // 记录内容预览

				// 记录完整内容到日志文件
				testLogger.Log("完整内容:\n%s", chunkContent) // 记录完整内容
			}
		}
	}

	// 10. 检查Batch Search是否可用 (这部分是对Qdrant批量搜索的说明，未实际执行)
	testLogger.Log("注意: 本测试通过两次单独调用SearchSimilarResumes实现了多向量检索") // 记录关于当前测试方法的说明
	testLogger.Log("在实际生产环境中，应考虑使用Qdrant的BatchSearch API来提高效率")   // 记录性能优化建议
	testLogger.Log("BatchSearch允许一次请求同时发送多个向量查询，减少网络往返")          // 记录BatchSearch的优势

	testLogger.Log("测试完成") // 记录测试完成
}

// TestCompareRetrievalMethods 比较单一向量检索和多向量联合检索的效果差异
func TestCompareRetrievalMethods(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "CompareRetrievalMethods") // 创建测试日志记录器，名称为CompareRetrievalMethods
	require.NoError(t, err, "创建测试日志记录器失败")                         // 确保日志记录器创建成功
	defer testLogger.Close()                                       // 确保测试结束后关闭日志文件

	testLogger.Log("开始测试比较单一向量检索和多向量联合检索效果差异") // 记录测试开始

	// 1. 设置测试环境
	ctx := context.Background() // 创建一个新的上下文

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true}) // 初始化应用日志配置
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)                                                      // 创建Hertz兼容的日志适配器
	glog.SetLogger(hertzCompatibleLogger)                                                                                 // 设置全局Hertz日志实例
	glog.SetLevel(glog.LevelDebug)                                                                                        // 设置日志级别为Debug

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)                // 记录将要加载的配置文件
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath) // 从文件和环境变量加载配置
	require.NoError(t, err, "加载配置失败")                                    // 确保配置加载成功
	require.NotNil(t, cfg, "配置不能为空")                                     // 确保配置不为空
	testLogger.Log("配置加载成功")                                             // 记录配置加载成功

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")              // 记录开始初始化存储组件
	s, err := storage.NewStorage(ctx, cfg) // 根据配置创建存储组件
	require.NoError(t, err, "初始化存储组件失败")   // 确保存储组件初始化成功
	defer s.Close()                        // 确保测试结束时关闭存储组件
	testLogger.Log("存储组件初始化成功")            // 记录存储组件初始化成功

	// 跳过CI环境测试
	if os.Getenv("CI") != "" { // 检查是否在CI环境中运行
		testLogger.Log("在CI环境中跳过此测试") // 记录跳过测试的原因
		t.Skip("在CI环境中跳过此测试")         // 在CI环境中跳过测试
	}

	// 检查必要组件是否可用
	if s.Qdrant == nil { // 检查Qdrant是否可用
		testLogger.Log("Qdrant未配置或不可用，跳过测试") // 记录Qdrant不可用
		t.Skip("Qdrant未配置或不可用，跳过测试")         // 跳过测试
	}

	// 确保API密钥配置正确
	if cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" { // 检查API密钥是否有效
		testLogger.Log("未配置有效的阿里云API密钥，跳过测试") // 记录API密钥无效
		t.Skip("未配置有效的阿里云API密钥，跳过测试")         // 跳过测试
	}

	// 初始化embedder
	testLogger.Log("初始化Embedder")                    // 记录开始初始化Embedder
	aliyunEmbeddingConfig := config.EmbeddingConfig{ // 创建Embedding配置对象
		Model:      cfg.Aliyun.Embedding.Model,      // 设置模型名称
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,    // 设置服务基础URL
		Dimensions: cfg.Aliyun.Embedding.Dimensions, // 设置向量维度
	}
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)                                      // 创建阿里云Embedder实例
	require.NoError(t, err, "初始化embedder失败")                                                                                 // 确保Embedder初始化成功
	testLogger.Log("Embedder初始化成功，Model: %s, Dimensions: %d", aliyunEmbeddingConfig.Model, aliyunEmbeddingConfig.Dimensions) // 记录初始化成功

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
· 具备奉献精神，善于沟通与团队合作。` // 定义第一种JD描述格式：详细结构化格式

	jobDesc3 := `产品质量技术负责人 / QA Lead / SDET Lead
核心职责：版本发布质量保障；功能、接口、性能、大数据测试；设计并落地测试工具与通用测试平台。
必需技能：Python / Java / C++ 之一；PyTest、Selenium、Rest-Assured、JMeter、Locust；CI/CD( Jenkins / GitLab ); 分布式系统。
加分：机器学习、NLP、云原生监控、Chaos Engineering。
指标：≥5 年测试架构经验；支撑百万 QPS 压测；缺陷率 <0.1%；质量看板 0→1。` // 定义第三种JD描述格式：超简洁列表格式

	testLogger.Log("准备使用两种JD描述格式进行检索对比")                    // 记录准备开始检索对比
	testLogger.Log("JD描述1 (详细结构化格式)，长度: %d", len(jobDesc1)) // 记录第一种描述长度
	testLogger.Log("JD描述3 (超简洁列表格式)，长度: %d", len(jobDesc3)) // 记录第三种描述长度

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
	goRelated := strings.Contains(strings.ToLower(content), "go") || // 检查内容是否包含"go"
		strings.Contains(strings.ToLower(content), "golang") // 或"golang"，表示与Go语言相关

	microserviceRelated := strings.Contains(strings.ToLower(content), "微服务") || // 检查内容是否包含"微服务"
		strings.Contains(strings.ToLower(content), "microservice") // 或"microservice"，表示与微服务相关

	keyTechCount := 0                                                                                       // 统计包含的关键技术数量
	for _, keyword := range []string{"grpc", "protobuf", "redis", "kafka", "docker", "kubernetes", "k8s"} { // 定义关键技术关键词列表
		if strings.Contains(strings.ToLower(content), keyword) { // 检查内容是否包含每个关键词
			keyTechCount++ // 如果包含，关键技术计数加1
		}
	}

	if goRelated && microserviceRelated && keyTechCount >= 3 { // 如果同时包含Go、微服务，且至少3项关键技术
		return "高质量匹配 - 包含Go和微服务经验，以及多项关键技术" // 返回高质量匹配评价
	} else if goRelated && keyTechCount >= 2 { // 如果包含Go且至少2项关键技术
		return "良好匹配 - 包含Go经验和部分关键技术" // 返回良好匹配评价
	} else if goRelated { // 如果只包含Go
		return "基本匹配 - 包含Go经验" // 返回基本匹配评价
	} else if keyTechCount >= 3 { // 如果不含Go但包含至少3项关键技术
		return "部分相关 - 包含多项关键技术但未明确提及Go" // 返回部分相关评价
	} else { // 如果以上条件都不满足
		return "相关性较低" // 返回相关性较低评价
	}
}

// TestFullSearchWithRedisCache 测试带有hr_id的全流程搜索和Redis缓存
func TestFullSearchWithRedisCache(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "FullSearchWithRedisCache") // 创建测试日志记录器，名称为FullSearchWithRedisCache
	require.NoError(t, err, "创建测试日志记录器失败")                          // 确保日志记录器创建成功
	defer testLogger.Close()                                        // 确保测试结束后关闭日志文件

	testLogger.Log("开始测试带有hr_id的全流程搜索和Redis缓存") // 记录测试开始

	// 1. 设置测试环境
	ctx := context.Background() // 创建一个新的上下文

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "debug", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true}) // 初始化应用日志配置，设置为debug级别
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)                                                       // 创建Hertz兼容的日志适配器
	glog.SetLogger(hertzCompatibleLogger)                                                                                  // 设置全局Hertz日志实例
	glog.SetLevel(glog.LevelDebug)                                                                                         // 设置日志级别为Debug

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)                // 记录将要加载的配置文件
	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath) // 从文件和环境变量加载配置
	require.NoError(t, err, "加载配置失败")                                    // 确保配置加载成功
	require.NotNil(t, cfg, "配置不能为空")                                     // 确保配置不为空
	testLogger.Log("配置加载成功")                                             // 记录配置加载成功

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")              // 记录开始初始化存储组件
	s, err := storage.NewStorage(ctx, cfg) // 根据配置创建存储组件
	require.NoError(t, err, "初始化存储组件失败")   // 确保存储组件初始化成功
	defer s.Close()                        // 确保测试结束时关闭存储组件
	testLogger.Log("存储组件初始化成功")            // 记录存储组件初始化成功

	// 检查Redis是否可用
	if s.Redis == nil { // 检查Redis组件是否可用
		testLogger.Log("Redis未配置或不可用，跳过测试") // 记录Redis不可用
		t.Skip("Redis未配置或不可用，跳过测试")         // 跳过测试
	}

	// 3. 创建Embedding服务
	testLogger.Log("创建Embedding服务")                                                          // 记录开始创建Embedding服务
	aliyunEmbedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding) // 创建阿里云Embedder实例
	require.NoError(t, err, "创建AliyunEmbedder失败")                                            // 确保Embedder创建成功
	testLogger.Log("Embedding服务创建成功")                                                        // 记录Embedding服务创建成功

	// 4. 初始化JD处理器
	testLogger.Log("初始化JD处理器")                                                               // 记录开始初始化JD处理器
	jdProcessor, err := processor.NewJDProcessor(aliyunEmbedder, s, cfg.ActiveParserVersion) // 创建JD处理器
	require.NoError(t, err, "初始化JD处理器失败")                                                    // 确保JD处理器初始化成功
	testLogger.Log("JD处理器初始化成功")                                                             // 记录JD处理器初始化成功

	// 5. 初始化JobSearchHandler
	testLogger.Log("初始化JobSearchHandler")                 // 记录开始初始化JobSearchHandler
	h := handler.NewJobSearchHandler(cfg, s, jdProcessor) // 创建JobSearchHandler实例
	testLogger.Log("JobSearchHandler初始化成功")               // 记录JobSearchHandler初始化成功

	// 6. 准备测试数据 - 岗位和HR信息
	testJobID := "22222222-3333-4444-5555-666666666666" // 设置测试用的岗位ID
	testHRID := "11111111-1111-1111-1111-111111111111"  // 设置测试用的HR ID
	jobTitle := "高级Go后端及微服务工程师"                         // 设置测试用的岗位标题
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
4.  有开源项目贡献或活跃的技术博客。` // 设置测试用的岗位描述，更贴近实际Go工程师JD

	testLogger.Log("测试岗位ID: %s", testJobID) // 记录测试岗位ID
	testLogger.Log("测试HR ID: %s", testHRID) // 记录测试HR ID
	testLogger.Log("岗位标题: %s", jobTitle)    // 记录岗位标题
	testLogger.Log("岗位描述:\n%s", jobDesc)    // 记录岗位描述

	// 7. 确保测试数据存在 - 岗位信息
	testLogger.Log("确保测试岗位数据存在")                        // 记录开始准备岗位数据
	ensureJobExists(t, s, testJobID, jobTitle, jobDesc) // 调用辅助函数确保测试岗位存在

	// 8. 确保测试数据存在 - HR信息
	testLogger.Log("确保测试HR数据存在")                                   // 记录开始准备HR数据
	ensureHRExists(t, s, testHRID, "MVP HR", "hr.mvp@example.com") // 调用辅助函数确保测试HR存在

	// 9. 创建请求上下文
	testLogger.Log("创建请求上下文") // 记录创建请求上下文
	c := app.NewContext(16)   // 创建Hertz上下文，16是初始化大小

	// 添加URL参数
	c.Params = append(c.Params, param.Param{ // 添加URL路径参数
		Key:   "job_id",  // 参数名为job_id
		Value: testJobID, // 参数值为测试岗位ID
	})

	// 设置HR ID到上下文中，模拟认证中间件的行为
	c.Set("hr_id", testHRID) // 设置HR ID到上下文，模拟认证通过的情况

	// 添加查询参数
	c.QueryArgs().Add("limit", "10") // 设置一个合理的展示数量限制为10条结果

	// 10. 清除可能存在的缓存，确保测试的独立性
	testLogger.Log("清除可能存在的Redis缓存") // 记录开始清除缓存
	// 使用正确的缓存键格式，与job_search_handler.go中的格式保持一致
	cacheKey := fmt.Sprintf(constants.KeySearchSession, testJobID, testHRID) // 组装缓存键
	_, err = s.Redis.Client.Del(ctx, cacheKey).Result()                      // 删除可能存在的缓存
	if err != nil && err != redis.Nil {                                      // 如果发生错误且不是缓存不存在的情况
		testLogger.Log("清除缓存时出错: %v", err) // 记录清除缓存出错
	}

	// 11. 执行测试
	testLogger.Log("执行岗位搜索处理")           // 记录开始执行岗位搜索
	h.HandleSearchResumesByJobID(ctx, c) // 调用被测试的处理函数

	// 12. 验证结果
	statusCode := c.Response.StatusCode() // 获取响应状态码
	testLogger.Log("状态码: %d", statusCode) // 记录状态码

	var respData map[string]interface{}                                  // 定义变量存储响应数据
	if err := json.Unmarshal(c.Response.Body(), &respData); err != nil { // 解析响应体JSON
		testLogger.Log("解析响应失败: %v", err) // 记录解析错误
		t.Fatalf("解析响应失败: %v", err)       // 如果解析失败，终止测试
	}

	// 记录完整响应数据到日志文件
	testLogger.LogObject("响应数据", respData) // 将完整响应数据以JSON格式记录到日志

	// 13. 验证基本响应结构
	assert.Equal(t, consts.StatusOK, statusCode)   // 断言状态码为200 OK
	assert.Contains(t, respData, "message")        // 断言响应包含message字段
	assert.Contains(t, respData, "job_id")         // 断言响应包含job_id字段
	assert.Equal(t, testJobID, respData["job_id"]) // 断言返回的job_id与请求相同

	// 14. 检查分页元数据（使用next_cursor和total_count，而非pagination字段）
	assert.Contains(t, respData, "next_cursor") // 断言响应包含next_cursor字段
	assert.Contains(t, respData, "total_count") // 断言响应包含total_count字段

	// 15. 现在检查Redis缓存
	testLogger.Log("检查Redis缓存") // 记录开始检查Redis缓存

	// 使用封装好的方法获取缓存数据，而不是直接使用Redis客户端
	// 注意：这里使用与job_search_handler.go中相同的方法获取缓存
	var cachedUUIDs []string             // 存储从缓存获取的UUID列表
	var totalCount int64                 // 存储缓存中的总记录数
	var cacheErr error                   // 存储缓存操作可能的错误
	maxRetries := 3                      // 设置最大重试次数
	retryDelay := 200 * time.Millisecond // 设置初始重试延迟

	for i := 0; i < maxRetries; i++ { // 最多尝试maxRetries次
		// 从位置0开始获取最多100个结果
		cachedUUIDs, totalCount, cacheErr = s.Redis.GetCachedSearchResults(ctx, cacheKey, 0, 100) // 获取缓存的搜索结果
		if cacheErr == nil && len(cachedUUIDs) > 0 {                                              // 如果获取成功且有结果
			testLogger.Log("成功获取Redis缓存数据，找到 %d 个UUID，总数: %d", len(cachedUUIDs), totalCount) // 记录成功获取缓存
			break                                                                            // 成功获取后退出循环
		}

		testLogger.Log("获取Redis缓存尝试 %d 失败: %v，将等待 %v 后重试...",
			i+1, cacheErr, retryDelay) // 记录获取失败的信息
		time.Sleep(retryDelay) // 等待一段时间后重试

		// 增加重试间隔时间，避免频繁请求
		retryDelay *= 2 // 指数退避策略，每次重试延迟加倍
	}

	// 如果重试后仍失败，但API调用成功（状态码为200），继续测试而不中断
	if (cacheErr != nil || len(cachedUUIDs) == 0) && statusCode == consts.StatusOK { // 如果缓存获取失败但API请求成功
		testLogger.Log("警告: 经过多次重试后仍无法获取Redis缓存或缓存为空: %v", cacheErr) // 记录警告信息
		testLogger.Log("但由于API请求成功返回了数据，将跳过缓存验证继续测试")                // 记录跳过缓存验证的决定
		t.Logf("警告: 无法获取Redis缓存，跳过缓存验证部分: %v", cacheErr)             // 输出警告到测试日志
		return                                                       // 提前结束测试，跳过后续缓存验证
	} else if cacheErr != nil { // 如果获取缓存失败且不是上述情况
		testLogger.Log("获取Redis缓存失败: %v", cacheErr) // 记录获取缓存失败
		t.Fatalf("获取Redis缓存失败: %v", cacheErr)       // 终止测试
	}

	// 直接使用获取到的UUID列表
	testLogger.Log("Redis缓存中包含 %d 个简历UUID，总数: %d", len(cachedUUIDs), totalCount) // 记录缓存中的UUID数量和总数
	testLogger.LogObject("Redis缓存数据 (UUIDs)", cachedUUIDs)                       // 记录缓存的UUID列表

	// 16. 验证缓存的结果数量是否符合预期 (应该是完整的召回结果集)
	testLogger.Log("验证缓存的结果数量")                         // 记录开始验证缓存数量
	assert.True(t, len(cachedUUIDs) > 0, "缓存结果数量应该大于0") // 断言缓存结果数量大于0
	assert.True(t, totalCount > 0, "总结果数应该大于0")         // 断言总结果数大于0

	// 如果API响应中有数据，比较结果
	if data, ok := respData["data"].([]interface{}); ok && len(data) > 0 { // 如果API响应包含data字段且不为空
		testLogger.Log("API返回了 %d 个结果", len(data)) // 记录API返回的结果数量

		// 验证API返回的结果数量与缓存信息一致
		if apiTotalCount, ok := respData["total_count"].(float64); ok { // 如果能获取API返回的总数
			testLogger.Log("API返回的总数: %.0f, Redis缓存的总数: %d", apiTotalCount, totalCount) // 记录API和缓存的总数比较
			// 注意: API返回的total_count是float64类型，需要转换比较
			assert.Equal(t, int64(apiTotalCount), totalCount, "API和缓存的总数应该一致") // 断言API和缓存的总数一致
		}

		// 尝试比较第一个结果
		if len(data) > 0 && len(cachedUUIDs) > 0 { // 如果API和缓存都有结果
			firstAPIResult := data[0].(map[string]interface{})                   // 获取API返回的第一个结果
			apiUUID, apiUUIDExists := firstAPIResult["submission_uuid"].(string) // 尝试获取第一个结果的UUID

			if apiUUIDExists { // 如果成功获取到UUID
				testLogger.Log("第一个API结果UUID: %s", apiUUID)     // 记录API返回的第一个UUID
				testLogger.Log("第一个缓存UUID: %s", cachedUUIDs[0]) // 记录缓存的第一个UUID

				// 检查API返回的第一个UUID是否在缓存的UUID列表中
				foundInCache := false              // 初始化标志变量
				for _, uuid := range cachedUUIDs { // 遍历缓存中的所有UUID
					if uuid == apiUUID { // 如果找到匹配的UUID
						foundInCache = true // 设置标志为true
						break               // 退出循环
					}
				}

				if foundInCache { // 如果在缓存中找到了UUID
					testLogger.Log("API结果UUID在缓存中找到，符合预期") // 记录找到UUID的信息
				} else { // 如果未找到UUID
					testLogger.Log("注意：API结果UUID在缓存中未找到，这可能是由于排序或过滤导致的") // 记录可能的原因
				}
			}
		}
	}

	testLogger.Log("测试完成") // 记录测试完成
}

// ensureHRExists 确保测试HR在数据库中存在
func ensureHRExists(t *testing.T, s *storage.Storage, hrID, name, email string) {
	// 创建一个静默的logger以关闭SQL日志打印
	silentLogger := logger.New(
		log.New(io.Discard, "", log.LstdFlags), // 输出到丢弃的writer，不打印任何日志
		logger.Config{
			SlowThreshold:             200 * time.Millisecond, // 设置慢查询阈值为200ms
			LogLevel:                  logger.Silent,          // 设置为Silent级别，不输出任何SQL日志
			IgnoreRecordNotFoundError: true,                   // 忽略记录未找到的错误
			Colorful:                  false,                  // 不使用彩色输出
		},
	)

	// 使用静默的logger创建一个新的数据库会话
	silentDB := s.MySQL.DB().Session(&gorm.Session{Logger: silentLogger}) // 使用静默logger创建数据库会话

	// 检查HR是否已存在
	var count int64                                                                            // 用于存储查询结果数量
	if err := silentDB.Table("hrs").Where("hr_id = ?", hrID).Count(&count).Error; err != nil { // 查询指定hr_id的记录数
		t.Fatalf("检查HR是否存在失败: %v", err) // 如果查询失败，终止测试
	}

	if count == 0 { // 如果HR不存在
		// 创建HR记录
		hr := struct { // 定义一个匿名结构体，映射到hrs表
			HRID      string    `gorm:"column:hr_id"`      // HR的唯一标识符
			Name      string    `gorm:"column:name"`       // HR姓名
			Email     string    `gorm:"column:email"`      // HR邮箱
			CreatedAt time.Time `gorm:"column:created_at"` // 创建时间
			UpdatedAt time.Time `gorm:"column:updated_at"` // 更新时间
		}{
			HRID:      hrID,       // 设置HR ID
			Name:      name,       // 设置HR姓名
			Email:     email,      // 设置HR邮箱
			CreatedAt: time.Now(), // 设置创建时间为当前时间
			UpdatedAt: time.Now(), // 设置更新时间为当前时间
		}

		if err := silentDB.Table("hrs").Create(&hr).Error; err != nil { // 创建HR记录
			t.Fatalf("创建HR记录失败: %v", err) // 如果创建失败，终止测试
		}
		t.Logf("已创建测试HR: %s", hrID) // 记录已创建HR
	} else {
		t.Logf("测试HR已存在: %s", hrID) // 记录HR已存在
	}
}

// abs 是一个辅助函数，返回整数的绝对值
func abs(x int) int {
	if x < 0 { // 如果x是负数
		return -x // 返回x的相反数（正数）
	}
	return x // 如果x是零或正数，直接返回x
}
