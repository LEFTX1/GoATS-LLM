package api_test

import (
	"ai-agent-go/pkg/api"
	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/handler"
	"ai-agent-go/pkg/parser"
	"ai-agent-go/pkg/storage"
	"ai-agent-go/pkg/storage/singleton"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/common/test/assert"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 全局测试变量
var (
	cfg           *config.Config
	testCfg       *singleton.TestConfig
	minioAdapter  *storage.MinIOAdapter
	mqAdapter     *storage.RabbitMQAdapter
	mysqlAdapter  *storage.MySQLAdapter
	pdfExtractor  *parser.EinoPDFTextExtractor
	resumeHandler *handler.ResumeHandler
)

// 初始化测试环境
func setupTest(t *testing.T) {
	t.Helper() // 标记这是一个辅助函数

	// 初始化配置 - 严格从文件加载，不使用环境变量覆盖
	var err error
	configPath := singleton.FindTestConfigFile()
	// 加载 TestConfig (用于测试桶名等)，确保不依赖环境变量
	testCfg, err = singleton.LoadTestConfigNoEnv(configPath)
	if err != nil {
		t.Fatalf("加载测试特定配置 (TestConfig) 失败: %v", err)
	}
	// 加载主 Config，确保不依赖环境变量
	cfg, err = config.LoadConfigFromFileOnly(configPath) // 使用新函数
	if err != nil {
		t.Fatalf("加载基础配置 (Config) 失败: %v", err)
	}

	// 对测试配置进行调整，使用测试用的交换机和队列名等
	testTimestamp := fmt.Sprintf("%d", time.Now().UnixNano())
	cfg.MinIO.BucketName = testCfg.TestBucketName
	cfg.RabbitMQ.ResumeEventsExchange = "test-resume-events-" + testTimestamp
	cfg.RabbitMQ.RawResumeQueue = "test-raw-resume-" + testTimestamp
	cfg.RabbitMQ.ProcessingEventsExchange = "test-processing-events-" + testTimestamp
	cfg.RabbitMQ.LLMParsingQueue = "test-llm-parsing-" + testTimestamp

	// 重置所有单例以确保测试环境干净
	singleton.ResetMinIOAdapter()
	singleton.ResetMySQLAdapter()
	singleton.ResetRabbitMQAdapter()
	singleton.ResetQdrantClient()

	// 初始化依赖项 - 使用单例模式
	// MinIO
	minioAdapter, err = singleton.GetMinIOAdapter(&cfg.MinIO)
	if err != nil {
		t.Fatalf("初始化MinIO适配器失败: %v", err)
	}

	// RabbitMQ
	mqAdapter, err = singleton.GetRabbitMQAdapter(&cfg.RabbitMQ)
	if err != nil {
		t.Fatalf("初始化RabbitMQ适配器失败: %v", err)
	}

	// MySQL
	// 不再强制添加 "_test" 后缀，直接使用 config.yaml 中定义的数据库名
	mysqlAdapter, err = singleton.GetMySQLAdapter(&cfg.MySQL)
	if err != nil {
		t.Fatalf("初始化MySQL适配器失败: %v", err)
	}

	// 创建必要的RabbitMQ交换机和队列
	ctx := context.Background()

	// 确保存在测试所需的交换机
	if err := mqAdapter.EnsureExchange(cfg.RabbitMQ.ResumeEventsExchange, "topic", true); err != nil {
		t.Fatalf("创建简历事件交换机失败: %v", err)
	}
	if err := mqAdapter.EnsureExchange(cfg.RabbitMQ.ProcessingEventsExchange, "topic", true); err != nil {
		t.Fatalf("创建处理事件交换机失败: %v", err)
	}

	// 确保存在测试所需的队列
	rawResumeQueue, err := mqAdapter.EnsureQueue(cfg.RabbitMQ.RawResumeQueue, true)
	if err != nil {
		t.Fatalf("创建原始简历队列失败: %v", err)
	}

	// 绑定队列到交换机
	if err := mqAdapter.BindQueue(rawResumeQueue.Name, cfg.RabbitMQ.ResumeEventsExchange, cfg.RabbitMQ.UploadedRoutingKey); err != nil {
		t.Fatalf("绑定原始简历队列失败: %v", err)
	}

	// PDF提取器
	pdfExtractor, err = parser.NewEinoPDFTextExtractor(ctx)
	if err != nil {
		t.Fatalf("初始化PDF提取器失败: %v", err)
	}

	// LLM Chunker (假设不需要真正的LLM进行测试)
	llmChunker := parser.NewLLMResumeChunker(nil)

	// 初始化ResumeHandler
	resumeHandler = handler.NewResumeHandler(
		cfg,
		minioAdapter,
		mqAdapter,
		mysqlAdapter,
		pdfExtractor,
		llmChunker,
	)
}

// 清理测试环境
func cleanupTest(t *testing.T) {
	t.Helper()

	// 删除测试桶中的所有对象
	// 注意：在真实测试中，我们应该有更精确的清理，只删除测试创建的对象
	log.Println("清理测试环境...")

	// 不需要关闭连接，因为单例管理了连接的生命周期
	// 但我们仍可以重置单例以释放资源
	singleton.ResetMinIOAdapter()
	singleton.ResetMySQLAdapter()
	singleton.ResetRabbitMQAdapter()
	singleton.ResetQdrantClient()
}

// TestUploadResume 测试简历上传API
func TestUploadResume(t *testing.T) {
	// 设置测试环境
	setupTest(t)
	defer cleanupTest(t)
	// 初始化API处理器
	resumeAPIHandler := api.NewResumeAPIHandler(resumeHandler)
	// 准备测试文件路径
	testPDFPath := filepath.Join("..", "..", "testdata", "黑白整齐简历模板 (6).pdf")
	fmt.Printf("测试文件路径: %s\n", testPDFPath)
	// 打开文件
	file, err := os.Open(testPDFPath)
	if err != nil {
		t.Fatalf("打开测试PDF文件失败: %v", err)
	}
	defer file.Close()

	// 获取文件信息
	fileInfo, err := file.Stat()
	if err != nil {
		t.Fatalf("获取文件信息失败: %v", err)
	}

	// 读取整个文件内容
	fileContent, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("读取文件内容失败: %v", err)
	}

	// 创建测试请求 - 使用multipart/form-data
	bodyBuf := &bytes.Buffer{}
	bodyWriter := multipart.NewWriter(bodyBuf)

	// 添加文件
	fileWriter, err := bodyWriter.CreateFormFile("resume_file", fileInfo.Name())
	if err != nil {
		t.Fatalf("创建表单文件字段失败: %v", err)
	}
	_, err = fileWriter.Write(fileContent)
	if err != nil {
		t.Fatalf("写入文件内容失败: %v", err)
	}

	// 添加其他表单字段
	_ = bodyWriter.WriteField("target_job_id", "test_job_123")
	_ = bodyWriter.WriteField("source_channel", "api_test")

	// 关闭bodyWriter，完成请求体构建
	if err := bodyWriter.Close(); err != nil {
		t.Fatalf("关闭bodyWriter失败: %v", err)
	}

	// 创建测试请求上下文
	contentType := bodyWriter.FormDataContentType()
	body := ut.Body{
		Body: bytes.NewReader(bodyBuf.Bytes()),
		Len:  bodyBuf.Len(),
	}
	// 使用标准的Hertz测试方法创建请求上下文
	c := ut.CreateUtRequestContext(consts.MethodPost, "/api/v1/resume/upload", &body, ut.Header{
		Key:   "Content-Type",
		Value: contentType,
	})

	// 调用API处理函数
	resumeAPIHandler.UploadResume(context.Background(), c)

	// 检查响应
	assert.DeepEqual(t, consts.StatusOK, c.Response.StatusCode())

	// 解析响应体JSON
	respBody := c.Response.Body()
	var respData map[string]interface{}
	err = json.Unmarshal(respBody, &respData)
	if err != nil {
		t.Fatalf("解析响应JSON失败: %v", err)
	}

	// 验证返回的字段
	submissionUUID, ok := respData["submission_uuid"].(string)
	assert.True(t, ok)
	assert.NotEqual(t, "", submissionUUID) // 确认UUID不为空

	status, ok := respData["status"].(string)
	assert.True(t, ok)
	assert.DeepEqual(t, "SUBMITTED_FOR_PROCESSING", status)

	fmt.Printf("上传成功，submission_uuid: %s\n", submissionUUID)
}

// 如果需要，可以添加更多的测试用例，如:
// - 测试文件过大
// - 测试非PDF文件
// - 测试缺少必须的参数
// - 测试服务器内部错误情况
